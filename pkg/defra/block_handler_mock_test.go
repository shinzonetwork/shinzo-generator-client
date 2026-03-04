package defra

import (
	"context"
	"fmt"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/mocks"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

// ---------------------------------------------------------------------------
// mockBlockDB — implements blockDB interface for unit tests
// ---------------------------------------------------------------------------

type mockBlockDB struct {
	newTxnFn    func() (client.Txn, error)
	initCtxFn   func(ctx context.Context, txn client.Txn) context.Context
	execReqFn   func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult
}

func (m *mockBlockDB) NewBlindWriteTxn() (client.Txn, error) {
	return m.newTxnFn()
}

func (m *mockBlockDB) InitContext(ctx context.Context, txn client.Txn) context.Context {
	if m.initCtxFn != nil {
		return m.initCtxFn(ctx, txn)
	}
	return ctx
}

func (m *mockBlockDB) ExecRequest(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	return m.execReqFn(ctx, request, opts...)
}

// ---------------------------------------------------------------------------
// errDocIDTracker — tracker that always returns an error
// ---------------------------------------------------------------------------

type errDocIDTracker struct{}

func (e *errDocIDTracker) TrackBlock(_ context.Context, _ int64, _ *BlockCreationResult) error {
	return fmt.Errorf("tracker error")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newMockHandler creates a handler with a mock DB and default signing stubs.
func newMockHandler(t *testing.T, db *mockBlockDB) *BlockHandler {
	t.Helper()
	return &BlockHandler{
		db:            db,
		maxDocsPerTxn: 1000,
		signBlockFn: func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
			return nil, nil // no identity → nil sig
		},
		verifyBlockSigFn: func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
			return true, nil
		},
		collectDocCIDsFn: func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
			return nil, nil
		},
		maxCIDRetries:  1,
		retryBackoffFn: func(int) time.Duration { return 0 },
	}
}

// mockTxnWithCollections creates a mock txn where GetCollectionByName returns
// real-looking mock collections that allow document creation.
func mockTxnWithCollections(t *testing.T, td *testDefraCollections) *mocks.Txn {
	t.Helper()
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).
		Return(td.block, nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).
		Return(td.tx, nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).
		Return(td.log, nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).
		Return(td.ale, nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).
		Return(td.blockSig, nil).Maybe()
	return txn
}

// testDefraCollections bundles mock collections with empty versions for
// triggering buildDocument errors, or with real versions for success paths.
type testDefraCollections struct {
	block    client.Collection
	tx       client.Collection
	log      client.Collection
	ale      client.Collection
	blockSig client.Collection
}

// emptyVersionCollections returns mock collections with empty versions.
// NewDocFromMap will fail because Set returns "field not found" for any field.
func emptyVersionCollections(t *testing.T) *testDefraCollections {
	t.Helper()
	mk := func() *mocks.Collection {
		c := mocks.NewCollection(t)
		c.EXPECT().Version().Return(client.CollectionVersion{}).Maybe()
		c.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		c.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		return c
	}
	return &testDefraCollections{
		block:    mk(),
		tx:       mk(),
		log:      mk(),
		ale:      mk(),
		blockSig: mk(),
	}
}

func testBlock() *types.Block {
	return mockBlock("0x64")
}

func testTx() *types.Transaction {
	return mockTransaction("0xabc1000000000000000000000000000000000000000000000000000000000001", "100")
}

func testReceipt() *types.TransactionReceipt {
	return mockReceipt("0xabc1000000000000000000000000000000000000000000000000000000000001", "0x64")
}

// =========================================================================
// createBlockSingleTransaction error paths
// =========================================================================

func TestSingleTxn_NewBlindWriteTxn_Error(t *testing.T) {
	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			return nil, fmt.Errorf("txn error")
		},
	}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "txn error")
}

func TestSingleTxn_GetCollection_Block_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).
		Return(nil, fmt.Errorf("no such collection"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such collection")
}

func TestSingleTxn_GetCollection_Tx_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).
		Return(nil, fmt.Errorf("no tx col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	// buildBlockDocument will fail because empty version → covers that error path too
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_GetCollection_Log_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).
		Return(nil, fmt.Errorf("no log col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_GetCollection_ALE_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).
		Return(nil, fmt.Errorf("no ale col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_GetCollection_BlockSig_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(cols.ale, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).
		Return(nil, fmt.Errorf("no sig col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_BuildBlockDocument_Error(t *testing.T) {
	// Empty version collection → buildBlockDocument fails because fields don't exist
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(cols.ale, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(cols.blockSig, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_BlockCreate_NonDuplicateError(t *testing.T) {
	td := setupRealCollectionVersions(t)

	txn := mocks.NewTxn(t)
	blockCol := mocks.NewCollection(t)
	blockCol.EXPECT().Version().Return(td.blockVersion)
	blockCol.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).
		Return(fmt.Errorf("some internal error"))
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil).Maybe()
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "some internal error")
}

func TestSingleTxn_BuildTxDocument_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithCreate(t, nil) // Create succeeds
	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(client.CollectionVersion{}) // empty → build fails

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_CreateManyTx_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithCreate(t, nil)
	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(td.txVersion)
	txCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create many tx error"))

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create many tx error")
}

func TestSingleTxn_BuildLogDocument_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithCreate(t, nil)
	txCol := td.txColWithCreateMany(t, nil)
	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(client.CollectionVersion{}) // empty → build fails

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_CreateManyLogs_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithCreate(t, nil)
	txCol := td.txColWithCreateMany(t, nil)
	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(td.logVersion)
	logCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create many logs error"))

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create many logs error")
}

func TestSingleTxn_BuildALEDocument_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithCreate(t, nil)
	txCol := td.txColWithCreateMany(t, nil)
	logCol := td.logColWithCreateMany(t, nil)
	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(client.CollectionVersion{}) // empty → build fails

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_CreateManyALE_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithCreate(t, nil)
	txCol := td.txColWithCreateMany(t, nil)
	logCol := td.logColWithCreateMany(t, nil)
	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create many ALE error"))

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create many ALE error")
}

func TestSingleTxn_SignBlock_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return nil, fmt.Errorf("signing error")
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // signing error is logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_VerifyBlockSig_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	sigCol := td.sigColWithCreate(t, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return false, fmt.Errorf("verify error")
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // verify error is logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_VerifyBlockSig_False(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	sigCol := td.sigColWithCreate(t, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return false, nil
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // false verification is logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_BuildBlockSigDoc_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	emptySigCol := mocks.NewCollection(t)
	emptySigCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(emptySigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return true, nil
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // build sig error is logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_CreateBlockSig_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	sigCol := mocks.NewCollection(t)
	sigCol.EXPECT().Version().Return(td.sigVersion)
	sigCol.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create sig error"))
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return true, nil
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // create sig error is logged
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_Commit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(fmt.Errorf("commit error"))

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit error")
}

func TestSingleTxn_TrackBlock_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.docIDTracker = &errDocIDTracker{}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // tracker error is logged, not returned
	assert.NotEmpty(t, blockID)
}

// =========================================================================
// createBlockBatched error paths
// =========================================================================

func TestBatched_NewTxn_Initial_Error(t *testing.T) {
	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			return nil, fmt.Errorf("txn error")
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "txn error")
}

func TestBatched_GetCollection_Block_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).
		Return(nil, fmt.Errorf("no block col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block col")
}

func TestBatched_BuildBlockDoc_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestBatched_BlockCreate_NonDuplicateError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := mocks.NewCollection(t)
	blockCol.EXPECT().Version().Return(td.blockVersion)
	blockCol.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("internal error"))

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal error")
}

func TestBatched_BlockCommit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().Commit().Return(fmt.Errorf("commit block error"))

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit block error")
}

func TestBatched_TxBatch_NewTxn_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0
	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil // block creation txn
			}
			return nil, fmt.Errorf("batch txn error") // all subsequent txns fail
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_TxBatch_GetCollection_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(nil, fmt.Errorf("no tx col"))
	batchTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return batchTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_TxBatch_BuildTxDoc_Warn(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	emptyTxCol := mocks.NewCollection(t)
	emptyTxCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(emptyTxCol, nil)
	batchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return batchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestBatched_TxBatch_CreateMany_NonDupError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(td.txVersion)
	txCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("batch create error"))

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	batchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return batchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_TxBatch_Commit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	batchTxn.EXPECT().Commit().Return(fmt.Errorf("commit tx batch error"))

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return batchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_SignBlock_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 1000 // keep batched path but with no txns to batch
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return nil, fmt.Errorf("sign error")
	}

	// Directly call batched since CreateBlockBatch routes based on doc count
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // sign error is logged
	assert.NotEmpty(t, blockID)
}

func TestBatched_VerifyBlockSig_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigCol := td.sigColWithCreate(t, nil)
	txn2 := sigTxn
	txn2.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn2.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return false, fmt.Errorf("verify error")
	}

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestBatched_VerifyBlockSig_False(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigCol := td.sigColWithCreate(t, nil)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return false, nil
	}

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestBatched_SigTxn_NewTxn_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return nil, fmt.Errorf("sig txn error")
		},
	}
	h := newMockHandler(t, db)

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // sig txn error is logged
	assert.NotEmpty(t, blockID)
}

func TestBatched_SigNil_Discard(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	// signBlockFn returns nil, nil → blockSig is nil → Discard

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestBatched_GetSigCollection_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).
		Return(nil, fmt.Errorf("no sig col"))
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) {
		return true, nil
	}

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestBatched_BuildSigDoc_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	emptySigCol := mocks.NewCollection(t)
	emptySigCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(emptySigCol, nil)
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) { return true, nil }

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // logged
	assert.NotEmpty(t, blockID)
}

func TestBatched_CreateSigDoc_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigCol := mocks.NewCollection(t)
	sigCol.EXPECT().Version().Return(td.sigVersion)
	sigCol.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create sig error"))
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) { return true, nil }

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

func TestBatched_SigCommit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigCol := td.sigColWithCreate(t, nil)
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Commit().Return(fmt.Errorf("commit sig error"))

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}
	h.verifyBlockSigFn = func(sig *node.BlockSignature, cids []cid.Cid) (bool, error) { return true, nil }

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // logged
	assert.NotEmpty(t, blockID)
}

func TestBatched_TrackBlock_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.docIDTracker = &errDocIDTracker{}

	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // tracker error is logged
	assert.NotEmpty(t, blockID)
}

// =========================================================================
// CreateBlockSignatureForExistingBlock error paths
// =========================================================================

func TestExistingSig_NewTmpTxn_Error(t *testing.T) {
	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) { return nil, fmt.Errorf("txn error") },
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "txn error")
}

func TestExistingSig_GetBlockCol_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).
		Return(nil, fmt.Errorf("no block col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block col")
}

func TestExistingSig_GetTxCol_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).
		Return(nil, fmt.Errorf("no tx col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
}

func TestExistingSig_GetLogCol_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).
		Return(nil, fmt.Errorf("no log col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
}

func TestExistingSig_GetALECol_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).
		Return(nil, fmt.Errorf("no ale col"))
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
}

func TestExistingSig_BuildBlockDoc_Error(t *testing.T) {
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t) // empty version → build fails
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(cols.ale, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestExistingSig_BuildTxDoc_Continue(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mocks.NewTxn(t)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockCol(t), nil)
	emptyTxCol := mocks.NewCollection(t)
	emptyTxCol.EXPECT().Version().Return(client.CollectionVersion{}) // build tx fails → continue
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(emptyTxCol, nil)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			return cidTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("no cids")
	}

	tx := testTx()
	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_CollectError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			return cidTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error")
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_InsufficientCIDs(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			return cidTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	// Return some CIDs but fewer than needed (need >= 1 for the block doc)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return nil, nil // 0 CIDs < len(allDocIDs)
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_TxnError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			return nil, fmt.Errorf("cid txn error")
		},
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_SigningTxn_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil // CID retry succeeds
			}
			return nil, fmt.Errorf("signing txn error")
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing txn error")
}

func TestExistingSig_CollectCIDsForSigning_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0
	collectCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		collectCount++
		if collectCount == 1 {
			return []cid.Cid{oneCID}, nil // retry succeeds
		}
		return nil, fmt.Errorf("collect signing error")
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collect signing error")
}

func TestExistingSig_SignBlock_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return nil, fmt.Errorf("sign error")
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign error")
}

func TestExistingSig_NilBlockSig(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}
	// signBlockFn returns nil, nil → "signing returned nil"

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing returned nil")
}

func TestExistingSig_GetSigCol_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).
		Return(nil, fmt.Errorf("no sig col"))
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sig col")
}

func TestExistingSig_BuildSigDoc_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	emptySigCol := mocks.NewCollection(t)
	emptySigCol.EXPECT().Version().Return(client.CollectionVersion{})
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(emptySigCol, nil)
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestExistingSig_CreateSigDoc_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	sigCol := mocks.NewCollection(t)
	sigCol.EXPECT().Version().Return(td.sigVersion)
	sigCol.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create error"))
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create error")
}

func TestExistingSig_Commit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard()

	sigCol := td.sigColWithCreate(t, nil)
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Commit().Return(fmt.Errorf("commit error"))

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return []cid.Cid{oneCID}, nil
	}
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return &node.BlockSignature{MerkleRoot: make([]byte, 32), Header: node.BlockSignature{}.Header}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit error")
}

// =========================================================================
// GetHighestBlockNumber mock tests
// =========================================================================

func TestGetHighestBlockNumber_GQLError(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Errors: []error{fmt.Errorf("gql error")},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

func TestGetHighestBlockNumber_DataCastFailure(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{Data: "not a map"},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

func TestGetHighestBlockNumber_MapSliceBranch(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []map[string]any{
							{"number": float64(42)},
						},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	num, err := h.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(42), num)
}

func TestGetHighestBlockNumber_MapSlice_Empty(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []map[string]any{},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

func TestGetHighestBlockNumber_DefaultBranch(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: "unexpected type",
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

func TestGetHighestBlockNumber_Int64Branch(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							map[string]any{"number": int64(99)},
						},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	num, err := h.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(99), num)
}

func TestGetHighestBlockNumber_IntBranch(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							map[string]any{"number": int(77)},
						},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	num, err := h.GetHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(77), num)
}

func TestGetHighestBlockNumber_InvalidNumberType(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							map[string]any{"number": "not a number"},
						},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

func TestGetHighestBlockNumber_InvalidFormat(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							"not a map",
						},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

// =========================================================================
// Real collection version helpers — extract versions from a real DefraDB
// to allow NewDocFromMap to succeed in mock tests
// =========================================================================

type realCollectionVersions struct {
	blockVersion client.CollectionVersion
	txVersion    client.CollectionVersion
	logVersion   client.CollectionVersion
	aleVersion   client.CollectionVersion
	sigVersion   client.CollectionVersion
}

func setupRealCollectionVersions(t *testing.T) *realCollectionVersions {
	t.Helper()
	td := testutilsSetupDefraDB(t)
	ctx := context.Background()

	txn, err := td.DB.NewBlindWriteTxn()
	require.NoError(t, err)
	tctx := td.DB.InitContext(ctx, txn)

	getVer := func(name string) client.CollectionVersion {
		col, err := txn.GetCollectionByName(tctx, name)
		require.NoError(t, err)
		return col.Version()
	}

	v := &realCollectionVersions{
		blockVersion: getVer(constants.CollectionBlock),
		txVersion:    getVer(constants.CollectionTransaction),
		logVersion:   getVer(constants.CollectionLog),
		aleVersion:   getVer(constants.CollectionAccessListEntry),
		sigVersion:   getVer(constants.CollectionBlockSignature),
	}
	txn.Discard()
	return v
}

func (v *realCollectionVersions) blockCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.blockVersion).Maybe()
	return c
}

func (v *realCollectionVersions) blockColWithCreate(t *testing.T, createErr error) *mocks.Collection {
	t.Helper()
	c := v.blockCol(t)
	c.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(createErr).Maybe()
	return c
}

func (v *realCollectionVersions) txCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.txVersion).Maybe()
	return c
}

func (v *realCollectionVersions) txColWithCreateMany(t *testing.T, err error) *mocks.Collection {
	t.Helper()
	c := v.txCol(t)
	c.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(err).Maybe()
	return c
}

func (v *realCollectionVersions) logCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.logVersion).Maybe()
	return c
}

func (v *realCollectionVersions) logColWithCreateMany(t *testing.T, err error) *mocks.Collection {
	t.Helper()
	c := v.logCol(t)
	c.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(err).Maybe()
	return c
}

func (v *realCollectionVersions) aleCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.aleVersion).Maybe()
	return c
}

func (v *realCollectionVersions) sigCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.sigVersion).Maybe()
	return c
}

func (v *realCollectionVersions) sigColWithCreate(t *testing.T, createErr error) *mocks.Collection {
	t.Helper()
	c := v.sigCol(t)
	c.EXPECT().Create(mock.Anything, mock.Anything, mock.Anything).Return(createErr).Maybe()
	return c
}

// =========================================================================
// Additional coverage tests — log/ALE batched loops, tracker with data, etc.
// =========================================================================

// TestSingleTxn_TrackBlock_WithALEs covers the aleIDs loop (lines 342-344)
func TestSingleTxn_TrackBlock_WithALEs(t *testing.T) {
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithCreateMany(t, nil), nil)
	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func() (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.docIDTracker = &errDocIDTracker{} // tracker error is logged, not returned

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// --- Batched: Log batch error paths ---

func TestBatched_LogBatch_NewTxn_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil // tx batch
			}
			if callCount == 3 {
				return nil, fmt.Errorf("log txn error") // log batch
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_LogBatch_GetCollection_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(nil, fmt.Errorf("no log col"))
	logBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return logBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_LogBatch_BuildDoc_Warn(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	emptyLogCol := mocks.NewCollection(t)
	emptyLogCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(emptyLogCol, nil)
	logBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return logBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.NoError(t, err) // build warnings, not errors
	assert.NotEmpty(t, blockID)
}

func TestBatched_LogBatch_CreateMany_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(td.logVersion)
	logCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("log create many error"))
	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	logBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return logBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_LogBatch_Commit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithCreateMany(t, nil), nil)
	logBatchTxn.EXPECT().Commit().Return(fmt.Errorf("log commit error"))

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return logBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

// --- Batched: ALE batch error paths ---

func TestBatched_ALEBatch_NewTxn_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return nil, fmt.Errorf("ale txn error") // ALE batch
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_ALEBatch_GetCollection_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(nil, fmt.Errorf("no ale col"))
	aleBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return aleBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_ALEBatch_BuildDoc_Warn(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	emptyALECol := mocks.NewCollection(t)
	emptyALECol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(emptyALECol, nil)
	aleBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return aleBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.NoError(t, err) // build warnings only
	assert.NotEmpty(t, blockID)
}

func TestBatched_ALEBatch_CreateMany_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("ale create many error"))
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	aleBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return aleBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

func TestBatched_ALEBatch_Commit_Error(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	aleBatchTxn.EXPECT().Commit().Return(fmt.Errorf("ale commit error"))

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return aleBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.NotEmpty(t, blockID)
	assert.Contains(t, err.Error(), "batch errors")
}

// --- Batched: "already exists" duplicate paths ---

func TestBatched_TxBatch_CreateMany_DuplicateError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(td.txVersion)
	txCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("document already exists"))
	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	batchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return batchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.NoError(t, err) // duplicate is logged, not an error
	assert.NotEmpty(t, blockID)
}

func TestBatched_LogBatch_CreateMany_DuplicateError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(td.logVersion)
	logCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("document already exists"))
	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	logBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return logBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.NoError(t, err) // duplicate is logged, not an error
	assert.NotEmpty(t, blockID)
}

func TestBatched_ALEBatch_CreateMany_DuplicateError(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().CreateMany(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("document already exists"))
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	aleBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			if callCount == 3 {
				return aleBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.NoError(t, err) // duplicate is logged, not an error
	assert.NotEmpty(t, blockID)
}

// --- GetHighestBlockNumber: []any empty ---

func TestGetHighestBlockNumber_AnySlice_Empty(t *testing.T) {
	db := &mockBlockDB{
		execReqFn: func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{},
					},
				},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.GetHighestBlockNumber(context.Background())
	require.Error(t, err)
}

// --- Batched: tracker with data (covers batched tracker loop) ---

func TestBatched_TrackBlock_WithData(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithCreate(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithCreateMany(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil
			}
			if callCount == 2 {
				return txBatchTxn, nil
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2
	h.docIDTracker = &errDocIDTracker{} // tracker error is logged

	tx := testTx()
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
}

// --- CreateBlockSignatureForExistingBlock: CID retry backoff path ---

func TestExistingSig_CIDRetry_BackoffPath(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0
	collectCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn1 := mocks.NewTxn(t)
	cidTxn1.EXPECT().Discard()
	cidTxn2 := mocks.NewTxn(t)
	cidTxn2.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard()

	oneCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn1, nil // first CID retry
			}
			if callCount == 3 {
				return cidTxn2, nil // second CID retry — succeeds
			}
			return sigTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		collectCount++
		if collectCount == 1 {
			return nil, nil // insufficient CIDs first time
		}
		return []cid.Cid{oneCID}, nil // sufficient second time
	}
	h.signBlockFn = func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error) {
		return nil, fmt.Errorf("sign error")
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
}

// TestExistingSig_BuildLogDoc_Continue covers the log build error continue path (line 556-557).
func TestExistingSig_BuildLogDoc_Continue(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	emptyLogCol := mocks.NewCollection(t)
	emptyLogCol.EXPECT().Version().Return(client.CollectionVersion{}) // build log fails → continue

	tmpTxn := mocks.NewTxn(t)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockCol(t), nil)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(emptyLogCol, nil)
	tmpTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	tmpTxn.EXPECT().Discard()

	cidTxn := mocks.NewTxn(t)
	cidTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			return cidTxn, nil
		},
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("no cids")
	}

	tx := testTx()
	receipt := testReceipt()
	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), []*types.Transaction{tx}, []*types.TransactionReceipt{receipt})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// TestExistingSig_CIDRetry_TxnError_Backoff covers the backoff path when
// NewBlindWriteTxn fails on a non-last attempt (lines 592-594).
func TestExistingSig_CIDRetry_TxnError_Backoff(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			// Both CID retry attempts fail with txn error
			return nil, fmt.Errorf("cid txn error")
		},
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2 // two attempts → first hits backoff, second is last

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// TestExistingSig_CIDRetry_CollectError_Backoff covers the backoff path when
// collectDocCIDsFn fails on a non-last attempt (lines 603-605).
func TestExistingSig_CIDRetry_CollectError_Backoff(t *testing.T) {
	td := setupRealCollectionVersions(t)
	callCount := 0

	tmpTxn := mockTxnWithCollections(t, &testDefraCollections{
		block: td.blockCol(t), tx: td.txCol(t), log: td.logCol(t),
		ale: td.aleCol(t), blockSig: td.sigCol(t),
	})
	tmpTxn.EXPECT().Discard()

	cidTxn1 := mocks.NewTxn(t)
	cidTxn1.EXPECT().Discard()
	cidTxn2 := mocks.NewTxn(t)
	cidTxn2.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func() (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return tmpTxn, nil
			}
			if callCount == 2 {
				return cidTxn1, nil
			}
			return cidTxn2, nil
		},
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	h.collectDocCIDsFn = func(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") // both attempts fail
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// testutilsSetupDefraDB wraps the testutils helper, extracting the DB interface.
type testDefraDB struct {
	DB blockDB
}

func testutilsSetupDefraDB(t *testing.T) *testDefraDB {
	t.Helper()
	td := testutils.SetupTestDefraDB(t)
	return &testDefraDB{DB: td.Node.DB}
}
