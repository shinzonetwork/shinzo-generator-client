package bitcoin

import (
	"context"
	"fmt"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// ---------------------------------------------------------------------------
// Prefix and delegation
// ---------------------------------------------------------------------------

func TestChainPrefixFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *config.Config
		wantName string
	}{
		{
			name:     "nil config falls back to the default prefix",
			cfg:      nil,
			wantName: "Bitcoin__Mainnet",
		},
		{
			name:     "empty chain name and network fall back per adapter",
			cfg:      &config.Config{},
			wantName: "Bitcoin__Mainnet",
		},
		{
			name: "configured name and network are joined",
			cfg: &config.Config{
				Chain: config.ChainConfig{Name: "Bitcoin", Network: "Signet"},
			},
			wantName: "Bitcoin__Signet",
		},
		{
			name: "only the name configured, network defaults",
			cfg: &config.Config{
				Chain: config.ChainConfig{Name: "TestNode"},
			},
			wantName: "TestNode__Mainnet",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantName, chainPrefixFromConfig(tt.cfg))
		})
	}
}

func TestConverter_SchemaAndCollectionsDelegation(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	for _, want := range []string{
		"Bitcoin__Mainnet__Block",
		"Bitcoin__Mainnet__Transaction",
		"Bitcoin__Mainnet__TxInput",
		"Bitcoin__Mainnet__TxOutput",
		"Bitcoin__Mainnet__BlockSignature",
	} {
		assert.Contains(t, sdl, want)
	}
	assert.NotContains(t, sdl, "Ethereum__Mainnet", "no EVM leak in the merged schema")

	assert.Equal(t, []string{
		"Bitcoin__Mainnet__Block",
		"Bitcoin__Mainnet__BlockSignature",
		"Bitcoin__Mainnet__SnapshotSignature",
		"Bitcoin__Mainnet__Transaction",
		"Bitcoin__Mainnet__TxInput",
		"Bitcoin__Mainnet__TxOutput",
	}, c.GetCollections(), "GetCollections must mirror AllCollections order")
	assert.Equal(t, "Bitcoin__Mainnet__Block", c.Collections().BlockCollection())

	// A custom deployment prefix relabels every collection and the matched
	// schema text.
	signet := NewConverter(&config.Config{
		Chain: config.ChainConfig{Name: "Bitcoin", Network: "Signet"},
	})
	assert.Equal(t, "Bitcoin__Signet__Block", signet.Collections().BlockCollection())
	signetSDL, err := signet.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, signetSDL, "Bitcoin__Signet__Transaction")
	assert.NotContains(t, signetSDL, "Bitcoin__Mainnet", "the placeholder prefix must be fully swapped")
}

// ---------------------------------------------------------------------------
// Convert: errors and shape
// ---------------------------------------------------------------------------

func TestConvert_WrongTypeAndNilBlock(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	_, err := c.Convert(context.Background(), "not a block")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *Block")

	_, err = c.Convert(context.Background(), (*Block)(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil block")
}

func assertDocFields(t *testing.T, doc map[string]any, want map[string]any, absent ...string) {
	t.Helper()
	for field, value := range want {
		require.Contains(t, doc, field)
		assert.Equal(t, value, doc[field], "field %s", field)
	}
	for _, field := range absent {
		_, has := doc[field]
		assert.False(t, has, "field %s must be key-omitted, not zeroed", field)
	}
}

// TestConvert_CoinbaseOnlyBlock pins the four-group emission and the whole
// field mapping of the smallest real block: coinbase transaction, coinbase
// input, subsidy output. Nullable fields that Core does not send are absent,
// never zeroed.
func TestConvert_CoinbaseOnlyBlock(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	block := fakeBlock(7)
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups, 4)

	assert.Equal(t, []string{
		c.collections.Block,
		c.collections.Transaction,
		c.collections.TxInput,
		c.collections.TxOutput,
	}, []string{result.Groups[0].Collection, result.Groups[1].Collection, result.Groups[2].Collection, result.Groups[3].Collection})

	blockDoc := result.Groups[0].Docs[0]
	assertDocFields(t, blockDoc, map[string]any{
		constants.NumberFieldName: int64(7),
		constants.HashFieldName:   block.Hash,
		ParentHashFieldName:       block.PreviousBlockHash,
		"version":                 int64(2),
		MerkleRootFieldName:       block.MerkleRoot,
		TimeFieldName:             int64(1231006505 + 7*600),
		BitsFieldName:             "1a0facdc",
		NonceFieldName:            "7",
		ChainworkFieldName:        block.Chainwork,
	})
	// The wire-only archive-floor fields must not leak into documents.
	assertDocFields(t, blockDoc, nil, "difficulty", "size", "strippedsize", "weight", "confirmations")

	txDoc := result.Groups[1].Docs[0]
	assertDocFields(t, txDoc, map[string]any{
		TxIDFieldName:                  block.Tx[0].TxID,
		"hash":                         block.Tx[0].Hash,
		constants.BlockHashFieldName:   block.Hash,
		constants.BlockNumberFieldName: int64(7),
		TransactionIndexFieldName:      0,
		"version":                      int64(1),
		"locktime":                     int64(0),
		IsCoinbaseFieldName:            true,
	})
	assertDocFields(t, txDoc, nil, FeeSatFieldName, "_blockID", "_transactionID")

	inputDoc := result.Groups[2].Docs[0]
	assertDocFields(t, inputDoc, map[string]any{
		TxIDFieldName:                  block.Tx[0].TxID,
		constants.BlockNumberFieldName: int64(7),
		InputIndexFieldName:            0,
		SequenceFieldName:              int64(4294967295),
		CoinbaseFieldName:              fmt.Sprintf("03%06x", 7),
	})
	assertDocFields(t, inputDoc, nil,
		PrevTxIDFieldName, PrevVoutFieldName, ScriptSigFieldName,
		PrevoutValueSatFieldName, PrevoutAddressFieldName, PrevoutTypeFieldName,
		WitnessFieldName, "_blockID", "_transactionID")

	outputDoc := result.Groups[3].Docs[0]
	assertDocFields(t, outputDoc, map[string]any{
		TxIDFieldName:                  block.Tx[0].TxID,
		constants.BlockNumberFieldName: int64(7),
		VoutIndexFieldName:             int64(0),
		ValueSatFieldName:              int64(5000000000),
		ScriptPubKeyHexFieldName:       "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
		ScriptPubKeyTypeFieldName:      "pubkeyhash",
	})
	assertDocFields(t, outputDoc, nil, ScriptPubKeyAddressFieldName, "_blockID", "_transactionID")
}

// TestConvert_FullBlock covers the regular-transaction branch of every
// builder: the spender doc carries the outpoint, the witness, the
// denormalized prevout and the fee, posited against the coinbase omissions in
// the same document groups.
func TestConvert_FullBlock(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups, 4)
	require.Len(t, result.Groups[1].Docs, 2)
	require.Len(t, result.Groups[2].Docs, 2)
	require.Len(t, result.Groups[3].Docs, 2)

	spender := block.Tx[1]
	txDoc := result.Groups[1].Docs[1]
	assertDocFields(t, txDoc, map[string]any{
		TxIDFieldName:                  spender.TxID,
		"hash":                         spender.Hash,
		constants.BlockHashFieldName:   block.Hash,
		constants.BlockNumberFieldName: int64(7),
		TransactionIndexFieldName:      1,
		"version":                      int64(2),
		"locktime":                     int64(0),
		IsCoinbaseFieldName:            false,
		FeeSatFieldName:                int64(10000),
	})

	spenderInput := result.Groups[2].Docs[1]
	assertDocFields(t, spenderInput, map[string]any{
		TxIDFieldName:                  spender.TxID,
		constants.BlockNumberFieldName: int64(7),
		InputIndexFieldName:            0,
		PrevTxIDFieldName:              fakeBitcoinHash("tx-7-1-prev"),
		PrevVoutFieldName:              int64(0),
		ScriptSigFieldName:             "00",
		SequenceFieldName:              int64(4294967295),
		WitnessFieldName:               []string{fakeBitcoinHash("tx-7-1-witness-0"), fakeBitcoinHash("tx-7-1-witness-1")},
		PrevoutValueSatFieldName:       int64(85705000000),
		PrevoutAddressFieldName:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
		PrevoutTypeFieldName:           "witness_v0_keyhash",
	})
	assertDocFields(t, spenderInput, nil, CoinbaseFieldName)

	spenderOutput := result.Groups[3].Docs[1]
	assertDocFields(t, spenderOutput, map[string]any{
		TxIDFieldName:                  spender.TxID,
		constants.BlockNumberFieldName: int64(7),
		VoutIndexFieldName:             int64(0),
		ValueSatFieldName:              int64(85704990000),
		ScriptPubKeyHexFieldName:       "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
		ScriptPubKeyAddressFieldName:   "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
		ScriptPubKeyTypeFieldName:      "pubkeyhash",
	})

	// The coinbase subtree stays wire-faithful next to the spender: its
	// input carries no reconstructed outpoint even when grouped together.
	assertDocFields(t, result.Groups[2].Docs[0], nil, PrevTxIDFieldName)
}

// TestConvert_GroupFieldContract pins the DocumentGroup contract the generic
// store relies on: every group carries a non-empty BlockNumField, and the
// block group carries the hash and parent-hash field names.
func TestConvert_GroupFieldContract(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	result, err := c.Convert(context.Background(), fakeBlockWithTxs(7, fakeTransaction(7, 1)))
	require.NoError(t, err)
	require.Len(t, result.Groups, 4)

	for i, group := range result.Groups {
		assert.NotEmpty(t, group.BlockNumField,
			"group %d (%s) must populate BlockNumField", i, group.Collection)
	}
	assert.Equal(t, constants.NumberFieldName, result.Groups[0].BlockNumField)
	assert.Equal(t, constants.HashFieldName, result.Groups[0].BlockHashField)
	assert.Equal(t, ParentHashFieldName, result.Groups[0].ParentHashField)
	for i, group := range result.Groups[1:] {
		assert.Empty(t, group.BlockHashField,
			"non-block group %d (%s) must keep BlockHashField empty", i+1, group.Collection)
		assert.Empty(t, group.ParentHashField,
			"non-block group %d (%s) must keep ParentHashField empty", i+1, group.Collection)
		assert.Equal(t, constants.BlockNumberFieldName, group.BlockNumField)
	}
}

func TestConvert_ConverterShipsStamperAndSignatureCollection(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	result, err := c.Convert(context.Background(), fakeBlock(7))
	require.NoError(t, err)
	assert.Equal(t, "Bitcoin__Mainnet__BlockSignature", result.SignatureCollection)
	_, ok := result.LinkStamper.(*bitcoinLinkStamper)
	assert.True(t, ok)
}

// ---------------------------------------------------------------------------
// Batch sizes
// ---------------------------------------------------------------------------

func TestConvert_BatchSizes(t *testing.T) {
	t.Parallel()
	block := fakeBlockWithTxs(7, fakeTransaction(7, 1), fakeTransaction(7, 2))

	tests := []struct {
		name        string
		cfg         *config.Config
		wantBlock   int
		wantTx      int
		wantDerived int
	}{
		{
			name:        "full knobs map per group kind",
			cfg:         testConverterConfig(),
			wantBlock:   1000,
			wantTx:      7,
			wantDerived: 5,
		},
		{
			name: "unset per-collection knobs fall back to the txn default",
			cfg: &config.Config{
				Indexer: config.IndexerConfig{MaxDocsPerTxn: 1234},
			},
			wantBlock:   1234,
			wantTx:      1234,
			wantDerived: 1234,
		},
		{
			name:        "nil config falls back to the package defaults",
			cfg:         nil,
			wantBlock:   1000,
			wantTx:      1000,
			wantDerived: 1000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(tt.cfg)
			result, err := c.Convert(context.Background(), block)
			require.NoError(t, err)
			require.Len(t, result.Groups, 4)
			assert.Equal(t, tt.wantBlock, result.Groups[0].BatchSize, "block group")
			assert.Equal(t, tt.wantTx, result.Groups[1].BatchSize, "transaction group")
			assert.Equal(t, tt.wantDerived, result.Groups[2].BatchSize, "txInput group")
			assert.Equal(t, tt.wantDerived, result.Groups[3].BatchSize, "txOutput group")
		})
	}
}

// ---------------------------------------------------------------------------
// Satoshi values
// ---------------------------------------------------------------------------

// TestConvert_SatoshiValues rides the converter's own docs so the
// shortest-representation table drives real documents end to end.
func TestConvert_SatoshiValues(t *testing.T) {
	t.Parallel()

	type docProbe func(t *testing.T, result chains.ConversionResult)

	tests := []struct {
		name  string
		build func() *Block
		probe docProbe
	}{
		{
			name:  "coinbase subsidy 50 BTC",
			build: func() *Block { return fakeBlock(7) },
			probe: func(t *testing.T, result chains.ConversionResult) {
				v, ok := result.Groups[3].Docs[0][ValueSatFieldName]
				require.True(t, ok)
				assert.Equal(t, int64(5000000000), v)
			},
		},
		{
			name: "fixture coinbase fee 50.2207 spans whole and fraction",
			build: func() *Block {
				block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
				block.Tx[1].Fee = new(50.2207)
				return block
			},
			probe: func(t *testing.T, result chains.ConversionResult) {
				assert.Equal(t, int64(5022070000), result.Groups[1].Docs[1][FeeSatFieldName])
			},
		},
		{
			name: "prevout 857.05 BTC",
			build: func() *Block {
				block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
				return block
			},
			probe: func(t *testing.T, result chains.ConversionResult) {
				assert.Equal(t, int64(85705000000), result.Groups[2].Docs[1][PrevoutValueSatFieldName])
			},
		},
		{
			name: "one-satoshi fee",
			build: func() *Block {
				block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
				block.Tx[1].Fee = new(0.00000001)
				return block
			},
			probe: func(t *testing.T, result chains.ConversionResult) {
				assert.Equal(t, int64(1), result.Groups[1].Docs[1][FeeSatFieldName])
			},
		},
		{
			name: "sub-satoshi fee keeps the satoshi it rounds to",
			build: func() *Block {
				block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
				block.Tx[1].Fee = new(0.000000009)
				return block
			},
			probe: func(t *testing.T, result chains.ConversionResult) {
				assert.Equal(t, int64(1), result.Groups[1].Docs[1][FeeSatFieldName])
			},
		},
		{
			name: "nil fee and nil prevout stay omitted",
			build: func() *Block {
				block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
				block.Tx[1].Fee = nil
				block.Tx[1].Vin[0].Prevout = nil
				return block
			},
			probe: func(t *testing.T, result chains.ConversionResult) {
				_, hasFee := result.Groups[1].Docs[1][FeeSatFieldName]
				assert.False(t, hasFee, "feeSat must be key-omitted when undo data is missing")
				_, hasPrevout := result.Groups[2].Docs[1][PrevoutValueSatFieldName]
				assert.False(t, hasPrevout, "prevoutValueSat must be key-omitted with no prevout")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(testConverterConfig())
			result, err := c.Convert(context.Background(), tt.build())
			require.NoError(t, err)
			tt.probe(t, result)
		})
	}
}

// TestConvert_SatoshiFailuresAreHardErrors pins the wire-anomaly contract: a
// negative or unparsable amount must abort the conversion instead of shaping
// a fake document.
func TestConvert_SatoshiFailuresAreHardErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		build    func(block *Block)
		wantUser string
	}{
		{
			name: "negative fee",
			build: func(block *Block) {
				block.Tx[1].Fee = new(-0.0001)
			},
			wantUser: "fee",
		},
		{
			name: "negative output value",
			build: func(block *Block) {
				block.Tx[1].Vout[0].Value = -1
			},
			wantUser: "output",
		},
		{
			name: "negative prevout value",
			build: func(block *Block) {
				block.Tx[1].Vin[0].Prevout.Value = -1
			},
			wantUser: "prevout value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(testConverterConfig())
			block := fakeBlockWithTxs(7, fakeTransaction(7, 1))
			tt.build(block)
			_, err := c.Convert(context.Background(), block)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantUser)
		})
	}
}

// ---------------------------------------------------------------------------
// DefraDB-backed progress and range queries
// ---------------------------------------------------------------------------

func TestConverterStoredHeights_SpanningQueries(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())
	td := newBitcoinTestDB(t, c)
	ctx := context.Background()

	_, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	assert.Error(t, err, "an empty store has no highest block")
	_, err = c.GetLowestStoredBlockNumber(ctx, td.Node)
	assert.Error(t, err, "an empty store has no lowest block")

	for _, num := range []int64{100, 101, 102} {
		storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(num))
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(102), highest)
}

// TestConverterStoredHeights_AfterPurge mirrors the store's owned purge path:
// after the lowest heights are reclaimed, the lowest query must move on to the
// next stored block instead of reporting corruption.
func TestConverterStoredHeights_AfterPurge(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())
	td := newBitcoinTestDB(t, c)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(num))
	}

	docIDs, err := c.queryCollectionDocIDs(ctx, td.Node, c.collections.Block, constants.NumberFieldName, 100, 101)
	require.NoError(t, err)
	require.Len(t, docIDs, 2)

	col, err := td.Node.DB.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)
	purgeIDs := make([]client.DocID, 0, len(docIDs))
	for _, id := range docIDs {
		parsed, err := client.NewDocIDFromString(id)
		require.NoError(t, err)
		purgeIDs = append(purgeIDs, parsed)
	}
	require.NoError(t, col.PurgeByDocIDs(ctx, purgeIDs, true))

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(102), lowest)

	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(104), highest)
}

// TestConverterStoredHeights_ResidueRows exercises the block-number queries
// against numberless rows: the `_geq: 0` filter must exclude them so a real
// block still answers, while a collection holding only numberless rows
// hard-fails with the corruption sentinel instead of reading as empty.
func TestConverterStoredHeights_ResidueRows(t *testing.T) {
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
				storeBitcoinResidueBlockDoc(ctx, t, td, c, 1)
				for _, num := range []int64{100, 101, 102} {
					storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(num))
				}
				storeBitcoinResidueBlockDoc(ctx, t, td, c, 2)
			},
			wantLow:  100,
			wantHigh: 102,
		},
		{
			name: "only numberless rows yield the corruption sentinel",
			setup: func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter) {
				for i := range 3 {
					storeBitcoinResidueBlockDoc(ctx, t, td, c, int64(i))
				}
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(testConverterConfig())
			td := newBitcoinTestDB(t, c)
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

// TestConverterStoredHeights_LowestWindow pins the window contract end to
// end: the configured limit reaches the lowest-block query, and purge
// residue cannot poison it, because numberless rows are filtered out by the
// query itself before any window applies.
func TestConverterStoredHeights_LowestWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	seed := func(c *Converter, td *testutils.TestDefraDB) {
		for _, seedNum := range []int64{50, 51} {
			storeBitcoinResidueBlockDoc(ctx, t, td, c, seedNum)
		}
		storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(100))
	}

	narrow := NewConverter(testConverterConfig())
	narrow.cfg.Converter.LowestBlockQueryLimit = 2
	td := newBitcoinTestDB(t, narrow)
	seed(narrow, td)
	lowest, err := narrow.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest,
		"numberless residue never enters the window, so a 2-row window still reaches the valid block")

	wide := NewConverter(testConverterConfig())
	wide.cfg.Converter.LowestBlockQueryLimit = 3
	tdWide := newBitcoinTestDB(t, wide)
	seed(wide, tdWide)
	lowest, err = wide.GetLowestStoredBlockNumber(ctx, tdWide.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)
}

// TestQueryBlockNumber_CustomLimits pins the parameterized window: ordering
// must hold for every limit, including limits larger than the stored count.
func TestQueryBlockNumber_CustomLimits(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())
	td := newBitcoinTestDB(t, c)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(num))
	}

	tests := []struct {
		order string
		limit int
		want  int64
	}{
		{"ASC", 1, 100},
		{"ASC", 2, 100},
		{"ASC", 3, 100},
		{"ASC", 20, 100},
		{"DESC", 1, 104},
		{"DESC", 3, 104},
		{"DESC", 7, 104},
		{"DESC", 20, 104},
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
		{"int", map[string]any{constants.NumberFieldName: 102}, 102, true},
		{"nil", map[string]any{constants.NumberFieldName: nil}, 0, false},
		{"missing", map[string]any{}, 0, false},
		{"string", map[string]any{constants.NumberFieldName: "0x64"}, 0, false},
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

// ---------------------------------------------------------------------------
// GetDocIDsByBlockRange
// ---------------------------------------------------------------------------

func TestGetDocIDsByBlockRange_Empty(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())
	td := newBitcoinTestDB(t, c)

	result, err := c.GetDocIDsByBlockRange(context.Background(), td.Node, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestGetDocIDsByBlockRange_Collections pins the collection set: the block,
// the transaction children and the block signature ride the range; the
// snapshot signature must never appear because snapshot history is not
// block-range state.
func TestGetDocIDsByBlockRange_Collections(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())
	td := newBitcoinTestDB(t, c)
	ctx := context.Background()

	storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(100))
	storeBitcoinBlockDoc(ctx, t, td, c, fakeBlock(249))
	storeBitcoinTxChildDoc(ctx, t, td, c.collections.TxInput, 100, fakeBitcoinHash("coinbase-100"))
	storeBitcoinTxChildDoc(ctx, t, td, c.collections.TxOutput, 100, fakeBitcoinHash("coinbase-100"))
	storeBitcoinSignatureDoc(ctx, t, td, c, 100, fakeBitcoinHash("block-100"))
	storeSnapshotSignatureDoc(ctx, t, td, c, fakeBitcoinHash("snapshot-1"))

	// Two blocks whose heights live in two different chunk windows.
	result, err := c.GetDocIDsByBlockRange(ctx, td.Node, 100, 300)
	require.NoError(t, err)
	require.Len(t, result[c.collections.Block], 2)
	assert.NotContains(t, result, c.collections.Transaction)
	require.Len(t, result[c.collections.TxInput], 1)
	require.Len(t, result[c.collections.TxOutput], 1)
	require.Len(t, result[c.collections.BlockSignature], 1)
	assert.NotContains(t, result, c.collections.SnapshotSignature)

	// A range hitting nothing yields an empty map, not per-collection
	// empties.
	missing, err := c.GetDocIDsByBlockRange(ctx, td.Node, 400, 500)
	require.NoError(t, err)
	assert.Empty(t, missing)
}
