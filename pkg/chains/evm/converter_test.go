package evm

import (
	"context"
	"fmt"
	"reflect"
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
// Construction
// ---------------------------------------------------------------------------

func TestNewConverter_NilConfig(t *testing.T) {
	t.Parallel()
	c := NewConverter(nil)
	assert.Equal(t, "Ethereum__Mainnet", c.collections.Prefix())
}

func TestNewConverter_DefaultPrefix(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)
	assert.Equal(t, "Ethereum__Mainnet", c.collections.Prefix())
}

func TestNewConverter_CustomPrefix(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{Name: "Arbitrum", Network: "One"}
	c := NewConverter(cfg)
	assert.Equal(t, "Arbitrum__One", c.collections.Prefix())
}

// ---------------------------------------------------------------------------
// GetSchema / GetCollections / Collections
// ---------------------------------------------------------------------------

func TestConverter_GetSchema_Delegates(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "Ethereum__Mainnet__Block")
	assert.Contains(t, sdl, "Ethereum__Mainnet__Transaction")
}

func TestConverter_GetSchema_CustomPrefix(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{Name: "Arbitrum", Network: "One"}
	c := NewConverter(cfg)

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "Arbitrum__One__Block")
}

func TestConverter_GetCollections(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	cols := c.GetCollections()
	assert.Len(t, cols, 6)
	assert.Equal(t, "Ethereum__Mainnet__Block", cols[0])
	assert.Equal(t, "Ethereum__Mainnet__BlockSignature", cols[1])
	assert.Equal(t, "Ethereum__Mainnet__SnapshotSignature", cols[2])
	assert.Equal(t, "Ethereum__Mainnet__Transaction", cols[3])
	assert.Equal(t, "Ethereum__Mainnet__AccessListEntry", cols[4])
	assert.Equal(t, "Ethereum__Mainnet__Log", cols[5])
}

func TestConverter_Collections(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	cols := c.Collections()
	require.NotNil(t, cols)
	assert.Equal(t, "Ethereum__Mainnet", cols.Prefix())

	name, err := cols.GetCollection(chains.TypeBlock)
	require.NoError(t, err)
	assert.Equal(t, "Ethereum__Mainnet__Block", name)
}

func TestConverter_SignatureCollection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		chain   config.ChainConfig
		wantCol string
	}{
		{
			name:    "DefaultPrefix",
			chain:   config.ChainConfig{Name: "Ethereum", Network: "Mainnet"},
			wantCol: "Ethereum__Mainnet__BlockSignature",
		},
		{
			name:    "CustomPrefix",
			chain:   config.ChainConfig{Name: "Optimism", Network: "Mainnet"},
			wantCol: "Optimism__Mainnet__BlockSignature",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			cfg.Chain = tc.chain
			c := NewConverter(cfg)
			assert.Equal(t, tc.wantCol, c.SignatureCollection())
		})
	}
}

// ---------------------------------------------------------------------------
// Convert
// ---------------------------------------------------------------------------

func TestConvert_TypeMismatch(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	result, err := c.Convert(context.Background(), "not a bundle")
	assert.Error(t, err)
	assert.Nil(t, result.Groups)
	assert.Empty(t, result.SignatureCollection)
}

func TestConvert_NilVP(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	// Convert no longer uses vp (link-ID pre-computation removed); nil vp
	// should succeed for a block-only bundle.
	bundle := &BlockBundle{Block: fakeBlock(1)}
	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1)
	assert.Equal(t, c.collections.BlockSignature, result.SignatureCollection)
}

func TestConvert_NilBlockInBundle(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	result, err := c.Convert(context.Background(), &BlockBundle{})
	assert.Error(t, err)
	assert.Nil(t, result.Groups)
}

func TestConvert_EmptyBlock(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	block := fakeBlock(42)
	bundle := &BlockBundle{Block: block, Transactions: nil, Receipts: nil}

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)
	require.Len(t, result.Groups, 1) // only block group
	assert.Equal(t, c.collections.Block, result.Groups[0].Collection)
	require.Len(t, result.Groups[0].Docs, 1)
	assert.Equal(t, int64(42), result.Groups[0].Docs[0][constants.NumberFieldName])
	assert.Equal(t, c.collections.BlockSignature, result.SignatureCollection)
	assert.Equal(t, constants.HashFieldName, result.Groups[0].BlockHashField,
		"block group should have BlockHashField set to constants.HashFieldName")
}

func TestConvert_BlockBundleToDocumentGroups(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	txHash := fakeHash("tx-1")
	tx := fakeTx(txHash)
	tx.BlockNumber = "0x2a"
	block := fakeBlockWithTxs(42, tx)
	receipt := fakeReceipt(txHash, 42)
	receipt.Logs = []Log{
		{
			Address:         "0x0000000000000000000000000000000000000003",
			Data:            "0x",
			BlockNumber:     "0x2a",
			BlockHash:       block.Hash,
			TransactionHash: txHash,
			LogIndex:        0,
		},
	}
	bundle := &BlockBundle{
		Block:        block,
		Transactions: []*Transaction{&tx},
		Receipts:     []*TransactionReceipt{receipt},
	}

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)
	assert.Equal(t, c.collections.BlockSignature, result.SignatureCollection)

	// Block group
	require.GreaterOrEqual(t, len(result.Groups), 2)
	assert.Equal(t, c.collections.Block, result.Groups[0].Collection)
	require.Len(t, result.Groups[0].Docs, 1)
	assert.Equal(t, block.Hash, result.Groups[0].Docs[0][constants.HashFieldName])

	// Transaction group
	assert.Equal(t, c.collections.Transaction, result.Groups[1].Collection)
	require.Len(t, result.Groups[1].Docs, 1)
	txData := result.Groups[1].Docs[0]
	assert.Equal(t, txHash, txData[constants.HashFieldName])
	assert.Empty(t, result.Groups[1].BlockHashField,
		"non-block groups should have empty BlockHashField")

	// _blockID and _transactionID are NOT set by Convert; they are resolved
	// by BlockHandler.Store (Phase D) after AddDocument assigns persistent docIDs.
	_, hasBlockID := txData["_blockID"]
	assert.False(t, hasBlockID, "_blockID should not be set by Convert")

	// Log group (if present)
	if len(result.Groups) >= 3 {
		assert.Equal(t, c.collections.Log, result.Groups[2].Collection)
		require.Len(t, result.Groups[2].Docs, 1)
		logData := result.Groups[2].Docs[0]
		_, hasLogBlockID := logData["_blockID"]
		assert.False(t, hasLogBlockID, "_blockID should not be set by Convert")
		_, hasTxID := logData["_transactionID"]
		assert.False(t, hasTxID, "_transactionID should not be set by Convert")
	}
}

func TestConvert_SignatureCollectionName(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{Name: "Optimism", Network: "Mainnet"}
	c := NewConverter(cfg)

	block := fakeBlock(1)
	bundle := &BlockBundle{Block: block}

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)
	assert.Equal(t, "Optimism__Mainnet__BlockSignature", result.SignatureCollection)
}

// TestConvert_GroupFieldContract pins the DocumentGroup contract the generic
// BlockHandler relies on: every group carries a non-empty BlockNumField, and
// the block group (Groups[0]) carries a non-empty BlockHashField and a
// non-empty ParentHashField. Non-block groups keep BlockHashField and
// ParentHashField empty (see chains.DocumentGroup).
func TestConvert_GroupFieldContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		bundle        *BlockBundle
		wantMinGroups int
	}{
		{
			name:          "block only",
			bundle:        &BlockBundle{Block: fakeBlock(7)},
			wantMinGroups: 1,
		},
		{
			name: "block with tx, receipt, and log",
			bundle: func() *BlockBundle {
				txHash := fakeHash("tx-cf")
				tx := fakeTx(txHash)
				tx.BlockNumber = "0x7"
				block := fakeBlockWithTxs(7, tx)
				receipt := fakeReceipt(txHash, 7)
				receipt.Logs = []Log{
					{
						Address:         "0x0000000000000000000000000000000000000003",
						Data:            "0x",
						BlockNumber:     "0x7",
						BlockHash:       block.Hash,
						TransactionHash: txHash,
						LogIndex:        0,
					},
				}
				return &BlockBundle{
					Block:        block,
					Transactions: []*Transaction{&tx},
					Receipts:     []*TransactionReceipt{receipt},
				}
			}(),
			wantMinGroups: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(testConfig())

			result, err := c.Convert(context.Background(), tc.bundle)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(result.Groups), tc.wantMinGroups)

			for i, g := range result.Groups {
				assert.NotEmpty(t, g.BlockNumField,
					"group %d (%s) must populate BlockNumField", i, g.Collection)
			}
			assert.Equal(t, ParentHashFieldName, result.Groups[0].ParentHashField,
				"block group must carry ParentHashField set to constants.ParentHashFieldName")
			for i, g := range result.Groups[1:] {
				assert.Empty(t, g.ParentHashField,
					"non-block group %d (%s) must keep ParentHashField empty", i+1, g.Collection)
			}
			assert.NotEmpty(t, result.Groups[0].BlockHashField,
				"block group must populate BlockHashField")
		})
	}
}

func TestConvert_WithAccessListEntries(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	txHash := fakeHash("tx-ale")
	tx := fakeTx(txHash)
	tx.BlockNumber = "0x10"
	tx.AccessList = []AccessListEntry{
		{Address: "0x0000000000000000000000000000000000000001", StorageKeys: []string{"0x01"}},
		{Address: "0x0000000000000000000000000000000000000002", StorageKeys: []string{}},
	}
	block := fakeBlockWithTxs(16, tx)
	bundle := &BlockBundle{
		Block:        block,
		Transactions: []*Transaction{&tx},
		Receipts:     []*TransactionReceipt{fakeReceipt(txHash, 16)},
	}

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)

	var aleGroup *chains.DocumentGroup
	for i := range result.Groups {
		if result.Groups[i].Collection == c.collections.AccessListEntry {
			aleGroup = &result.Groups[i]
			break
		}
	}
	require.NotNil(t, aleGroup, "should have an ALE group")
	assert.Len(t, aleGroup.Docs, 2)
	assert.Equal(t, "0x0000000000000000000000000000000000000001", aleGroup.Docs[0][AddressFieldName])
}

func TestConvert_DuplicateAccessListEntries(t *testing.T) {
	t.Parallel()

	type aleCase struct {
		name       string
		txs        []Transaction
		wantDocs   int
		wantRefs   []string
		wantFields []map[string]any // per-doc expected content, index-aligned
	}

	aleStorageKey1 := "0x0000000000000000000000000000000000000000000000000000000000000001"
	aleStorageKey2 := "0x0000000000000000000000000000000000000000000000000000000000000002"
	aleAddr := "0x0000000000000000000000000000000000000001"

	cases := []aleCase{
		{
			name: "repeated identical entry in one tx yields one doc",
			txs: []Transaction{
				{
					Hash: fakeHash("tx-ale-dup"),
					AccessList: []AccessListEntry{
						{Address: aleAddr, StorageKeys: []string{aleStorageKey1}},
						{Address: aleAddr, StorageKeys: []string{aleStorageKey1}},
						{Address: aleAddr, StorageKeys: []string{aleStorageKey1}},
					},
				},
			},
			wantDocs: 1,
			wantRefs: []string{fakeHash("tx-ale-dup")},
			wantFields: []map[string]any{
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey1}},
			},
		},
		{
			name: "same address and keys in two different txs yields two docs",
			txs: []Transaction{
				{
					Hash:       fakeHash("tx-ale-cross-1"),
					AccessList: []AccessListEntry{{Address: aleAddr, StorageKeys: []string{aleStorageKey1}}},
				},
				{
					Hash:       fakeHash("tx-ale-cross-2"),
					AccessList: []AccessListEntry{{Address: aleAddr, StorageKeys: []string{aleStorageKey1}}},
				},
			},
			wantDocs: 2,
			wantRefs: []string{fakeHash("tx-ale-cross-1"), fakeHash("tx-ale-cross-2")},
			wantFields: []map[string]any{
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey1}},
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey1}},
			},
		},
		{
			name: "same address with same keys in different order yields two docs",
			txs: []Transaction{
				{
					Hash: fakeHash("tx-ale-order"),
					AccessList: []AccessListEntry{
						{Address: aleAddr, StorageKeys: []string{aleStorageKey1, aleStorageKey2}},
						{Address: aleAddr, StorageKeys: []string{aleStorageKey2, aleStorageKey1}},
					},
				},
			},
			wantDocs: 2,
			wantRefs: []string{fakeHash("tx-ale-order"), fakeHash("tx-ale-order")},
			wantFields: []map[string]any{
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey1, aleStorageKey2}},
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey2, aleStorageKey1}},
			},
		},
		{
			name: "same address with different keys yields two docs",
			txs: []Transaction{
				{
					Hash: fakeHash("tx-ale-distinct"),
					AccessList: []AccessListEntry{
						{Address: aleAddr, StorageKeys: []string{aleStorageKey1}},
						{Address: aleAddr, StorageKeys: []string{aleStorageKey2}},
					},
				},
			},
			wantDocs: 2,
			wantRefs: []string{fakeHash("tx-ale-distinct"), fakeHash("tx-ale-distinct")},
			wantFields: []map[string]any{
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey1}},
				{AddressFieldName: aleAddr, StorageKeysFieldName: []string{aleStorageKey2}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := NewConverter(testConfig())

			blockNum := int64(16)
			block := fakeBlockWithTxs(blockNum, tc.txs...)
			txs := make([]*Transaction, len(tc.txs))
			for i := range tc.txs {
				txs[i] = &tc.txs[i]
			}
			bundle := &BlockBundle{
				Block:        block,
				Transactions: txs,
				Receipts:     []*TransactionReceipt{fakeReceipt(tc.txs[0].Hash, blockNum)},
			}

			aleGroup, parentRefs := convertForALE(t, c, bundle)

			require.Len(t, aleGroup.Docs, tc.wantDocs)
			require.Len(t, parentRefs, tc.wantDocs, "docs and parentRefs must stay index-aligned")
			assert.Equal(t, tc.wantRefs, parentRefs)
			for i, want := range tc.wantFields {
				for field, wantVal := range want {
					assert.Equal(t, wantVal, aleGroup.Docs[i][field],
						"doc %d field %q must keep the first occurrence's content", i, field)
				}
			}
		})
	}
}

// TestConvert_ALEGroupHasNoContentIdenticalDocsPerTx is the converter-side
// contract: within one ALE group, no two documents destined for the same
// parent transaction may be content-identical. Pre-stamp doc maps carry no tx
// hash (the _transactionID link is stamped at write time), so identity is
// scoped by parent ref: two same-content entries kept for two different txs
// resolve to different links and are correctly NOT dropped.
func TestConvert_ALEGroupHasNoContentIdenticalDocsPerTx(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	aleAddr := "0x0000000000000000000000000000000000000001"
	key := "0x0000000000000000000000000000000000000000000000000000000000000009"

	tx1Hash := fakeHash("tx-ale-contract-1")
	tx1 := Transaction{Hash: tx1Hash, AccessList: []AccessListEntry{
		{Address: aleAddr, StorageKeys: []string{key}},
		{Address: aleAddr, StorageKeys: []string{key}},
	}}
	tx2Hash := fakeHash("tx-ale-contract-2")
	// Same address and keys as tx1's entries — identical docs pre-stamp, but
	// different _transactionID at write time, so both must survive.
	tx2 := Transaction{Hash: tx2Hash, AccessList: []AccessListEntry{
		{Address: aleAddr, StorageKeys: []string{key}},
	}}
	blockNum := int64(16)
	bundle := &BlockBundle{
		Block:        fakeBlockWithTxs(blockNum, tx1, tx2),
		Transactions: []*Transaction{&tx1, &tx2},
		Receipts:     []*TransactionReceipt{fakeReceipt(tx1Hash, blockNum)},
	}

	aleGroup, parentRefs := convertForALE(t, c, bundle)
	require.Len(t, parentRefs, len(aleGroup.Docs))

	for i := range aleGroup.Docs {
		for j := i + 1; j < len(aleGroup.Docs); j++ {
			if parentRefs[i] != parentRefs[j] {
				continue
			}
			assert.False(t, reflect.DeepEqual(aleGroup.Docs[i], aleGroup.Docs[j]),
				"docs %d and %d for the same parent tx must not be content-identical", i, j)
		}
	}
}

// ---------------------------------------------------------------------------
// Progress queries (require real DefraDB)
// ---------------------------------------------------------------------------

func TestGetHighestStoredBlockNumber_NoBlocks(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	td := testutils.SetupTestDefraDB(t)
	_, err := c.GetHighestStoredBlockNumber(context.Background(), td.Node)
	assert.Error(t, err)
}

func TestGetLowestStoredBlockNumber_NoBlocks(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	td := testutils.SetupTestDefraDB(t)
	_, err := c.GetLowestStoredBlockNumber(context.Background(), td.Node)
	assert.Error(t, err)
}

func TestGetDocIDsByBlockRange_Empty(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	td := testutils.SetupTestDefraDB(t)
	result, err := c.GetDocIDsByBlockRange(context.Background(), td.Node, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestGetStoredBlockNumbers_WithBlocks(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)
	td := testutils.SetupTestDefraDB(t)
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
// 2026-09-02 incident: a PurgeByDocIDs residue left a dangling row behind, so
// the ASC lowest-block query kept returning a row whose number field was
// unparseable ("block exists but has invalid or unparseable number field").
// After purging the lowest blocks, the query must return the next stored
// block without error.
func TestGetLowestStoredBlockNumber_AfterPurge(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)
	td := testutils.SetupTestDefraDB(t)
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
// default limit of 1 — structurally fixing the failure that previously
// required a widened lowest_block_query_limit window — and a collection
// holding only numberless rows must surface the corruption sentinel rather
// than "not found", so the pruner hard-fails with ErrNoValidBlocks instead
// of silently skipping pruning.
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
			cfg := testConfig()
			c := NewConverter(cfg)
			td := testutils.SetupTestDefraDB(t)
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

// TestQueryBlockNumber_CustomLimits pins the parameterized query limit: every
// supported limit must build valid GraphQL, honor the ordering, and stay safe
// when the limit exceeds the number of stored blocks.
func TestQueryBlockNumber_CustomLimits(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)
	td := testutils.SetupTestDefraDB(t)
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

	configured := testConfig()
	configured.Converter.LowestBlockQueryLimit = 7

	assert.Equal(t, config.DefaultLowestBlockQueryLimit,
		NewConverter(nil).lowestBlockQueryLimit(), "nil config falls back to default")
	assert.Equal(t, config.DefaultLowestBlockQueryLimit,
		NewConverter(testConfig()).lowestBlockQueryLimit(), "unset config falls back to default")
	assert.Equal(t, 7,
		NewConverter(configured).lowestBlockQueryLimit(), "configured value is honored")
}

// TestGetLowestStoredBlockNumber_ConfigWindow proves the configured window is
// plumbed from config into the lowest-block query end to end.
func TestGetLowestStoredBlockNumber_ConfigWindow(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Converter.LowestBlockQueryLimit = 2
	c := NewConverter(cfg)
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeTestBlockDoc(ctx, t, td, c, num)
	}

	got, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), got)
}

// ---------------------------------------------------------------------------
// MockConverter
// ---------------------------------------------------------------------------

func TestMockConverter_SatisfiesInterface(t *testing.T) {
	t.Parallel()
	var _ chains.Converter = (*testutils.MockConverter)(nil)

	m := &testutils.MockConverter{}
	assert.Equal(t, 0, m.GetSchemaCalls)

	_, err := m.GetSchema()
	assert.NoError(t, err)
	assert.Equal(t, 1, m.GetSchemaCalls)
}

func TestMockConverter_ConvertNotSet(t *testing.T) {
	t.Parallel()
	m := &testutils.MockConverter{}
	_, err := m.Convert(context.Background(), nil)
	assert.ErrorIs(t, err, testutils.ErrMockConvertFnNotSet)
	assert.Len(t, m.ConvertCalls, 1)
}

func TestMockConverter_ConvertFn(t *testing.T) {
	t.Parallel()
	m := &testutils.MockConverter{}
	expectedGroups := []chains.DocumentGroup{{Collection: "test", Docs: []map[string]any{{"foo": "bar"}}}}
	expectedSigCol := "sigCol"
	m.ConvertFn = func(_ context.Context, _ any) (chains.ConversionResult, error) {
		return chains.ConversionResult{
			Groups:              expectedGroups,
			SignatureCollection: expectedSigCol,
		}, nil
	}
	result, err := m.Convert(context.Background(), &BlockBundle{})
	require.NoError(t, err)
	assert.Equal(t, expectedGroups, result.Groups)
	assert.Equal(t, expectedSigCol, result.SignatureCollection)
	assert.Len(t, m.ConvertCalls, 1)
}

func TestMockConverter_SignatureCollection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		setupFn func(m *testutils.MockConverter)
		wantCol string
	}{
		{
			name:    "Default",
			setupFn: func(_ *testutils.MockConverter) {},
			wantCol: "Ethereum__Mainnet__BlockSignature",
		},
		{
			name: "CustomFn",
			setupFn: func(m *testutils.MockConverter) {
				m.SignatureCollectionFn = func() string { return "Custom__Chain__BlockSignature" }
			},
			wantCol: "Custom__Chain__BlockSignature",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &testutils.MockConverter{}
			tc.setupFn(m)
			result := m.SignatureCollection()
			assert.Equal(t, tc.wantCol, result)
			assert.Equal(t, 1, m.SignatureCollectionCalls)
		})
	}
}
