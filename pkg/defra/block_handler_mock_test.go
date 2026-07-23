package defra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/mocks"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

var testDocIDCounter atomic.Uint64

func nextTestDocID() client.DocID {
	n := testDocIDCounter.Add(1)
	data := fmt.Appendf(nil, "test-doc-%d", n)
	h, _ := mh.Sum(data, mh.SHA2_256, -1)
	c := cid.NewCidV1(cid.DagCBOR, h)
	return client.NewDocIDV0(c)
}

// ---------------------------------------------------------------------------
// mockBlockDB: implements the blockDB interface for unit tests
// ---------------------------------------------------------------------------

type mockBlockDB struct {
	newTxnFn  func(readOnly bool) (client.Txn, error)
	execReqFn func(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult
}

func (m *mockBlockDB) NewTxn(readOnly bool) (client.Txn, error) {
	return m.newTxnFn(readOnly)
}

func (m *mockBlockDB) ExecRequest(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	if m.execReqFn != nil {
		return m.execReqFn(ctx, request, opts...)
	}
	return &client.RequestResult{}
}

// ---------------------------------------------------------------------------
// errDocIDTracker: tracker that always returns an error
// ---------------------------------------------------------------------------

type errDocIDTracker struct{}

func (e *errDocIDTracker) TrackBlock(_ context.Context, _ int64, _ *BlockCreationResult) error {
	return errors.New("tracker error") //nolint: err113
}

// emptyExecReqFn returns an execReqFn that yields empty GQL data (no docIDs).
// collectExistingBlockDocIDs will return an empty allDocIDs slice.
func emptyExecReqFn() func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	return func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
		return &client.RequestResult{GQL: client.GQLResult{Data: map[string]any{}}}
	}
}

// execReqFnWithDocIDs returns an execReqFn that yields one docID for every
// collection query, so allDocIDs is non-empty (4 elements for 4 collections).
func execReqFnWithDocIDs() func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	return func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
		arr := []any{map[string]any{"_docID": "test-doc-id-1"}}
		return &client.RequestResult{
			GQL: client.GQLResult{
				Data: map[string]any{
					constants.CollectionBlock:           arr,
					constants.CollectionTransaction:     arr,
					constants.CollectionLog:             arr,
					constants.CollectionAccessListEntry: arr,
				},
			},
		}
	}
}

// execReqFnWithErrorForCol returns an execReqFn that returns a GQL error when
// the request targets the given collection name, and empty data otherwise.
func execReqFnWithErrorForCol(targetCol string) func(_ context.Context, request string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	return func(_ context.Context, request string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
		if strings.Contains(request, targetCol) {
			return &client.RequestResult{
				GQL: client.GQLResult{Errors: []error{fmt.Errorf("query error for %s", targetCol)}},
			}
		}
		return &client.RequestResult{GQL: client.GQLResult{Data: map[string]any{}}}
	}
}

// oneTestCID returns a reusable CID for mock tests.
func oneTestCID() cid.Cid {
	c, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	return c
}

// testCID is used only by the disabled TestBatched_SignsCompleteBlock; kept (commented) alongside it.
/*
func testCID(seed string) cid.Cid {
	h, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.DagCBOR, h)
}
*/

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newMockHandler creates a handler with a mock DB and default signing stubs.
func newMockHandler(t *testing.T, db *mockBlockDB) *BlockHandler {
	t.Helper()
	return &BlockHandler{
		db:            db,
		maxDocsPerTxn: 1000,
		collections:   constants.NewCollectionNames(constants.DefaultCollectionPrefix),
		signBatchFn: func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
			return nil, nil // no identity → nil sig
		},
		verifyBatchSigFn: func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
			return true, nil
		},
		collectDocCIDsFn: func(_ context.Context, _ []string) ([]cid.Cid, error) {
			return nil, nil
		},
		blockExistsFn:  func(_ context.Context, _ int64) (bool, error) { return false, nil },
		maxCIDRetries:  1,
		retryBackoffFn: func(int) time.Duration { return 0 },
	}
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
		c.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		c.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
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
	t.Parallel()
	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
			return nil, fmt.Errorf("txn error") //nolint: err113
		},
	}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "txn error")
}

func TestSingleTxn_GetCollection_Block_Error(t *testing.T) {
	t.Parallel()
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).
		Return(nil, fmt.Errorf("no such collection")) //nolint: err113
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such collection")
}

func TestSingleTxn_GetCollection_Tx_Error(t *testing.T) {
	t.Parallel()
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).
		Return(nil, fmt.Errorf("no tx col")) //nolint: err113
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	// buildBlockDocument will fail because empty version → covers that error path too
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_GetCollection_Log_Error(t *testing.T) {
	t.Parallel()
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).
		Return(nil, fmt.Errorf("no log col")) //nolint: err113
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_GetCollection_ALE_Error(t *testing.T) {
	t.Parallel()
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).
		Return(nil, fmt.Errorf("no ale col")) //nolint: err113
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_GetCollection_BlockSig_Error(t *testing.T) {
	t.Parallel()
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(cols.ale, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).
		Return(nil, fmt.Errorf("no sig col")) //nolint: err113
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
}

func TestSingleTxn_BuildBlockDocument_Error(t *testing.T) {
	t.Parallel()
	// Empty version collection → buildBlockDocument fails because fields don't exist
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(cols.tx, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(cols.log, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(cols.ale, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(cols.blockSig, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_BlockCreate_NonDuplicateError(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	txn := mocks.NewTxn(t)
	blockCol := mocks.NewCollection(t)
	blockCol.EXPECT().Version().Return(td.blockVersion)
	blockCol.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).
		Return(fmt.Errorf("some internal error"))
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil).Maybe()
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil).Maybe()
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "some internal error")
}

func TestSingleTxn_BuildTxDocument_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithAddDocument(t, nil) // Create succeeds
	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(client.CollectionVersion{}) // empty → build fails

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_CreateManyTx_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithAddDocument(t, nil)
	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(td.txVersion)
	txCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create many tx error")) //nolint: err113

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create many tx error")
}

func TestSingleTxn_BuildLogDocument_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithAddDocument(t, nil)
	txCol := td.txColWithAddManyDocuments(t, nil)
	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(client.CollectionVersion{}) // empty → build fails

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestSingleTxn_CreateManyLogs_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithAddDocument(t, nil)
	txCol := td.txColWithAddManyDocuments(t, nil)
	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(td.logVersion)
	logCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create many logs error")) //nolint: err113

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	tx := testTx()
	receipt := testReceipt()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: receipt}
	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create many logs error")
}

func TestSingleTxn_BuildALEDocument_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithAddDocument(t, nil)
	txCol := td.txColWithAddManyDocuments(t, nil)
	logCol := td.logColWithAddManyDocuments(t, nil)
	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(client.CollectionVersion{}) // empty → build fails

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := td.blockColWithAddDocument(t, nil)
	txCol := td.txColWithAddManyDocuments(t, nil)
	logCol := td.logColWithAddManyDocuments(t, nil)
	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create many ALE error")) //nolint: err113

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return nil, fmt.Errorf("signing error") //nolint: err113
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // signing error is logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_VerifyBlockSig_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	// No AddDocument expectation on the signature collection: a signature that fails verification must not be stored.
	sigCol := td.sigCol(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}
	h.verifyBatchSigFn = func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
		return false, fmt.Errorf("verify error") //nolint: err113
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // block is committed; the unverifiable signature is not stored
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_VerifyBlockSig_False(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	// No AddDocument expectation on the signature collection: a signature that fails verification must not be stored.
	sigCol := td.sigCol(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}
	h.verifyBatchSigFn = func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
		return false, nil
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // block is committed; the unverifiable signature is not stored
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_BuildBlockSigDoc_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	emptySigCol := mocks.NewCollection(t)
	emptySigCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(emptySigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}
	h.verifyBatchSigFn = func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
		return true, nil
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // build sig error is logged, not returned
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_CreateBlockSig_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	sigCol := mocks.NewCollection(t)
	sigCol.EXPECT().Version().Return(td.sigVersion)
	sigCol.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create sig error")) //nolint: err113
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}
	h.verifyBatchSigFn = func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
		return true, nil
	}

	blockID, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.NoError(t, err) // create sig error is logged
	assert.NotEmpty(t, blockID)
}

func TestSingleTxn_Commit_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(fmt.Errorf("commit error")) //nolint: err113

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)

	_, err := h.createBlockSingleTransaction(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit error")
}

func TestSingleTxn_TrackBlock_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(td.aleCol(t), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
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
	t.Parallel()
	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
			return nil, fmt.Errorf("txn error") //nolint: err113
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
	t.Parallel()
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).
		Return(nil, fmt.Errorf("no block col")) //nolint: err113
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block col")
}

func TestBatched_BuildBlockDoc_Error(t *testing.T) {
	t.Parallel()
	txn := mocks.NewTxn(t)
	cols := emptyVersionCollections(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(cols.block, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestBatched_BlockCreate_NonDuplicateError(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)

	blockCol := mocks.NewCollection(t)
	blockCol.EXPECT().Version().Return(td.blockVersion)
	blockCol.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("internal error")) //nolint: err113

	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(blockCol, nil)
	txn.EXPECT().Discard()

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal error")
}

func TestBatched_BlockCommit_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().Commit().Return(fmt.Errorf("commit block error")) //nolint: err113

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.maxDocsPerTxn = 2

	_, err := h.createBlockBatched(context.Background(), testBlock(), 100, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit block error")
}

func TestBatched_TxBatch_NewTxn_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0
	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
			callCount++
			if callCount == 1 {
				return blockTxn, nil // block creation txn
			}
			return nil, fmt.Errorf("batch txn error") //nolint: err113 all subsequent txns fail
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(nil, fmt.Errorf("no tx col")) //nolint: err113
	batchTxn.EXPECT().Discard()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	emptyTxCol := mocks.NewCollection(t)
	emptyTxCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(emptyTxCol, nil)
	batchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(td.txVersion)
	txCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("batch create error")) //nolint: err113

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	batchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	batchTxn.EXPECT().Commit().Return(fmt.Errorf("commit tx batch error")) //nolint: err113

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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

// TestBatched_SignsCompleteBlock is disabled and kept for reference (commented out below).
//
// It asserts the batched path signs over CIDs re-queried from the committed DB (the read-back path,
// via collectDocCIDsFn). createBlockBatched now signs over the in-context BatchCIDCollector instead,
// and the collector is filled by the defra write path (coreblock store) which these mocks do not
// exercise — so a mock-based test cannot drive the current signing path. The batched signing behavior
// is covered by the real-defra TestCreateBlockBatch_BatchedMode_SignsOverCommittedDocumentCIDs, which
// asserts the signed set equals the block's document CIDs.
/*
func TestBatched_SignsCompleteBlock(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txTxn := mocks.NewTxn(t)
	txTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txTxn.EXPECT().Commit().Return(nil)

	logTxn := mocks.NewTxn(t)
	logTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithAddManyDocuments(t, nil), nil)
	logTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigColWithAddDocument(t, nil), nil)
	sigTxn.EXPECT().Commit().Return(nil)

	callCount := 0
	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) {
		callCount++
		switch callCount {
		case 1:
			return blockTxn, nil
		case 2:
			return txTxn, nil
		case 3:
			return logTxn, nil
		default:
			return sigTxn, nil
		}
	}}
	h := newMockHandler(t, db)
	committedCIDs := []cid.Cid{testCID("cid-a"), testCID("cid-b"), testCID("cid-c")}
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return committedCIDs, nil
	}
	var signedCIDs []cid.Cid
	h.signBatchFn = func(_ context.Context, collector *node.BatchCIDCollector) (*node.BatchSignature, error) {
		signedCIDs = collector.GetCIDs()
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}

	tx := testTx()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: testReceipt()}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.NoError(t, err)
	assert.NotEmpty(t, blockID)
	// The block is signed over the CIDs re-queried from the committed DB, not the in-memory collector.
	assert.Equal(t, committedCIDs, signedCIDs)
}
*/

// TestBatched_SkipsSign_OnBatchError checks that a block whose write reported an error is not
// signed: the block is created, an error is returned, and signing never runs.
func TestBatched_SkipsSign_OnBatchError(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txTxn := mocks.NewTxn(t)
	txTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txTxn.EXPECT().Commit().Return(nil)

	logTxn := mocks.NewTxn(t)
	logTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithAddManyDocuments(t, fmt.Errorf("log write error")), nil) //nolint:err113
	logTxn.EXPECT().Discard().Maybe()

	callCount := 0
	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) {
		callCount++
		switch callCount {
		case 1:
			return blockTxn, nil
		case 2:
			return txTxn, nil
		default:
			return logTxn, nil
		}
	}}
	h := newMockHandler(t, db)
	signed := false
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		signed = true
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}

	tx := testTx()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: testReceipt()}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "batch errors")
	assert.NotEmpty(t, blockID)
	assert.False(t, signed)
}

// TestBatched_SkipsSign_OnDroppedBatch checks that a silently dropped batch is not signed: an
// already-exists batch leaves the block a document short without reporting a batch error, so the
// doc-count gate must catch it.
func TestBatched_SkipsSign_OnDroppedBatch(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txTxn := mocks.NewTxn(t)
	txTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txTxn.EXPECT().Commit().Return(nil)

	logTxn := mocks.NewTxn(t)
	logTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithAddManyDocuments(t, fmt.Errorf("already exists")), nil) //nolint:err113
	logTxn.EXPECT().Discard().Maybe()

	callCount := 0
	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) {
		callCount++
		switch callCount {
		case 1:
			return blockTxn, nil
		case 2:
			return txTxn, nil
		default:
			return logTxn, nil
		}
	}}
	h := newMockHandler(t, db)
	signed := false
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		signed = true
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}

	tx := testTx()
	receiptMap := map[string]*types.TransactionReceipt{tx.Hash: testReceipt()}
	blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
	require.NoError(t, err) // already-exists is not reported as a batch error
	assert.NotEmpty(t, blockID)
	assert.False(t, signed)
}

// TestBatched_SkipsSign_OnVerifyFailure checks that a signature which fails its own verification
// is not stored: the block is created but no signature transaction is opened.
func TestBatched_SkipsSign_OnVerifyFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		verify func(*node.BatchSignature, []cid.Cid) (bool, error)
	}{
		{"does not verify", func(*node.BatchSignature, []cid.Cid) (bool, error) { return false, nil }},
		{"verify errors", func(*node.BatchSignature, []cid.Cid) (bool, error) {
			return false, fmt.Errorf("verify error") //nolint:err113
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			td := setupRealCollectionVersions(t)

			blockTxn := mocks.NewTxn(t)
			blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
			blockTxn.EXPECT().Commit().Return(nil)

			txTxn := mocks.NewTxn(t)
			txTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
			txTxn.EXPECT().Commit().Return(nil)

			logTxn := mocks.NewTxn(t)
			logTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithAddManyDocuments(t, nil), nil)
			logTxn.EXPECT().Commit().Return(nil)

			callCount := 0
			db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) {
				callCount++
				switch callCount {
				case 1:
					return blockTxn, nil
				case 2:
					return txTxn, nil
				case 3:
					return logTxn, nil
				default:
					return nil, fmt.Errorf("signature transaction should not be opened") //nolint:err113
				}
			}}
			h := newMockHandler(t, db)
			h.collectDocCIDsFn = func(_ context.Context, docIDs []string) ([]cid.Cid, error) {
				cids := make([]cid.Cid, len(docIDs))
				for i := range docIDs {
					cids[i] = oneTestCID()
				}
				return cids, nil
			}
			h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
				return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
			}
			h.verifyBatchSigFn = tc.verify

			tx := testTx()
			receiptMap := map[string]*types.TransactionReceipt{tx.Hash: testReceipt()}
			blockID, err := h.createBlockBatched(context.Background(), testBlock(), 100, []*types.Transaction{tx}, receiptMap)
			require.NoError(t, err)
			assert.NotEmpty(t, blockID)
			assert.Equal(t, 3, callCount) // signature transaction never opened
		})
	}
}

// TestWriteBatchWithRetry checks the retry loop: a conflict is retried, a non-conflict error is
// not, it gives up after maxBatchRetries, and a discarded attempt's CIDs are rolled back.
func TestWriteBatchWithRetry(t *testing.T) {
	t.Parallel()
	h := newMockHandler(t, &mockBlockDB{})

	t.Run("retries a conflict then succeeds", func(t *testing.T) {
		calls := 0
		err := h.writeBatchWithRetry(context.Background(), 100, "log", func() error {
			calls++
			if calls == 1 {
				return fmt.Errorf("transaction conflict") //nolint:err113
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, calls)
	})

	t.Run("gives up after maxBatchRetries", func(t *testing.T) {
		calls := 0
		err := h.writeBatchWithRetry(context.Background(), 100, "log", func() error {
			calls++
			return fmt.Errorf("transaction conflict") //nolint:err113
		})
		require.Error(t, err)
		assert.Equal(t, maxBatchRetries, calls)
	})

	t.Run("does not retry a non-conflict error", func(t *testing.T) {
		calls := 0
		err := h.writeBatchWithRetry(context.Background(), 100, "log", func() error {
			calls++
			return fmt.Errorf("some other error") //nolint:err113
		})
		require.Error(t, err)
		assert.Equal(t, 1, calls)
	})

	t.Run("rolls back a discarded attempt's CIDs", func(t *testing.T) {
		collector := node.NewBatchCIDCollector()
		collector.Add(oneTestCID()) // a CID from an earlier committed batch
		ctx := node.ContextWithBatchSigning(context.Background(), collector)

		calls := 0
		err := h.writeBatchWithRetry(ctx, 100, "log", func() error {
			collector.Add(oneTestCID()) // the attempt records its CID before the transaction commits
			calls++
			if calls == 1 {
				return fmt.Errorf("transaction conflict") //nolint:err113
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, calls)
		// Earlier CID plus the committed attempt's CID; the discarded attempt's CID was rolled back.
		assert.Equal(t, 2, collector.Len())
	})
}

// TestBatchCreateTransactions_RetriesConflict checks that a transaction batch whose commit
// conflicts is retried and the transaction is still written.
func TestBatchCreateTransactions_RetriesConflict(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	txn1 := mocks.NewTxn(t)
	txn1.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txn1.EXPECT().Commit().Return(fmt.Errorf("transaction conflict")) //nolint:err113
	txn2 := mocks.NewTxn(t)
	txn2.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txn2.EXPECT().Commit().Return(nil)

	callCount := 0
	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) {
		callCount++
		if callCount == 1 {
			return txn1, nil
		}
		return txn2, nil
	}}
	h := newMockHandler(t, db)

	blockID := nextTestDocID().String()
	txHashToID, batchErrors := h.batchCreateTransactions(context.Background(), 100, []*types.Transaction{testTx()}, blockID)
	assert.Empty(t, batchErrors)
	assert.Len(t, txHashToID, 1)
	assert.Equal(t, 2, callCount) // one conflict, one retry
}

// TestBatchCreateTransactions_AlreadyExistsDrops checks that an already-exists batch is dropped
// without retrying and without reporting a batch error (it is P2P echo, not a conflict).
func TestBatchCreateTransactions_AlreadyExistsDrops(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, fmt.Errorf("already exists")), nil) //nolint:err113
	txn.EXPECT().Discard().Maybe()

	callCount := 0
	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) {
		callCount++
		return txn, nil
	}}
	h := newMockHandler(t, db)

	blockID := nextTestDocID().String()
	txHashToID, batchErrors := h.batchCreateTransactions(context.Background(), 100, []*types.Transaction{testTx()}, blockID)
	assert.Empty(t, batchErrors)  // already-exists is not a batch error
	assert.Empty(t, txHashToID)   // the batch was dropped
	assert.Equal(t, 1, callCount) // not retried
}

func TestBatched_TrackBlock_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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

func TestExistingSig_GQLQuery_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{Errors: []error{fmt.Errorf("gql query error")}},
			}
		},
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query block docIDs")
}

func TestExistingSig_GetBlockCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(constants.CollectionBlock),
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query block docIDs")
}

func TestExistingSig_GetTxCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(constants.CollectionTransaction),
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query tx docIDs")
}

func TestExistingSig_GetLogCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(constants.CollectionLog),
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query log docIDs")
}

func TestExistingSig_GetALECol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(constants.CollectionAccessListEntry),
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query ale docIDs")
}

func TestExistingSig_CIDRetry_CollectError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") // nolint:err113
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_InsufficientCIDs(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, nil // 0 CIDs < len(allDocIDs)=4
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_TxnError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_SigningTxn_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return nil, fmt.Errorf("signing txn error") }, // nolint:err113
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing txn error")
}

func TestExistingSig_SignBlock_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return nil, fmt.Errorf("sign error") // nolint:err113
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign error")
}

func TestExistingSig_NilBlockSig(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	// signBatchFn returns nil, nil -> "signing returned nil"

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing returned nil")
}

func TestExistingSig_GetSigCol_Error(t *testing.T) {
	t.Parallel()
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).
		Return(nil, fmt.Errorf("no sig col")) // nolint:err113
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return sigTxn, nil },
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sig col")
}

func TestExistingSig_BuildSigDoc_Error(t *testing.T) {
	t.Parallel()

	emptySigCol := mocks.NewCollection(t)
	emptySigCol.EXPECT().Version().Return(client.CollectionVersion{})
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(emptySigCol, nil)
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return sigTxn, nil },
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestExistingSig_CreateSigDoc_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	sigCol := mocks.NewCollection(t)
	sigCol.EXPECT().Version().Return(td.sigVersion)
	sigCol.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create error")) // nolint:err113
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Discard()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return sigTxn, nil },
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create error")
}

func TestExistingSig_Commit_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	sigCol := td.sigColWithAddDocument(t, nil)
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Commit().Return(fmt.Errorf("commit error")) // nolint:err113

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return sigTxn, nil },
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit error")
}

// =========================================================================
// GetHighestBlockNumber mock tests
// =========================================================================

func TestGetHighestBlockNumber_GQLError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []map[string]any{
							{constants.NumberFieldValue: float64(42)},
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							map[string]any{constants.NumberFieldValue: int64(99)},
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							map[string]any{constants.NumberFieldValue: int(77)},
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
			return &client.RequestResult{
				GQL: client.GQLResult{
					Data: map[string]any{
						constants.CollectionBlock: []any{
							map[string]any{constants.NumberFieldValue: "not a number"},
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
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
// Real collection version helpers: extract versions from a real DefraDB
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

	txn, err := td.DB.NewTxn(false)
	require.NoError(t, err)

	getVer := func(name string) client.CollectionVersion {
		col, err := txn.GetCollectionByName(ctx, name)
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

func (v *realCollectionVersions) blockColWithAddDocument(t *testing.T, createErr error) *mocks.Collection {
	t.Helper()
	c := v.blockCol(t)
	c.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Run(func(_ context.Context, doc *client.Document, _ ...options.Enumerable[options.AddDocumentOptions]) {
		if createErr != nil {
			return
		}
		client.ApplySavedDocumentID(doc, nextTestDocID())
	}).Return(createErr).Maybe()
	return c
}

func (v *realCollectionVersions) txCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.txVersion).Maybe()
	return c
}

func (v *realCollectionVersions) txColWithAddManyDocuments(t *testing.T, err error) *mocks.Collection {
	t.Helper()
	c := v.txCol(t)
	c.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Run(func(_ context.Context, docs []*client.Document, _ ...options.Enumerable[options.AddDocumentOptions]) {
		if err != nil {
			return
		}
		for _, doc := range docs {
			client.ApplySavedDocumentID(doc, nextTestDocID())
		}
	}).Return(err).Maybe()
	return c
}

func (v *realCollectionVersions) logCol(t *testing.T) *mocks.Collection {
	t.Helper()
	c := mocks.NewCollection(t)
	c.EXPECT().Version().Return(v.logVersion).Maybe()
	return c
}

func (v *realCollectionVersions) logColWithAddManyDocuments(t *testing.T, err error) *mocks.Collection {
	t.Helper()
	c := v.logCol(t)
	c.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Run(func(_ context.Context, docs []*client.Document, _ ...options.Enumerable[options.AddDocumentOptions]) {
		if err != nil {
			return
		}
		for _, doc := range docs {
			client.ApplySavedDocumentID(doc, nextTestDocID())
		}
	}).Return(err).Maybe()
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

func (v *realCollectionVersions) sigColWithAddDocument(t *testing.T, createErr error) *mocks.Collection {
	t.Helper()
	c := v.sigCol(t)
	c.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Run(func(_ context.Context, doc *client.Document, _ ...options.Enumerable[options.AddDocumentOptions]) {
		if createErr != nil {
			return
		}
		client.ApplySavedDocumentID(doc, nextTestDocID())
	}).Return(createErr).Maybe()
	return c
}

// =========================================================================
// Additional coverage tests: log/ALE batched loops, tracker with data, etc.
// =========================================================================

// TestSingleTxn_TrackBlock_WithALEs covers the aleIDs loop (lines 342-344). We don't need to test the log loop as well since it's the same code and the log batch tests cover it.
func TestSingleTxn_TrackBlock_WithALEs(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)
	txn := mocks.NewTxn(t)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithAddManyDocuments(t, nil), nil)
	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Run(func(_ context.Context, docs []*client.Document, _ ...options.Enumerable[options.AddDocumentOptions]) {
		for _, doc := range docs {
			client.ApplySavedDocumentID(doc, nextTestDocID())
		}
	}).Return(nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	txn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlockSignature, mock.Anything).Return(td.sigCol(t), nil)
	txn.EXPECT().Commit().Return(nil)

	db := &mockBlockDB{newTxnFn: func(_ bool) (client.Txn, error) { return txn, nil }}
	h := newMockHandler(t, db)
	h.docIDTracker = &errDocIDTracker{} // tracker error is logged, not returned.

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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(nil, fmt.Errorf("no log col"))
	logBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	emptyLogCol := mocks.NewCollection(t)
	emptyLogCol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(emptyLogCol, nil)
	logBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(td.logVersion)
	logCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("log create many error"))
	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	logBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(td.logColWithAddManyDocuments(t, nil), nil)
	logBatchTxn.EXPECT().Commit().Return(fmt.Errorf("log commit error"))

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(nil, fmt.Errorf("no ale col"))
	aleBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	emptyALECol := mocks.NewCollection(t)
	emptyALECol.EXPECT().Version().Return(client.CollectionVersion{}) // build fails
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(emptyALECol, nil)
	aleBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("ale create many error"))
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	aleBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Run(func(_ context.Context, docs []*client.Document, _ ...options.Enumerable[options.AddDocumentOptions]) {
		for _, doc := range docs {
			client.ApplySavedDocumentID(doc, nextTestDocID())
		}
	}).Return(nil)
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	aleBatchTxn.EXPECT().Commit().Return(fmt.Errorf("ale commit error"))

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txCol := mocks.NewCollection(t)
	txCol.EXPECT().Version().Return(td.txVersion)
	txCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("document already exists"))
	batchTxn := mocks.NewTxn(t)
	batchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(txCol, nil)
	batchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	logCol := mocks.NewCollection(t)
	logCol.EXPECT().Version().Return(td.logVersion)
	logCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("document already exists"))
	logBatchTxn := mocks.NewTxn(t)
	logBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionLog, mock.Anything).Return(logCol, nil)
	logBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	aleCol := mocks.NewCollection(t)
	aleCol.EXPECT().Version().Return(td.aleVersion)
	aleCol.EXPECT().AddManyDocuments(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("document already exists"))
	aleBatchTxn := mocks.NewTxn(t)
	aleBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionAccessListEntry, mock.Anything).Return(aleCol, nil)
	aleBatchTxn.EXPECT().Discard()

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
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
	t.Parallel()
	td := setupRealCollectionVersions(t)
	callCount := 0

	blockTxn := mocks.NewTxn(t)
	blockTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionBlock, mock.Anything).Return(td.blockColWithAddDocument(t, nil), nil)
	blockTxn.EXPECT().Commit().Return(nil)

	txBatchTxn := mocks.NewTxn(t)
	txBatchTxn.EXPECT().GetCollectionByName(mock.Anything, constants.CollectionTransaction, mock.Anything).Return(td.txColWithAddManyDocuments(t, nil), nil)
	txBatchTxn.EXPECT().Commit().Return(nil)

	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().Discard().Maybe()

	db := &mockBlockDB{
		newTxnFn: func(_ bool) (client.Txn, error) {
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
	t.Parallel()
	collectCount := 0

	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		collectCount++
		if collectCount == 1 {
			return nil, nil // insufficient CIDs first time (triggers backoff)
		}
		if collectCount == 2 {
			return []cid.Cid{oneTestCID()}, nil // lastCIDCount=1, loop ends, waitForCIDs returns nil
		}
		return nil, fmt.Errorf("sign error") // nolint:err113 // 3rd call in signAndStore
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return nil, fmt.Errorf("sign error") // nolint:err113
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
}

// TestExistingSig_BuildLogDoc_Continue covers the CID collection failure path:
// collectExistingBlockDocIDs returns empty (no docs), collectDocCIDsFn errors,
// waitForCIDs exhausts retries → "no CIDs found".
func TestExistingSig_BuildLogDoc_Continue(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("no cids")
	}

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// TestExistingSig_CIDRetry_TxnError_Backoff covers the backoff path when
// collectDocCIDsFn returns insufficient CIDs across multiple attempts.
func TestExistingSig_CIDRetry_TxnError_Backoff(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2 // two attempts → first hits backoff, second is last

	_, err := h.CreateBlockSignatureForExistingBlock(context.Background(), 100, "0xhash", testBlock(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// TestExistingSig_CIDRetry_CollectError_Backoff covers the backoff path when
// collectDocCIDsFn fails on every attempt.
func TestExistingSig_CIDRetry_CollectError_Backoff(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") // both attempts fail, triggers backoff between them
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
