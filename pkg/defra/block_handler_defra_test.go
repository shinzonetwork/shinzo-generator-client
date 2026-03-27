package defra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/immutable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
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
		ChainId:           "1",
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

// ---------------------------------------------------------------------------
// NewBlockHandler with real node
// ---------------------------------------------------------------------------

func TestNewBlockHandler_WithNode(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)
	require.NotNil(t, handler)
	assert.Equal(t, 1000, handler.maxDocsPerTxn)
}

func TestNewBlockHandler_DefaultMaxDocsWithNode(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, handler)
	assert.Equal(t, 1000, handler.maxDocsPerTxn, "maxDocsPerTxn should default to 1000 when 0")

	handler2, err := NewBlockHandler(td.Node, -5, nil)
	require.NoError(t, err)
	require.NotNil(t, handler2)
	assert.Equal(t, 1000, handler2.maxDocsPerTxn, "maxDocsPerTxn should default to 1000 when negative")
}

// ---------------------------------------------------------------------------
// GetPort with real node
// ---------------------------------------------------------------------------

func TestGetPort_WithNode(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	port := GetPort(td.Node)
	assert.Equal(t, td.Port, port)
}

// ---------------------------------------------------------------------------
// CreateBlockBatch — single transaction mode (small block)
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_SingleTxn_BlockOnly(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0x64") // 100
	blockID, err := handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_SingleTxn_WithTransaction(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0xC8") // 200
	tx := mockTransaction("0xabc1000000000000000000000000000000000000000000000000000000000001", "200")
	receipt := mockReceipt("0xabc1000000000000000000000000000000000000000000000000000000000001", "0xC8")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_SingleTxn_WithAccessList(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
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

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_NilBlock(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	_, err = handler.CreateBlockBatch(context.Background(), nil, nil, nil)
	require.Error(t, err)
}

func TestCreateBlockBatch_InvalidBlockNumber(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("invalid")
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.Error(t, err)
}

func TestCreateBlockBatch_DuplicateBlock(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0x190") // 400
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.NoError(t, err)

	// Attempting to create the same block again should fail
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateBlockBatch_NilDefraNode(t *testing.T) {
	handler := &BlockHandler{maxDocsPerTxn: 1000}
	block := mockBlock("0x1")
	_, err := handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.Error(t, err)
}

func TestCreateBlockBatch_WithDocIDTracker(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	block := mockBlock("0x1F4") // 500
	tx := mockTransaction("0xabc3000000000000000000000000000000000000000000000000000000000003", "500")
	receipt := mockReceipt("0xabc3000000000000000000000000000000000000000000000000000000000003", "0x1F4")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)

	// Verify tracker was called
	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(500), tracker.trackedBlocks[0])
	assert.Equal(t, blockID, tracker.trackedResults[0].BlockID)
	assert.Len(t, tracker.trackedResults[0].TransactionIDs, 1)
	assert.Len(t, tracker.trackedResults[0].LogIDs, 1)
}

func TestCreateBlockBatch_NilTransaction(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0x258") // 600
	// Include a nil transaction in the list
	txs := []*types.Transaction{nil, mockTransaction("0xabc4000000000000000000000000000000000000000000000000000000000004", "600")}
	receipt := mockReceipt("0xabc4000000000000000000000000000000000000000000000000000000000004", "0x258")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, txs, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_NilReceipt(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0x2BC") // 700
	tx := mockTransaction("0xabc5000000000000000000000000000000000000000000000000000000000005", "700")
	// nil receipt in the list
	receipts := []*types.TransactionReceipt{nil}

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, receipts)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// CreateBlockBatch — batched mode (large block exceeding maxDocsPerTxn)
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	// Set very low maxDocsPerTxn to force batched mode
	handler, err := NewBlockHandler(td.Node, 2, nil)
	require.NoError(t, err)

	block := mockBlock("0x320") // 800
	tx1 := mockTransaction("0xabc6000000000000000000000000000000000000000000000000000000000006", "800")
	tx2 := mockTransaction("0xabc7000000000000000000000000000000000000000000000000000000000007", "800")
	receipt1 := mockReceipt("0xabc6000000000000000000000000000000000000000000000000000000000006", "0x320")
	receipt2 := mockReceipt("0xabc7000000000000000000000000000000000000000000000000000000000007", "0x320")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_BatchedMode_WithTracker(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	block := mockBlock("0x384") // 900
	tx1 := mockTransaction("0xabc8000000000000000000000000000000000000000000000000000000000008", "900")
	tx2 := mockTransaction("0xabc9000000000000000000000000000000000000000000000000000000000009", "900")
	receipt1 := mockReceipt("0xabc8000000000000000000000000000000000000000000000000000000000008", "0x384")
	receipt2 := mockReceipt("0xabc9000000000000000000000000000000000000000000000000000000000009", "0x384")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)

	// Verify tracker was called
	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(900), tracker.trackedBlocks[0])
	assert.Equal(t, blockID, tracker.trackedResults[0].BlockID)
	assert.Len(t, tracker.trackedResults[0].TransactionIDs, 2)
	assert.Len(t, tracker.trackedResults[0].LogIDs, 2)
}

func TestCreateBlockBatch_BatchedMode_DuplicateBlock(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil)
	require.NoError(t, err)

	block := mockBlock("0x3E8") // 1000
	tx1 := mockTransaction("0xabca000000000000000000000000000000000000000000000000000000000010", "1000")
	receipt1 := mockReceipt("0xabca000000000000000000000000000000000000000000000000000000000010", "0x3E8")

	_, err = handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	require.NoError(t, err)

	// Try again — should fail with "already exists"
	_, err = handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// GetHighestBlockNumber
// ---------------------------------------------------------------------------

func TestGetHighestBlockNumber_EmptyDB(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	_, err = handler.GetHighestBlockNumber(context.Background())
	require.Error(t, err, "should fail on empty DB")
}

func TestGetHighestBlockNumber_AfterInserts(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	// Insert block 100
	block1 := mockBlock("0x64") // 100
	_, err = handler.CreateBlockBatch(context.Background(), block1, nil, nil)
	require.NoError(t, err)

	highest, err := handler.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), highest)

	// Insert block 200
	block2 := mockBlock("0xC8") // 200
	_, err = handler.CreateBlockBatch(context.Background(), block2, nil, nil)
	require.NoError(t, err)

	highest, err = handler.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(200), highest)
}

func TestGetHighestBlockNumber_NonSequential(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	// Insert blocks in non-sequential order
	blocks := []string{"0x1F4", "0x64", "0x12C"} // 500, 100, 300
	for _, num := range blocks {
		block := mockBlock(num)
		_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
		require.NoError(t, err)
	}

	highest, err := handler.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(500), highest)
}

// ---------------------------------------------------------------------------
// Multiple transactions with no receipts (no logs)
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_MultipleTransactionsNoReceipts(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0x44C") // 1100
	tx1 := mockTransaction("0xabcb000000000000000000000000000000000000000000000000000000000011", "1100")
	tx2 := mockTransaction("0xabcc000000000000000000000000000000000000000000000000000000000012", "1100")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1, tx2}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// Batched mode with access list entries
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_WithAccessList(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched
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

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// Helper: context with signing identity
// ---------------------------------------------------------------------------

// ctxWithIdentity creates a context with a generated signing identity.
// This enables block signing (buildBlockSignatureDocument path) in tests.
func ctxWithIdentity(t *testing.T) context.Context {
	t.Helper()
	ident, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	ctx := identity.WithContext(
		context.Background(),
		immutable.Some[identity.Identity](ident),
	)
	return ctx
}

// ---------------------------------------------------------------------------
// createBlockSingleTransaction — block signature path (with identity)
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_SingleTxn_WithSigningIdentity_BlockOnly(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x514") // 1300
	blockID, err := handler.CreateBlockBatch(ctx, block, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_SingleTxn_WithSigningIdentity_FullBlock(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
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

	blockID, err := handler.CreateBlockBatch(ctx, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_SingleTxn_WithSigningIdentity_AndTracker(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	tracker := &mockDocIDTracker{}
	handler.SetDocIDTracker(tracker)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x5DC") // 1500
	tx := mockTransaction("0xaaa2000000000000000000000000000000000000000000000000000000000002", "1500")
	receipt := mockReceipt("0xaaa2000000000000000000000000000000000000000000000000000000000002", "0x5DC")

	blockID, err := handler.CreateBlockBatch(ctx, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)

	// Verify tracker was called and captured the BlockSignatureID
	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(1500), tracker.trackedBlocks[0])
	assert.Equal(t, blockID, tracker.trackedResults[0].BlockID)
	assert.NotEmpty(t, tracker.trackedResults[0].BlockSignatureID, "BlockSignatureID should be set when signing identity is present")
}

func TestCreateBlockBatch_SingleTxn_DuplicateWithIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x640") // 1600
	_, err = handler.CreateBlockBatch(ctx, block, nil, nil)
	require.NoError(t, err)

	// Attempting to create the same block again should fail with "already exists"
	_, err = handler.CreateBlockBatch(ctx, block, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// createBlockBatched — block signature path (with identity)
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_WithSigningIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched mode
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x6A4") // 1700
	tx1 := mockTransaction("0xbbb1000000000000000000000000000000000000000000000000000000000001", "1700")
	tx2 := mockTransaction("0xbbb2000000000000000000000000000000000000000000000000000000000002", "1700")
	receipt1 := mockReceipt("0xbbb1000000000000000000000000000000000000000000000000000000000001", "0x6A4")
	receipt2 := mockReceipt("0xbbb2000000000000000000000000000000000000000000000000000000000002", "0x6A4")

	blockID, err := handler.CreateBlockBatch(ctx, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestCreateBlockBatch_BatchedMode_WithSigningIdentity_AndTracker(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched mode
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

	blockID, err := handler.CreateBlockBatch(ctx, block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1, receipt2})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)

	// Verify tracker was called and captured BlockSignatureID
	require.Len(t, tracker.trackedBlocks, 1)
	assert.Equal(t, int64(1800), tracker.trackedBlocks[0])
	assert.Equal(t, blockID, tracker.trackedResults[0].BlockID)
	assert.NotEmpty(t, tracker.trackedResults[0].BlockSignatureID, "BlockSignatureID should be set in batched mode with identity")
	assert.Len(t, tracker.trackedResults[0].TransactionIDs, 2)
	assert.Len(t, tracker.trackedResults[0].LogIDs, 2)
	assert.Len(t, tracker.trackedResults[0].AccessListIDs, 1)
}

func TestCreateBlockBatch_BatchedMode_DuplicateWithIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched mode
	require.NoError(t, err)

	ctx := ctxWithIdentity(t)
	block := mockBlock("0x76C") // 1900
	tx1 := mockTransaction("0xbbb5000000000000000000000000000000000000000000000000000000000005", "1900")
	receipt1 := mockReceipt("0xbbb5000000000000000000000000000000000000000000000000000000000005", "0x76C")

	_, err = handler.CreateBlockBatch(ctx, block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	require.NoError(t, err)

	// Try again -- should fail with "already exists"
	_, err = handler.CreateBlockBatch(ctx, block, []*types.Transaction{tx1}, []*types.TransactionReceipt{receipt1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// createBlockBatched — nil transactions in batch
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_NilTransactionsInBatch(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched
	require.NoError(t, err)

	block := mockBlock("0x7D0") // 2000
	tx1 := mockTransaction("0xccc1000000000000000000000000000000000000000000000000000000000001", "2000")
	receipt1 := mockReceipt("0xccc1000000000000000000000000000000000000000000000000000000000001", "0x7D0")
	// Include nil transactions in the list
	txs := []*types.Transaction{nil, tx1, nil}

	blockID, err := handler.CreateBlockBatch(context.Background(), block, txs, []*types.TransactionReceipt{receipt1})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// createBlockBatched — nil receipt handling
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_NilReceipts(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched
	require.NoError(t, err)

	block := mockBlock("0x834") // 2100
	tx1 := mockTransaction("0xccc2000000000000000000000000000000000000000000000000000000000002", "2100")
	tx2 := mockTransaction("0xccc3000000000000000000000000000000000000000000000000000000000003", "2100")
	// nil receipt in the list
	receipts := []*types.TransactionReceipt{nil}

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1, tx2}, receipts)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// createBlockBatched — multiple batches of logs
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_ManyLogs(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched
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

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// CreateBlockSignatureForExistingBlock
// ---------------------------------------------------------------------------

func TestCreateBlockSignatureForExistingBlock_NilDefraNode(t *testing.T) {
	handler := &BlockHandler{maxDocsPerTxn: 1000}
	_, err := handler.CreateBlockSignatureForExistingBlock(
		context.Background(), 100, "0xhash", mockBlock("0x64"), nil, nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defraNode is nil")
}

func TestCreateBlockSignatureForExistingBlock_Success(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	// Create a block WITHOUT identity (simulates P2P replication where block arrives
	// without a signature). Then create a signature for the existing block.
	block := mockBlock("0x8FC") // 2300
	tx := mockTransaction("0xddd1000000000000000000000000000000000000000000000000000000000001", "2300")
	receipt := mockReceipt("0xddd1000000000000000000000000000000000000000000000000000000000001", "0x8FC")

	_, err = handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)

	// Now create a block signature for the existing block (with identity)
	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.CreateBlockSignatureForExistingBlock(
		ctx, 2300, block.Hash, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestCreateBlockSignatureForExistingBlock_WithAccessList(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
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

	// Create block without identity (no BlockSignature created)
	_, err = handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)

	// Now create a signature for the existing block
	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.CreateBlockSignatureForExistingBlock(
		ctx, 2400, block.Hash, block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestCreateBlockSignatureForExistingBlock_NilTransactionsAndReceipts(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0x9C4") // 2500

	// Create the block first without identity
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.NoError(t, err)

	// Create signature for existing block with no txs/receipts
	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.CreateBlockSignatureForExistingBlock(
		ctx, 2500, block.Hash, block, nil, nil,
	)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestCreateBlockSignatureForExistingBlock_NilTxInList(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0xA28") // 2600
	tx := mockTransaction("0xddd3000000000000000000000000000000000000000000000000000000000003", "2600")
	receipt := mockReceipt("0xddd3000000000000000000000000000000000000000000000000000000000003", "0xA28")

	// Create block without identity
	_, err = handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)

	// Pass nil transactions in the list (should be skipped gracefully)
	ctx := ctxWithIdentity(t)
	sigDocID, err := handler.CreateBlockSignatureForExistingBlock(
		ctx, 2600, block.Hash, block, []*types.Transaction{nil, tx}, []*types.TransactionReceipt{receipt},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
}

func TestCreateBlockSignatureForExistingBlock_NoIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	// Create a block first without identity
	block := mockBlock("0xA8C") // 2700
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.NoError(t, err)

	// Try to create block signature without identity context
	// SignBlock will return nil (no identity), causing "signing returned nil (no identity?)" error
	_, err = handler.CreateBlockSignatureForExistingBlock(
		context.Background(), 2700, block.Hash, block, nil, nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing returned nil")
}

// ---------------------------------------------------------------------------
// GetHighestBlockNumber — additional coverage
// ---------------------------------------------------------------------------

func TestGetHighestBlockNumber_SingleBlock(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0xAF0") // 2800
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.NoError(t, err)

	highest, err := handler.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2800), highest)
}

func TestGetHighestBlockNumber_LargeBlockNumber(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	// Use a large block number to ensure int64 handling works
	block := mockBlock("0xF4240") // 1000000
	_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
	require.NoError(t, err)

	highest, err := handler.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1000000), highest)
}

// ---------------------------------------------------------------------------
// createBlockSingleTransaction — transaction with no matching receipt
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_SingleTxn_TxWithNoMatchingReceipt(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	block := mockBlock("0xB54") // 2900
	tx := mockTransaction("0xeee1000000000000000000000000000000000000000000000000000000000001", "2900")
	// Receipt hash doesn't match the transaction hash
	receipt := mockReceipt("0xeee2000000000000000000000000000000000000000000000000000000000099", "0xB54")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID, "block should be created even without matching receipt")
}

// ---------------------------------------------------------------------------
// createBlockBatched — transaction with no matching receipt
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_TxWithNoMatchingReceipt(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched
	require.NoError(t, err)

	block := mockBlock("0xBB8") // 3000
	tx1 := mockTransaction("0xeee3000000000000000000000000000000000000000000000000000000000003", "3000")
	tx2 := mockTransaction("0xeee4000000000000000000000000000000000000000000000000000000000004", "3000")
	// Receipt for tx1 only, tx2 has no matching receipt
	receipt1 := mockReceipt("0xeee3000000000000000000000000000000000000000000000000000000000003", "0xBB8")

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx1, tx2}, []*types.TransactionReceipt{receipt1})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// createBlockBatched — many access list entries across batches
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_ManyAccessListEntries(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 2, nil) // force batched
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

	blockID, err := handler.CreateBlockBatch(context.Background(), block, []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// createBlockBatched — transactions that span multiple batches
// ---------------------------------------------------------------------------

func TestCreateBlockBatch_BatchedMode_TransactionsMultipleBatches(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1, nil) // force batched with batchSize=1
	require.NoError(t, err)

	block := mockBlock("0xCE4") // 3300
	tx1 := mockTransaction("0xfff1000000000000000000000000000000000000000000000000000000000001", "3300")
	tx2 := mockTransaction("0xfff2000000000000000000000000000000000000000000000000000000000002", "3300")
	tx3 := mockTransaction("0xfff3000000000000000000000000000000000000000000000000000000000003", "3300")
	receipt1 := mockReceipt("0xfff1000000000000000000000000000000000000000000000000000000000001", "0xCE4")
	receipt2 := mockReceipt("0xfff2000000000000000000000000000000000000000000000000000000000002", "0xCE4")
	receipt3 := mockReceipt("0xfff3000000000000000000000000000000000000000000000000000000000003", "0xCE4")

	blockID, err := handler.CreateBlockBatch(context.Background(), block,
		[]*types.Transaction{tx1, tx2, tx3},
		[]*types.TransactionReceipt{receipt1, receipt2, receipt3},
	)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// ---------------------------------------------------------------------------
// GetHighestBlockNumber — multiple blocks to ensure ORDER DESC works
// ---------------------------------------------------------------------------

func TestGetHighestBlockNumber_ThreeBlocksDescOrder(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	handler, err := NewBlockHandler(td.Node, 1000, nil)
	require.NoError(t, err)

	blocks := []struct {
		hex    string
		number int64
	}{
		{"0xD48", 3400},
		{"0xDAC", 3500},
		{"0xE10", 3600},
	}

	for _, b := range blocks {
		block := mockBlock(b.hex)
		_, err = handler.CreateBlockBatch(context.Background(), block, nil, nil)
		require.NoError(t, err)
	}

	highest, err := handler.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3600), highest)
}
