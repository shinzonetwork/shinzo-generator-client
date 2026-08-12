package defra

import (
	"context"
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

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/mocks"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

const (
	colBlock           = "Ethereum__Mainnet__Block"
	colTransaction     = "Ethereum__Mainnet__Transaction"
	colLog             = "Ethereum__Mainnet__Log"
	colAccessListEntry = "Ethereum__Mainnet__AccessListEntry"
	colBlockSignature  = "Ethereum__Mainnet__BlockSignature"
)

var testDocIDCounter atomic.Uint64

func nextTestDocID() client.DocID {
	n := testDocIDCounter.Add(1)
	data := fmt.Appendf(nil, "test-doc-%d", n)
	h, _ := mh.Sum(data, mh.SHA2_256, -1)
	c := cid.NewCidV1(cid.DagCBOR, h)
	return client.NewDocIDV0(c)
}

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

func emptyExecReqFn() func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	return func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
		return &client.RequestResult{GQL: client.GQLResult{Data: map[string]any{}}}
	}
}

func execReqFnWithDocIDs() func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
	return func(_ context.Context, _ string, _ ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult {
		arr := []any{map[string]any{"_docID": "test-doc-id-1"}}
		return &client.RequestResult{
			GQL: client.GQLResult{
				Data: map[string]any{
					colBlock:           arr,
					colTransaction:     arr,
					colLog:             arr,
					colAccessListEntry: arr,
				},
			},
		}
	}
}

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

func oneTestCID() cid.Cid {
	c, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	return c
}

func newMockHandler(t *testing.T, db *mockBlockDB) *BlockHandler {
	t.Helper()
	return &BlockHandler{
		db:            db,
		maxDocsPerTxn: 1000,
		collections:   chains.NewStubCollections("Ethereum__Mainnet"),
		signBatchFn: func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
			return nil, nil
		},
		verifyBatchSigFn: func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
			return true, nil
		},
		collectDocCIDsFn: func(_ context.Context, _ []string) ([]cid.Cid, error) {
			return nil, nil
		},
		maxCIDRetries:  1,
		retryBackoffFn: func(int) time.Duration { return 0 },
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

func buildSigGroups(t *testing.T, block *types.Block, txs []*types.Transaction, receipts []*types.TransactionReceipt) []chains.DocumentGroup {
	t.Helper()
	groups, _, err := testutils.BuildEVMGroups(chains.NewStubCollections("Ethereum__Mainnet"), block, txs, receipts)
	require.NoError(t, err)
	return groups
}

// =========================================================================
// writeBatchWithRetry
// =========================================================================

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
		collector.Add(oneTestCID())
		ctx := node.ContextWithBatchSigning(context.Background(), collector)

		calls := 0
		err := h.writeBatchWithRetry(ctx, 100, "log", func() error {
			collector.Add(oneTestCID())
			calls++
			if calls == 1 {
				return fmt.Errorf("transaction conflict") //nolint:err113
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, calls)
		assert.Equal(t, 2, collector.Len())
	})
}

// =========================================================================
// SignExisting error paths
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
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetBlockCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colBlock),
	}
	h := newMockHandler(t, db)
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetTxCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colTransaction),
	}
	h := newMockHandler(t, db)
	groups := buildSigGroups(t, testBlock(), []*types.Transaction{testTx()}, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetLogCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colLog),
	}
	h := newMockHandler(t, db)
	groups := buildSigGroups(t, testBlock(), []*types.Transaction{testTx()}, []*types.TransactionReceipt{testReceipt()})

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetALECol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colAccessListEntry),
	}
	h := newMockHandler(t, db)
	tx := testTx()
	tx.AccessList = []types.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	groups := buildSigGroups(t, testBlock(), []*types.Transaction{tx}, []*types.TransactionReceipt{testReceipt()})

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_CIDRetry_CollectError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") //nolint:err113
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
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
		return nil, nil
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_TxnError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_SigningTxn_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return nil, fmt.Errorf("signing txn error") }, //nolint:err113
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
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
		return nil, fmt.Errorf("sign error") //nolint:err113
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
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
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing returned nil")
}

func TestExistingSig_GetSigCol_Error(t *testing.T) {
	t.Parallel()
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, colBlockSignature, mock.Anything).
		Return(nil, fmt.Errorf("no sig col")) //nolint:err113
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
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sig col")
}

func TestExistingSig_BuildSigDoc_Error(t *testing.T) {
	t.Parallel()

	emptySigCol := mocks.NewCollection(t)
	emptySigCol.EXPECT().Version().Return(client.CollectionVersion{})
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, colBlockSignature, mock.Anything).Return(emptySigCol, nil)
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
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field does not exist")
}

func TestExistingSig_CreateSigDoc_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	sigCol := mocks.NewCollection(t)
	sigCol.EXPECT().Version().Return(td.sigVersion)
	sigCol.EXPECT().AddDocument(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("create error")) //nolint:err113
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, colBlockSignature, mock.Anything).Return(sigCol, nil)
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
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create error")
}

func TestExistingSig_Commit_Error(t *testing.T) {
	t.Parallel()
	td := setupRealCollectionVersions(t)

	sigCol := td.sigColWithAddDocument(t, nil)
	sigTxn := mocks.NewTxn(t)
	sigTxn.EXPECT().GetCollectionByName(mock.Anything, colBlockSignature, mock.Anything).Return(sigCol, nil)
	sigTxn.EXPECT().Commit().Return(fmt.Errorf("commit error")) //nolint:err113

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
		newTxnFn:  func(_ bool) (client.Txn, error) { return sigTxn, nil },
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit error")
}

// --- CID retry backoff paths ---

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
			return nil, nil
		}
		if collectCount == 2 {
			return []cid.Cid{oneTestCID()}, nil
		}
		return nil, fmt.Errorf("sign error") //nolint:err113
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return nil, fmt.Errorf("sign error") //nolint:err113
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
}

func TestExistingSig_BuildLogDoc_Continue(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("no cids") //nolint:err113
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_TxnError_Backoff(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_CollectError_Backoff(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.maxCIDRetries = 2
	h.collectDocCIDsFn = func(_ context.Context, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") //nolint:err113
	}
	groups := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), groups, colBlockSignature, "0xhash", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// =========================================================================
// Real collection version helpers: extract versions from a real DefraDB
// to allow NewDocFromMap to succeed in mock tests
// =========================================================================

type realCollectionVersions struct {
	sigVersion client.CollectionVersion
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
		sigVersion: getVer(colBlockSignature),
	}
	txn.Discard()
	return v
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

// testutilsSetupDefraDB wraps the testutils helper, extracting the DB interface.
type testDefraDB struct {
	DB blockDB
}

func testutilsSetupDefraDB(t *testing.T) *testDefraDB {
	t.Helper()
	td := testutils.SetupTestDefraDB(t)
	return &testDefraDB{DB: td.Node.DB}
}
