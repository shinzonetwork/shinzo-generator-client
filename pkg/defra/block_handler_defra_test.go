package defra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	cid "github.com/ipfs/go-cid"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// deterministicHash generates a valid 66-char hex hash from a seed string.
func deterministicHash(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return "0x" + hex.EncodeToString(h[:])
}

func mockBlock(number string) *types.Block {
	return &types.Block{
		Hash:             deterministicHash("block-" + number),
		Number:           number,
		Timestamp:        "1640995200",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Difficulty:       "1000000",
		TotalDifficulty:  "1000000",
		GasUsed:          "21000",
		GasLimit:         "8000000",
		BaseFeePerGas:    "",
		Nonce:            "0x0",
		Miner:            "0x0000000000000000000000000000000000000001",
		Size:             "1024",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000001",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		ReceiptsRoot:     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		LogsBloom:        "0x00",
		ExtraData:        "0x",
		MixHash:          "0x0000000000000000000000000000000000000000000000000000000000000000",
	}
}

func mockTransaction(hash string, blockNumber string) *types.Transaction {
	return &types.Transaction{
		Hash:              hash,
		BlockHash:         "0x0000000000000000000000000000000000000000000000000000000000000001",
		BlockNumber:       blockNumber,
		From:              "0x0000000000000000000000000000000000000001",
		To:                "0x0000000000000000000000000000000000000002",
		Value:             "1000000000000000000",
		Gas:               "21000",
		GasPrice:          "20000000000",
		Input:             "0x",
		Nonce:             "1",
		TransactionIndex:  0,
		Type:              "0",
		ChainID:           "1",
		V:                 "27",
		R:                 "0x0000000000000000000000000000000000000000000000000000000000000001",
		S:                 "0x0000000000000000000000000000000000000000000000000000000000000001",
		Status:            true,
		CumulativeGasUsed: "21000",
		EffectiveGasPrice: "20000000000",
	}
}

func mockReceipt(txHash string, blockNumber string) *types.TransactionReceipt {
	return &types.TransactionReceipt{
		TransactionHash:   txHash,
		TransactionIndex:  "0",
		BlockHash:         "0x0000000000000000000000000000000000000000000000000000000000000001",
		BlockNumber:       blockNumber,
		From:              "0x0000000000000000000000000000000000000001",
		To:                "0x0000000000000000000000000000000000000002",
		CumulativeGasUsed: "21000",
		GasUsed:           "21000",
		Status:            "0x1",
		Logs: []types.Log{
			{
				Address:          "0x0000000000000000000000000000000000000003",
				Topics:           []string{"0x0000000000000000000000000000000000000000000000000000000000000001"},
				Data:             "0x00",
				BlockNumber:      blockNumber,
				TransactionHash:  txHash,
				TransactionIndex: 0,
				BlockHash:        "0x0000000000000000000000000000000000000000000000000000000000000001",
				LogIndex:         0,
				Removed:          false,
			},
		},
	}
}

// buildGroups is a test helper that converts raw EVM types into DocumentGroups.
func buildGroups(t *testing.T, block *types.Block, txs []*types.Transaction, receipts []*types.TransactionReceipt) chains.ConversionResult {
	t.Helper()
	result, err := evm.NewConverter(nil).Convert(context.Background(), &evm.BlockBundle{
		Block:        block,
		Transactions: txs,
		Receipts:     receipts,
	})
	require.NoError(t, err)
	return result
}

// extractCollection is a test helper that resolves a collection name by role,
// panicking on failure (programmer error).
func extractCollection(collections chains.Collections, role string) string {
	name, err := collections.GetCollection(role)
	if err != nil {
		panic(fmt.Sprintf("programmer error: %v", err))
	}
	return name
}

// ---------------------------------------------------------------------------
// NewBlockHandler with real node
// ---------------------------------------------------------------------------

func TestNewBlockHandler_WithNode(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	require.NotNil(t, handler)
	assert.Equal(t, 1000, handler.maxDocsPerTxn)
}

func TestNewBlockHandler_DefaultMaxDocsWithNode(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 0)
	require.NoError(t, err)
	require.NotNil(t, handler)
	assert.Equal(t, 1000, handler.maxDocsPerTxn, "maxDocsPerTxn should default to 1000 when 0")

	handler2, err := NewBlockHandler(td.Node, -5)
	require.NoError(t, err)
	require.NotNil(t, handler2)
	assert.Equal(t, 1000, handler2.maxDocsPerTxn, "maxDocsPerTxn should default to 1000 when negative")
}

// ---------------------------------------------------------------------------
// GetPort with real node
// ---------------------------------------------------------------------------

func TestGetPort_WithNode(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	port := GetPort(td.Node)
	assert.Equal(t, td.Port, port)
}

// ---------------------------------------------------------------------------
// Store — single transaction mode (small block)
// ---------------------------------------------------------------------------

func TestStore_BlockOnly(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x64") // 100
	result := buildGroups(t, block, nil, nil)
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_WithTransaction(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0xC8") // 200
	tx := mockTransaction("0xabc1000000000000000000000000000000000000000000000000000000000001", "200")
	receipt := mockReceipt("0xabc1000000000000000000000000000000000000000000000000000000000001", "0xC8")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_WithAccessList(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x12C") // 300
	tx := mockTransaction("0xabc2000000000000000000000000000000000000000000000000000000000002", "300")
	tx.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000010",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000001"},
		},
	}
	receipt := mockReceipt("0xabc2000000000000000000000000000000000000000000000000000000000002", "0x12C")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_NilBlock(t *testing.T) {
	t.Parallel()
	_, err := evm.NewConverter(nil).Convert(context.Background(), &evm.BlockBundle{
		Block: nil,
	})
	require.Error(t, err)
}

func TestStore_InvalidBlockNumber(t *testing.T) {
	t.Parallel()
	block := mockBlock("invalid")
	_, err := evm.NewConverter(nil).Convert(context.Background(), &evm.BlockBundle{
		Block: block,
	})
	require.Error(t, err)
}

func TestStore_DuplicateBlock(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x190") // 400
	result := buildGroups(t, block, nil, nil)
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	result2 := buildGroups(t, block, nil, nil)
	_, err = handler.Store(context.Background(), result2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStore_NilDefraNode(t *testing.T) {
	t.Parallel()
	handler := &BlockHandler{maxDocsPerTxn: 1000}
	_, err := handler.Store(context.Background(), chains.ConversionResult{})
	require.Error(t, err)
}

func TestStore_WithDocIDTracker(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cols := chains.NewStubCollections("Ethereum__Mainnet")
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	block := mockBlock("0x1F4") // 500
	tx := mockTransaction("0xabc3000000000000000000000000000000000000000000000000000000000003", "500")
	receipt := mockReceipt("0xabc3000000000000000000000000000000000000000000000000000000000003", "0x1F4")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)

	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(500), tracker.trackedBlocks[0])
	assert.Equal(t, res.BlockID, tracker.trackedResults[0].BlockID)
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeTransaction)], 1)
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeLog)], 1)
}

func TestStore_NilTransaction(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x258") // 600
	txs := []*types.Transaction{nil, mockTransaction("0xabc4000000000000000000000000000000000000000000000000000000000004", "600")}
	receipt := mockReceipt("0xabc4000000000000000000000000000000000000000000000000000000000004", "0x258")

	result := buildGroups(t, block, txs, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_NilReceipt(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x2BC") // 700
	tx := mockTransaction("0xabc5000000000000000000000000000000000000000000000000000000000005", "700")
	receipts := []*types.TransactionReceipt{nil}

	result := buildGroups(t, block, []*types.Transaction{tx}, receipts)
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Store — batched mode (large block exceeding maxDocsPerTxn)
// ---------------------------------------------------------------------------

func TestStore_BatchedMode(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0x320") // 800
	tx1 := mockTransaction("0xabc6000000000000000000000000000000000000000000000000000000000006", "800")
	tx2 := mockTransaction("0xabc7000000000000000000000000000000000000000000000000000000000007", "800")
	receipt1 := mockReceipt("0xabc6000000000000000000000000000000000000000000000000000000000006", "0x320")
	receipt2 := mockReceipt("0xabc7000000000000000000000000000000000000000000000000000000000007", "0x320")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_BatchedMode_WithTracker(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cols := chains.NewStubCollections("Ethereum__Mainnet")
	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	block := mockBlock("0x384") // 900
	tx1 := mockTransaction("0xabc8000000000000000000000000000000000000000000000000000000000008", "900")
	tx2 := mockTransaction("0xabc9000000000000000000000000000000000000000000000000000000000009", "900")
	receipt1 := mockReceipt("0xabc8000000000000000000000000000000000000000000000000000000000008", "0x384")
	receipt2 := mockReceipt("0xabc9000000000000000000000000000000000000000000000000000000000009", "0x384")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)

	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(900), tracker.trackedBlocks[0])
	assert.Equal(t, res.BlockID, tracker.trackedResults[0].BlockID)
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeTransaction)], 2)
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeLog)], 2)
}

func TestStore_BatchedMode_DuplicateBlock(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0x3E8") // 1000
	tx1 := mockTransaction("0xabca000000000000000000000000000000000000000000000000000000000010", "1000")
	receipt1 := mockReceipt("0xabca000000000000000000000000000000000000000000000000000000000010", "0x3E8")

	result := buildGroups(t, block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	result2 := buildGroups(t, block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	_, err = handler.Store(context.Background(), result2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// Multiple transactions with no receipts (no logs)
// ---------------------------------------------------------------------------

func TestStore_MultipleTransactionsNoReceipts(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x44C") // 1100
	tx1 := mockTransaction("0xabcb000000000000000000000000000000000000000000000000000000000011", "1100")
	tx2 := mockTransaction("0xabcc000000000000000000000000000000000000000000000000000000000012", "1100")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, nil)
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Batched mode with access list entries
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_WithAccessList(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0x4B0") // 1200
	tx := mockTransaction("0xabcd000000000000000000000000000000000000000000000000000000000013", "1200")
	tx.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000020",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000002"},
		},
		{
			Address:     "0x0000000000000000000000000000000000000021",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000003"},
		},
	}
	receipt := mockReceipt("0xabcd000000000000000000000000000000000000000000000000000000000013", "0x4B0")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Helper: context with signing identity
// ---------------------------------------------------------------------------

func ctxWithIdentity(t *testing.T) context.Context {
	t.Helper()
	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	return defracontext.WithIdentity(context.Background(), ident)
}

// ---------------------------------------------------------------------------
// Store — block signature path (with identity)
// ---------------------------------------------------------------------------

func TestStore_WithSigningIdentity_BlockOnly(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x514") // 1300
	result := buildGroups(t, block, nil, nil)
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_WithSigningIdentity_FullBlock(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x578") // 1400
	tx := mockTransaction("0xaaa1000000000000000000000000000000000000000000000000000000000001", "1400")
	tx.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000030",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000004"},
		},
	}
	receipt := mockReceipt("0xaaa1000000000000000000000000000000000000000000000000000000000001", "0x578")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_WithSigningIdentity_AndTracker(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x5DC") // 1500
	tx := mockTransaction("0xaaa2000000000000000000000000000000000000000000000000000000000002", "1500")
	receipt := mockReceipt("0xaaa2000000000000000000000000000000000000000000000000000000000002", "0x5DC")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)

	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(1500), tracker.trackedBlocks[0])
	assert.Equal(t, res.BlockID, tracker.trackedResults[0].BlockID)
	assert.NotEmpty(t, tracker.trackedResults[0].BlockSignatureID, "BlockSignatureID should be set when signing identity is present")
}

func TestStore_DuplicateWithIdentity(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x640") // 1600
	result := buildGroups(t, block, nil, nil)
	_, err = handler.Store(ctx, result)
	require.NoError(t, err)

	result2 := buildGroups(t, block, nil, nil)
	_, err = handler.Store(ctx, result2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// Store — batched mode block signature path (with identity)
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_WithSigningIdentity(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x6A4") // 1700
	tx1 := mockTransaction("0xbbb1000000000000000000000000000000000000000000000000000000000001", "1700")
	tx2 := mockTransaction("0xbbb2000000000000000000000000000000000000000000000000000000000002", "1700")
	receipt1 := mockReceipt("0xbbb1000000000000000000000000000000000000000000000000000000000001", "0x6A4")
	receipt2 := mockReceipt("0xbbb2000000000000000000000000000000000000000000000000000000000002", "0x6A4")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

func TestStore_BatchedMode_WithSigningIdentity_AndTracker(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cols := chains.NewStubCollections("Ethereum__Mainnet")
	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x708") // 1800
	tx1 := mockTransaction("0xbbb3000000000000000000000000000000000000000000000000000000000003", "1800")
	tx2 := mockTransaction("0xbbb4000000000000000000000000000000000000000000000000000000000004", "1800")
	tx1.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000040",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000005"},
		},
	}
	receipt1 := mockReceipt("0xbbb3000000000000000000000000000000000000000000000000000000000003", "0x708")
	receipt2 := mockReceipt("0xbbb4000000000000000000000000000000000000000000000000000000000004", "0x708")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)

	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(1800), tracker.trackedBlocks[0])
	assert.Equal(t, res.BlockID, tracker.trackedResults[0].BlockID)
	assert.NotEmpty(t, tracker.trackedResults[0].BlockSignatureID, "BlockSignatureID should be set in batched mode with identity")
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeTransaction)], 2)
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeLog)], 2)
	assert.Len(t, tracker.trackedResults[0].OtherDocIDs[extractCollection(cols, chains.TypeAccessListEntry)], 1)
}

func TestStore_BatchedMode_SignsOverCommittedDocumentCIDs(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cols := chains.NewStubCollections("Ethereum__Mainnet")
	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	var signedCIDs []cid.Cid
	inner := handler.signBatchFn
	handler.signBatchFn = func(ctx context.Context, collector *node.BatchCIDCollector) (*node.BatchSignature, error) {
		signedCIDs = collector.GetCIDs()
		return inner(ctx, collector)
	}

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x76C") // 1900
	tx1 := mockTransaction("0xccc1000000000000000000000000000000000000000000000000000000000001", "1900")
	tx2 := mockTransaction("0xccc2000000000000000000000000000000000000000000000000000000000002", "1900")
	tx1.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000050",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000006"},
		},
	}
	receipt1 := mockReceipt("0xccc1000000000000000000000000000000000000000000000000000000000001", "0x76C")
	receipt2 := mockReceipt("0xccc2000000000000000000000000000000000000000000000000000000000002", "0x76C")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	require.NotEmpty(t, res.BlockID)

	require.Len(t, tracker.trackedResults, 1)
	require.NotEmpty(t, tracker.trackedResults[0].BlockSignatureID, "batched block should be signed")
	require.NotEmpty(t, signedCIDs)

	var docIDs []string
	var collectionNames []string
	for _, role := range []string{chains.TypeBlock, chains.TypeTransaction, chains.TypeLog, chains.TypeAccessListEntry} {
		colName := extractCollection(cols, role)
		field := "blockNumber"
		if role == chains.TypeBlock {
			field = "number"
		}
		ids, err := handler.queryCollectionDocIDs(ctx, colName, field, 1900, 1900)
		require.NoError(t, err)
		docIDs = append(docIDs, ids...)
		collectionNames = append(collectionNames, colName)
	}
	queriedCIDs, err := handler.defaultCollectDocCIDs(ctx, docIDs, collectionNames)
	require.NoError(t, err)
	require.NotEmpty(t, queriedCIDs)
	assert.ElementsMatch(t, sortedCIDStrings(queriedCIDs), sortedCIDStrings(signedCIDs),
		"batched signature must attest exactly the block's document CIDs")
}

func TestStore_BatchedMode_DuplicateWithIdentity(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x76C") // 1900
	tx1 := mockTransaction("0xbbb5000000000000000000000000000000000000000000000000000000000005", "1900")
	receipt1 := mockReceipt("0xbbb5000000000000000000000000000000000000000000000000000000000005", "0x76C")

	result := buildGroups(t, block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	_, err = handler.Store(ctx, result)
	require.NoError(t, err)

	result2 := buildGroups(t, block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	_, err = handler.Store(ctx, result2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// Store — nil transactions in batch
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_NilTransactionsInBatch(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0x7D0") // 2000
	tx1 := mockTransaction("0xccc1000000000000000000000000000000000000000000000000000000000001", "2000")
	receipt1 := mockReceipt("0xccc1000000000000000000000000000000000000000000000000000000000001", "0x7D0")
	txs := []*types.Transaction{nil, tx1, nil}

	result := buildGroups(t, block, txs, []*types.TransactionReceipt{receipt1})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Store — nil receipt handling
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_NilReceipts(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0x834") // 2100
	tx1 := mockTransaction("0xccc2000000000000000000000000000000000000000000000000000000000002", "2100")
	tx2 := mockTransaction("0xccc3000000000000000000000000000000000000000000000000000000000003", "2100")
	receipts := []*types.TransactionReceipt{nil}

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, receipts)
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Store — multiple batches of logs
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_ManyLogs(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0x898") // 2200
	tx := mockTransaction("0xccc4000000000000000000000000000000000000000000000000000000000004", "2200")
	receipt := &types.TransactionReceipt{
		TransactionHash:   "0xccc4000000000000000000000000000000000000000000000000000000000004",
		TransactionIndex:  "0",
		BlockHash:         "0x0000000000000000000000000000000000000000000000000000000000000001",
		BlockNumber:       "0x898",
		From:              "0x0000000000000000000000000000000000000001",
		To:                "0x0000000000000000000000000000000000000002",
		CumulativeGasUsed: "21000",
		GasUsed:           "21000",
		Status:            "0x1",
		Logs: []types.Log{
			{
				Address:          "0x0000000000000000000000000000000000000003",
				Topics:           []string{"0x0000000000000000000000000000000000000000000000000000000000000001"},
				Data:             "0x01",
				BlockNumber:      "0x898",
				TransactionHash:  "0xccc4000000000000000000000000000000000000000000000000000000000004",
				TransactionIndex: 0,
				BlockHash:        "0x0000000000000000000000000000000000000000000000000000000000000001",
				LogIndex:         0,
				Removed:          false,
			},
			{
				Address:          "0x0000000000000000000000000000000000000004",
				Topics:           []string{"0x0000000000000000000000000000000000000000000000000000000000000002"},
				Data:             "0x02",
				BlockNumber:      "0x898",
				TransactionHash:  "0xccc4000000000000000000000000000000000000000000000000000000000004",
				TransactionIndex: 0,
				BlockHash:        "0x0000000000000000000000000000000000000000000000000000000000000001",
				LogIndex:         1,
				Removed:          false,
			},
			{
				Address:          "0x0000000000000000000000000000000000000005",
				Topics:           []string{"0x0000000000000000000000000000000000000000000000000000000000000003"},
				Data:             "0x03",
				BlockNumber:      "0x898",
				TransactionHash:  "0xccc4000000000000000000000000000000000000000000000000000000000004",
				TransactionIndex: 0,
				BlockHash:        "0x0000000000000000000000000000000000000000000000000000000000000001",
				LogIndex:         2,
				Removed:          false,
			},
		},
	}

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// SignExisting
// ---------------------------------------------------------------------------

func TestSignExisting_NilDefraNode(t *testing.T) {
	t.Parallel()
	handler := &BlockHandler{maxDocsPerTxn: 1000}
	_, err := handler.SignExisting(context.Background(), chains.ConversionResult{}, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defraNode is nil")
}

func TestSignExisting_Success(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x8FC") // 2300
	tx := mockTransaction("0xddd1000000000000000000000000000000000000000000000000000000000001", "2300")
	receipt := mockReceipt("0xddd1000000000000000000000000000000000000000000000000000000000001", "0x8FC")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.SignExisting(ctx, result, block.Hash, 2300)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestSignExisting_WithAccessList(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x960") // 2400
	tx := mockTransaction("0xddd2000000000000000000000000000000000000000000000000000000000002", "2400")
	tx.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000050",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000006"},
		},
	}
	receipt := mockReceipt("0xddd2000000000000000000000000000000000000000000000000000000000002", "0x960")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.SignExisting(ctx, result, block.Hash, 2400)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestSignExisting_NilTransactionsAndReceipts(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0x9C4") // 2500

	result := buildGroups(t, block, nil, nil)
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.SignExisting(ctx, result, block.Hash, 2500)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestSignExisting_NilTxInList(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0xA28") // 2600
	tx := mockTransaction("0xddd3000000000000000000000000000000000000000000000000000000000003", "2600")
	receipt := mockReceipt("0xddd3000000000000000000000000000000000000000000000000000000000003", "0xA28")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	result2 := buildGroups(t, block, []*types.Transaction{nil, tx}, []*types.TransactionReceipt{receipt})
	sigDocID, err := handler.SignExisting(ctx, result2, block.Hash, 2600)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestSignExisting_NoIdentity(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0xA8C") // 2700
	result := buildGroups(t, block, nil, nil)
	_, err = handler.Store(context.Background(), result)
	require.NoError(t, err)

	_, err = handler.SignExisting(context.Background(), result, block.Hash, 2700)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identity available for signing")
}

// ---------------------------------------------------------------------------
// Store — transaction with no matching receipt
// ---------------------------------------------------------------------------

func TestStore_TxWithNoMatchingReceipt(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := mockBlock("0xB54") // 2900
	tx := mockTransaction("0xeee1000000000000000000000000000000000000000000000000000000000001", "2900")
	receipt := mockReceipt("0xeee2000000000000000000000000000000000000000000000000000000000099", "0xB54")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID, "block should be created even without matching receipt")
}

// ---------------------------------------------------------------------------
// Store — batched mode transaction with no matching receipt
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_TxWithNoMatchingReceipt(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0xBB8") // 3000
	tx1 := mockTransaction("0xeee3000000000000000000000000000000000000000000000000000000000003", "3000")
	tx2 := mockTransaction("0xeee4000000000000000000000000000000000000000000000000000000000004", "3000")
	receipt1 := mockReceipt("0xeee3000000000000000000000000000000000000000000000000000000000003", "0xBB8")

	result := buildGroups(t, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Store — batched mode many access list entries
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_ManyAccessListEntries(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 2)
	require.NoError(t, err)

	block := mockBlock("0xC1C") // 3100
	tx := mockTransaction("0xeee5000000000000000000000000000000000000000000000000000000000005", "3100")
	tx.AccessList = []types.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000060",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000007"},
		},
		{
			Address:     "0x0000000000000000000000000000000000000061",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000008"},
		},
		{
			Address:     "0x0000000000000000000000000000000000000062",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000009"},
		},
	}
	receipt := mockReceipt("0xeee5000000000000000000000000000000000000000000000000000000000005", "0xC1C")

	result := buildGroups(t, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}

// ---------------------------------------------------------------------------
// Store — batched mode transactions that span multiple batches
// ---------------------------------------------------------------------------

func TestStore_BatchedMode_TransactionsMultipleBatches(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1)
	require.NoError(t, err)

	block := mockBlock("0xCE4") // 3300
	tx1 := mockTransaction("0xfff1000000000000000000000000000000000000000000000000000000000001", "3300")
	tx2 := mockTransaction("0xfff2000000000000000000000000000000000000000000000000000000000002", "3300")
	tx3 := mockTransaction("0xfff3000000000000000000000000000000000000000000000000000000000003", "3300")
	receipt1 := mockReceipt("0xfff1000000000000000000000000000000000000000000000000000000000001", "0xCE4")
	receipt2 := mockReceipt("0xfff2000000000000000000000000000000000000000000000000000000000002", "0xCE4")
	receipt3 := mockReceipt("0xfff3000000000000000000000000000000000000000000000000000000000003", "0xCE4")

	result := buildGroups(t, block,
		[]*types.Transaction{tx1, tx2, tx3},
		[]*types.TransactionReceipt{receipt1, receipt2, receipt3},
	)
	res, err := handler.Store(context.Background(), result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
}
