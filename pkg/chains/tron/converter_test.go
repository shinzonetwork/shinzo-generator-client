package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// loadFixtureBytes reads one golden fixture from the package testdata dir.
func loadFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err, "golden fixture %s must ship with the package", name)
	return data
}

// loadGoldenBundle assembles the populated golden block and its receipts
// into a bundle, the same shape FetchBlock returns.
func loadGoldenBundle(t *testing.T) (*BlockBundle, error) {
	t.Helper()
	var block Block
	require.NoError(t, json.Unmarshal(loadFixtureBytes(t, "block_by_num.json"), &block))
	require.NotEmpty(t, block.BlockID, "golden block must not be the empty not-found marker")

	var receipts []*TransactionInfo
	require.NoError(t, json.Unmarshal(loadFixtureBytes(t, "receipts.json"), &receipts))
	require.Len(t, receipts, 3)

	transactions := make([]*Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}
	return &BlockBundle{
		Block:        &block,
		Transactions: transactions,
		Receipts:     receipts,
	}, nil
}

// goldenConverter builds a converter with the populated block's config.
func goldenConverter() *Converter {
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet"},
		Indexer: config.IndexerConfig{
			MaxDocsPerTxn:      37,
			MaxTxDocsPerBatch:  21,
			MaxLogDocsPerBatch: 33,
		},
	}
	return NewConverter(cfg)
}

// TestConvert_GoldenPopulatedBlock runs the golden workspace block through
// the converter and asserts every group, its order, and the important
// field derivations.
func TestConvert_GoldenPopulatedBlock(t *testing.T) {
	t.Parallel()
	bundle, err := loadGoldenBundle(t)
	require.NoError(t, err)

	result, err := goldenConverter().Convert(context.Background(), bundle)
	require.NoError(t, err)

	require.Len(t, result.Groups, 4)
	assert.Equal(t, DefaultCollectionBlock, result.Groups[0].Collection)
	assert.Equal(t, DefaultCollectionTransaction, result.Groups[1].Collection)
	assert.Equal(t, DefaultCollectionLog, result.Groups[2].Collection)
	assert.Equal(t, DefaultCollectionInternalTransfer, result.Groups[3].Collection)
	assert.Equal(t, DefaultCollectionBlockSignature, result.SignatureCollection)

	// Block group: blockID, header fields, and the tx count.
	blockDoc := result.Groups[0].Docs[0]
	assert.Equal(t, "00000000000f4241a1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef012", blockDoc[constants.HashFieldName])
	assert.Equal(t, int64(1000001), blockDoc[constants.NumberFieldName])
	assert.Equal(t, int64(1578594884000), blockDoc[TimestampFieldName])
	assert.Equal(t, "00000000000f4240c0ded9a72face835b8bfe8d5d43dee8d60dd57fbf1ecfca9", blockDoc[ParentHashFieldName])
	assert.Equal(t, "41b2f713d57dbcec679d93a8849fa0cd0e4db594ba", blockDoc[WitnessAddressFieldName])
	assert.Equal(t, "5b3cf6cb15d52947989f7726f4907a144b39ccd667a1a0f98707b40cdfe65b96173ddf34ae8dcc5e78f136e0cf903a15c7128984aa2191f02333209d1879d3f900", blockDoc[WitnessSignatureFieldName])
	assert.Equal(t, int64(3), blockDoc[TransactionCountFieldName])

	// Transaction group: TRX transfer, contract call, and asset transfer.
	txDocs := result.Groups[1].Docs
	require.Len(t, txDocs, 3)

	assert.Equal(t, "f52df87da4991d1cd8333f48526b2f55375a22e2705e9eaf7edce2a12e53827b", txDocs[0][constants.HashFieldName])
	assert.Equal(t, "00000000000f4241a1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef012", txDocs[0][constants.BlockHashFieldName])
	assert.Equal(t, int64(1000001), txDocs[0][constants.BlockNumberFieldName])
	assert.Equal(t, "TransferContract", txDocs[0][TypeFieldName])
	assert.Equal(t, "41072a35d3cd7112c08fe2b04f5c99e73e0a3c099e", txDocs[0][FromFieldName])
	assert.Equal(t, "41118f7b4382bea375ee420f1b211a4619f3ba78ad", txDocs[0][ToFieldName])
	assert.Equal(t, int64(1000000), txDocs[0][AmountFieldName])
	assert.Equal(t, int64(268000), txDocs[0][FeeFieldName])
	assert.Equal(t, "SUCCESS", txDocs[0][RetFieldName])

	assert.Equal(t, "TriggerSmartContract", txDocs[1][TypeFieldName])
	assert.Equal(t, "41a614f803b6fd780986a42c78ec9c7f77e6ded13c", txDocs[1][ToFieldName], "contract address fills in for recipient")
	assert.Equal(t, int64(0), txDocs[1][AmountFieldName])
	assert.Equal(t, int64(345000), txDocs[1][FeeFieldName])
	assert.Equal(t, int64(64284), txDocs[1][EnergyUsageFieldName])

	assert.Equal(t, "TransferAssetContract", txDocs[2][TypeFieldName])
	assert.Equal(t, "41118f7b4382bea375ee420f1b211a4619f3ba78ad", txDocs[2][ToFieldName])
	assert.Equal(t, int64(5000000), txDocs[2][AmountFieldName])

	// rawData round-trips the tx's raw_data as a JSON string.
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(txDocs[1][RawDataFieldName].(string)), &raw))
	assert.Equal(t, float64(1500000000), raw["fee_limit"])

	// Log group: three events, including the deliberately duplicated
	// transfer event; logIndex disambiguates it.
	logDocs := result.Groups[2].Docs
	require.Len(t, logDocs, 3)

	assert.Equal(t, "a614f803b6fd780986a42c78ec9c7f77e6ded13c", logDocs[0][AddressFieldName])
	assert.Equal(t, "e32493469c2ab6f31684c0e5a88cf409329fd9d7176baed4763d294d1c99b1f0", logDocs[0][TransactionHashFieldName])
	assert.Equal(t, "00000000000f4241a1b2c3d4e5f60718293a4b5c6d7e8f9012345678abcdef012", logDocs[0][constants.BlockHashFieldName])
	assert.Equal(t, int64(1000001), logDocs[0][constants.BlockNumberFieldName])
	assert.Equal(t, 1, logDocs[0][TransactionIndexFieldName], "the contract tx is the block's second transaction")
	assert.Equal(t, 0, logDocs[0][LogIndexFieldName])
	assert.Equal(t, "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", logDocs[0][TopicsFieldName].([]string)[0])

	assert.Equal(t, 1, logDocs[1][LogIndexFieldName])
	assert.Equal(t, "8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925", logDocs[1][TopicsFieldName].([]string)[0])

	assert.Equal(t, logDocs[0][TopicsFieldName], logDocs[2][TopicsFieldName], "the duplicated event keeps its content")
	assert.Equal(t, 2, logDocs[2][LogIndexFieldName], "the duplicated event carries its own ordinal, so its docID stays unique")

	// InternalTransfer group: TRX movement with a rejected no-value entry.
	transferDocs := result.Groups[3].Docs
	require.Len(t, transferDocs, 2)

	assert.Equal(t, "41a614f803b6fd780986a42c78ec9c7f77e6ded13c", transferDocs[0][FromFieldName])
	assert.Equal(t, "41118f7b4382bea375ee420f1b211a4619f3ba78ad", transferDocs[0][ToFieldName])
	assert.Equal(t, int64(700000), transferDocs[0][AmountFieldName])
	assert.Equal(t, "", transferDocs[0][AssetNameFieldName])
	assert.Equal(t, false, transferDocs[0][RejectedFieldName])
	assert.Equal(t, "e32493469c2ab6f31684c0e5a88cf409329fd9d7176baed4763d294d1c99b1f0", transferDocs[0][TransactionHashFieldName])
	assert.Equal(t, 0, transferDocs[0][TransferIndexFieldName])

	assert.Equal(t, true, transferDocs[1][RejectedFieldName])
	assert.Equal(t, int64(0), transferDocs[1][AmountFieldName])
	assert.Equal(t, 1, transferDocs[1][TransferIndexFieldName])
}

// TestConvert_GoldenEmptyBlock runs the empty block through the converter:
// exactly one block group, no data groups.
func TestConvert_GoldenEmptyBlock(t *testing.T) {
	t.Parallel()
	var block Block
	require.NoError(t, json.Unmarshal(loadFixtureBytes(t, "empty_block_by_num.json"), &block))
	require.NotEmpty(t, block.BlockID)

	var receipts []*TransactionInfo
	require.NoError(t, json.Unmarshal(loadFixtureBytes(t, "empty_receipts.json"), &receipts))
	require.Empty(t, receipts)

	result, err := goldenConverter().Convert(context.Background(), &BlockBundle{
		Block:        &block,
		Transactions: nil,
		Receipts:     receipts,
	})
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, DefaultCollectionBlock, result.Groups[0].Collection)
	assert.Equal(t, int64(0), result.Groups[0].Docs[0][TransactionCountFieldName])
}

// TestConvert_BatchSizing sizes groups from config with the documented
// fallback chain, and reuses the log batch size for internal transfers.
func TestConvert_BatchSizing(t *testing.T) {
	t.Parallel()
	bundle, err := loadGoldenBundle(t)
	require.NoError(t, err)

	result, err := goldenConverter().Convert(context.Background(), bundle)
	require.NoError(t, err)
	require.Len(t, result.Groups, 4)
	assert.Equal(t, 37, result.Groups[0].BatchSize, "block group uses MaxDocsPerTxn")
	assert.Equal(t, 21, result.Groups[1].BatchSize, "transaction group uses MaxTxDocsPerBatch")
	assert.Equal(t, 33, result.Groups[2].BatchSize, "log group uses MaxLogDocsPerBatch")
	assert.Equal(t, 33, result.Groups[3].BatchSize, "internal transfers share the log batch size")

	fallback, err := NewConverter(nil).Convert(context.Background(), bundle)
	require.NoError(t, err)
	require.Len(t, fallback.Groups, 4)
	for _, group := range fallback.Groups {
		assert.Equal(t, 1000, group.BatchSize, "nil config falls back to the default batch size")
	}
}

// TestConvert_RejectsOtherTypes covers the type assertions.
func TestConvert_RejectsOtherTypes(t *testing.T) {
	t.Parallel()
	converter := goldenConverter()

	_, err := converter.Convert(context.Background(), "not a bundle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *BlockBundle")

	_, err = converter.Convert(context.Background(), nil)
	require.Error(t, err)

	_, err = converter.Convert(context.Background(), &BlockBundle{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil block")
}

// TestChainPrefixFromConfig covers prefix derivation and its fallbacks.
func TestChainPrefixFromConfig(t *testing.T) {
	t.Parallel()
	assert.Equal(t, DefaultCollectionPrefix, chainPrefixFromConfig(nil))
	assert.Equal(t, DefaultCollectionPrefix, chainPrefixFromConfig(&config.Config{}))
	assert.Equal(t, DefaultCollectionPrefix, chainPrefixFromConfig(&config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet"},
	}))
	assert.Equal(t, "Tron__Nile", chainPrefixFromConfig(&config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Nile"},
	}))
}

// ---------------------------------------------------------------------------
// Progress queries (require real DefraDB)
// ---------------------------------------------------------------------------

func TestGetHighestStoredBlockNumber_NoBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())

	td := setupTronDefraDB(t, c)
	_, err := c.GetHighestStoredBlockNumber(context.Background(), td.Node)
	assert.Error(t, err)
}

func TestGetLowestStoredBlockNumber_NoBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())

	td := setupTronDefraDB(t, c)
	_, err := c.GetLowestStoredBlockNumber(context.Background(), td.Node)
	assert.Error(t, err)
}

func TestGetDocIDsByBlockRange_Empty(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())

	td := setupTronDefraDB(t, c)
	result, err := c.GetDocIDsByBlockRange(context.Background(), td.Node, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestGetStoredBlockNumbers_WithBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())
	td := setupTronDefraDB(t, c)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102} {
		storeTestBlockDoc(ctx, t, td, c, num)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(102), highest)
}

// TestGetLowestStoredBlockNumber_AfterPurge is the regression test for the
// 2026-09-02 incident (first hit by the EVM adapter): a PurgeByDocIDs
// residue left a dangling row behind, so the ASC lowest-block query kept
// returning a row whose number field was unparseable. After purging the
// lowest blocks, the query must return the next stored block without
// error.
func TestGetLowestStoredBlockNumber_AfterPurge(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())
	td := setupTronDefraDB(t, c)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeTestBlockDoc(ctx, t, td, c, num)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	docIDs, err := c.queryCollectionDocIDs(ctx, td.Node, c.collections.Block, constants.NumberFieldName, 100, 101)
	require.NoError(t, err)
	require.Len(t, docIDs, 2)

	col, err := td.Node.DB.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	purgeIDs := make([]client.DocID, 0, len(docIDs))
	for _, id := range docIDs {
		docID, err := client.NewDocIDFromString(id)
		require.NoError(t, err)
		purgeIDs = append(purgeIDs, docID)
	}
	require.NoError(t, col.PurgeByDocIDs(ctx, purgeIDs, true))

	lowest, err = c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(102), lowest)

	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(104), highest)
}

// TestGetStoredBlockNumber_ResidueRows exercises the block-number queries
// against numberless rows (purge residue / P2P replication): the `_geq: 0`
// filter must exclude them so the queries return the next real block at the
// default limit of 1, and a collection holding only numberless rows must
// surface the corruption sentinel rather than "not found", so the pruner
// hard-fails with ErrNoValidBlocks instead of silently skipping pruning.
func TestGetStoredBlockNumber_ResidueRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter)
		wantLow  int64
		wantHigh int64
		wantErr  bool
	}{
		{
			name: "residue rows around valid blocks are skipped",
			setup: func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter) {
				storeResidueBlockDoc(ctx, t, td, c, 1)
				for _, num := range []int64{100, 101, 102} {
					storeTestBlockDoc(ctx, t, td, c, num)
				}
				storeResidueBlockDoc(ctx, t, td, c, 2)
			},
			wantLow:  100,
			wantHigh: 102,
		},
		{
			name: "only numberless rows yields the corruption sentinel",
			setup: func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter) {
				for i := range 3 {
					storeResidueBlockDoc(ctx, t, td, c, int64(i))
				}
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(tronTestConfig())
			td := setupTronDefraDB(t, c)
			ctx := context.Background()

			tc.setup(ctx, t, td, c)

			lowest, errLow := c.GetLowestStoredBlockNumber(ctx, td.Node)
			highest, errHigh := c.GetHighestStoredBlockNumber(ctx, td.Node)

			if tc.wantErr {
				require.Error(t, errLow)
				assert.ErrorIs(t, errLow, chains.ErrBlockNumberCorrupt)
				require.Error(t, errHigh)
				assert.ErrorIs(t, errHigh, chains.ErrBlockNumberCorrupt)
				return
			}
			require.NoError(t, errLow)
			assert.Equal(t, tc.wantLow, lowest)
			require.NoError(t, errHigh)
			assert.Equal(t, tc.wantHigh, highest)
		})
	}
}

// TestGetDocIDsByBlockRange_Populated covers the tron multi-collection path:
// block and transaction documents stored through the production write path
// are found by their block-number fields, while collections without docs in
// range stay absent from the result map.
func TestGetDocIDsByBlockRange_Populated(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())
	td := setupTronDefraDB(t, c)
	ctx := context.Background()

	storeTestBlockDoc(ctx, t, td, c, 100)
	storeTestTxnDoc(ctx, t, td, c, 100)

	result, err := c.GetDocIDsByBlockRange(ctx, td.Node, 100, 100)
	require.NoError(t, err)
	assert.Len(t, result[c.collections.Block], 1)
	assert.Len(t, result[c.collections.Transaction], 1)

	empty, err := c.GetDocIDsByBlockRange(ctx, td.Node, 200, 300)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// storeTestTxnDoc writes a single transaction document through the same
// write path used in production.
func storeTestTxnDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, num int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Transaction)
	require.NoError(t, err)

	tx := &Transaction{TxID: fakeTronBlockID(num + 80000)}
	doc, err := client.NewDocFromMap(ctx, buildTransactionData(fakeTronBlock(num), tx, nil), col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}

func TestParseBlockNumberRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		row   map[string]any
		want  int64
		valid bool
	}{
		{"float64", map[string]any{constants.NumberFieldName: float64(100)}, 100, true},
		{"int64", map[string]any{constants.NumberFieldName: int64(101)}, 101, true},
		{"int", map[string]any{constants.NumberFieldName: int(102)}, 102, true},
		{"nil", map[string]any{constants.NumberFieldName: nil}, 0, false},
		{"missing", map[string]any{}, 0, false},
		{"string", map[string]any{constants.NumberFieldName: "100"}, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, valid := parseBlockNumberRow(tc.row)
			assert.Equal(t, tc.valid, valid)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestQueryBlockNumber_CustomLimits pins the parameterized query limit: every
// supported limit must build valid GraphQL, honor the ordering, and stay safe
// when the limit exceeds the number of stored blocks.
func TestQueryBlockNumber_CustomLimits(t *testing.T) {
	t.Parallel()
	c := NewConverter(tronTestConfig())
	td := setupTronDefraDB(t, c)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeTestBlockDoc(ctx, t, td, c, num)
	}

	tests := []struct {
		order string
		limit int
		want  int64
	}{
		{"ASC", 1, 100},
		{"ASC", 2, 100},
		{"ASC", 3, 100},
		{"ASC", 7, 100},
		{"ASC", 20, 100},
		{"ASC", 50, 100},
		{"DESC", 1, 104},
		{"DESC", 3, 104},
		{"DESC", 7, 104},
		{"DESC", 20, 104},
		{"DESC", 50, 104},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_limit%d", tc.order, tc.limit), func(t *testing.T) {
			t.Parallel()
			got, err := c.queryBlockNumber(ctx, td.Node, tc.order, "TestQueryBlockNumber_CustomLimits", tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestLowestBlockQueryLimit_ConfigPaths pins the helper's fallback contract:
// nil and unset configs fall back to the exported default; a configured
// window is honored. The config-level SetDefaults only normalizes configs
// that pass through LoadConfig, so the helper guards these paths itself.
func TestLowestBlockQueryLimit_ConfigPaths(t *testing.T) {
	t.Parallel()

	configured := tronTestConfig()
	configured.Converter.LowestBlockQueryLimit = 7

	assert.Equal(t, config.DefaultLowestBlockQueryLimit,
		NewConverter(nil).lowestBlockQueryLimit(), "nil config falls back to default")
	assert.Equal(t, config.DefaultLowestBlockQueryLimit,
		NewConverter(tronTestConfig()).lowestBlockQueryLimit(), "unset config falls back to default")
	assert.Equal(t, 7,
		NewConverter(configured).lowestBlockQueryLimit(), "configured value is honored")
}

// TestGetLowestStoredBlockNumber_ConfigWindow proves the configured window
// is plumbed from config into the lowest-block query end to end.
func TestGetLowestStoredBlockNumber_ConfigWindow(t *testing.T) {
	t.Parallel()
	cfg := tronTestConfig()
	cfg.Converter.LowestBlockQueryLimit = 2
	c := NewConverter(cfg)
	td := setupTronDefraDB(t, c)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeTestBlockDoc(ctx, t, td, c, num)
	}

	got, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), got)
}
