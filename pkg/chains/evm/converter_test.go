package evm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
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

// ---------------------------------------------------------------------------
// Convert
// ---------------------------------------------------------------------------

func TestConvert_TypeMismatch(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	groups, sigCol, err := c.Convert(context.Background(), "not a bundle", nil)
	assert.Error(t, err)
	assert.Nil(t, groups)
	assert.Empty(t, sigCol)
}

func TestConvert_NilVP(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	// Convert no longer uses vp (link-ID pre-computation removed); nil vp
	// should succeed for a block-only bundle.
	bundle := &BlockBundle{Block: fakeBlock(1)}
	groups, sigCol, err := c.Convert(context.Background(), bundle, nil)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, c.collections.BlockSignature, sigCol)
}

func TestConvert_NilBlockInBundle(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	groups, _, err := c.Convert(context.Background(), &BlockBundle{}, nil)
	assert.Error(t, err)
	assert.Nil(t, groups)
}

func TestConvert_EmptyBlock(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	block := fakeBlock(42)
	bundle := &BlockBundle{Block: block, Transactions: nil, Receipts: nil}

	groups, sigCol, err := c.Convert(context.Background(), bundle, nil)
	require.NoError(t, err)
	require.Len(t, groups, 1) // only block group
	assert.Equal(t, c.collections.Block, groups[0].Collection)
	require.Len(t, groups[0].Docs, 1)
	assert.Equal(t, int64(42), groups[0].Docs[0][constants.NumberFieldValue])
	assert.Equal(t, c.collections.BlockSignature, sigCol)
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
	receipt.Logs = []types.Log{
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
		Transactions: []*types.Transaction{&tx},
		Receipts:     []*types.TransactionReceipt{receipt},
	}

	groups, sigCol, err := c.Convert(context.Background(), bundle, nil)
	require.NoError(t, err)
	assert.Equal(t, c.collections.BlockSignature, sigCol)

	// Block group
	require.GreaterOrEqual(t, len(groups), 2)
	assert.Equal(t, c.collections.Block, groups[0].Collection)
	require.Len(t, groups[0].Docs, 1)
	assert.Equal(t, block.Hash, groups[0].Docs[0]["hash"])

	// Transaction group
	assert.Equal(t, c.collections.Transaction, groups[1].Collection)
	require.Len(t, groups[1].Docs, 1)
	txData := groups[1].Docs[0]
	assert.Equal(t, txHash, txData["hash"])

	// _blockID and _transactionID are NOT set by Convert; they are resolved
	// by BlockHandler.Store (Phase D) after AddDocument assigns persistent docIDs.
	_, hasBlockID := txData["_blockID"]
	assert.False(t, hasBlockID, "_blockID should not be set by Convert")

	// Log group (if present)
	if len(groups) >= 3 {
		assert.Equal(t, c.collections.Log, groups[2].Collection)
		require.Len(t, groups[2].Docs, 1)
		logData := groups[2].Docs[0]
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

	_, sigCol, err := c.Convert(context.Background(), bundle, nil)
	require.NoError(t, err)
	assert.Equal(t, "Optimism__Mainnet__BlockSignature", sigCol)
}

func TestConvert_WithAccessListEntries(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	c := NewConverter(cfg)

	txHash := fakeHash("tx-ale")
	tx := fakeTx(txHash)
	tx.BlockNumber = "0x10"
	tx.AccessList = []types.AccessListEntry{
		{Address: "0x0000000000000000000000000000000000000001", StorageKeys: []string{"0x01"}},
		{Address: "0x0000000000000000000000000000000000000002", StorageKeys: []string{}},
	}
	block := fakeBlockWithTxs(16, tx)
	bundle := &BlockBundle{
		Block:        block,
		Transactions: []*types.Transaction{&tx},
		Receipts:     []*types.TransactionReceipt{fakeReceipt(txHash, 16)},
	}

	groups, _, err := c.Convert(context.Background(), bundle, nil)
	require.NoError(t, err)

	var aleGroup *chains.DocumentGroup
	for i := range groups {
		if groups[i].Collection == c.collections.AccessListEntry {
			aleGroup = &groups[i]
			break
		}
	}
	require.NotNil(t, aleGroup, "should have an ALE group")
	assert.Len(t, aleGroup.Docs, 2)
	assert.Equal(t, "0x0000000000000000000000000000000000000001", aleGroup.Docs[0]["address"])
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
	_, _, err := m.Convert(context.Background(), nil, nil)
	assert.ErrorIs(t, err, testutils.ErrMockConvertFnNotSet)
	assert.Len(t, m.ConvertCalls, 1)
}

func TestMockConverter_ConvertFn(t *testing.T) {
	t.Parallel()
	m := &testutils.MockConverter{}
	expectedGroups := []chains.DocumentGroup{{Collection: "test", Docs: []map[string]any{{"foo": "bar"}}}}
	expectedSigCol := "sigCol"
	m.ConvertFn = func(_ context.Context, _ any, _ chains.CollectionVersionProvider) ([]chains.DocumentGroup, string, error) {
		return expectedGroups, expectedSigCol, nil
	}
	groups, sigCol, err := m.Convert(context.Background(), &BlockBundle{}, nil)
	require.NoError(t, err)
	assert.Equal(t, expectedGroups, groups)
	assert.Equal(t, expectedSigCol, sigCol)
	assert.Len(t, m.ConvertCalls, 1)
}

// ---------------------------------------------------------------------------
// Verify adapter delegates to converter
// ---------------------------------------------------------------------------

func TestAdapter_DelegatesGetSchema(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	client := &fakeRPCClient{}
	a := newAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })

	schema, err := a.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, schema, "Ethereum__Mainnet__Block")
}

func TestAdapter_DelegatesGetCollections(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	client := &fakeRPCClient{}
	a := newAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })

	cols := a.GetCollections()
	assert.Len(t, cols, 6)
}
