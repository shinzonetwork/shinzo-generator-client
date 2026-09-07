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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
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
		signBatchFn: func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
			return nil, nil
		},
		verifyBatchSigFn: func(_ *node.BatchSignature, _ []cid.Cid) (bool, error) {
			return true, nil
		},
		collectDocCIDsFn: func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
			return nil, nil
		},
		maxCIDRetries:  1,
		retryBackoffFn: func(int) time.Duration { return 0 },
	}
}

func testBlock() *evm.Block {
	return mockBlock("0x64")
}

func testTx() *evm.Transaction {
	return mockTransaction("0xabc1000000000000000000000000000000000000000000000000000000000001", "100")
}

func testReceipt() *evm.TransactionReceipt {
	return mockReceipt("0xabc1000000000000000000000000000000000000000000000000000000000001", "0x64")
}

func buildSigGroups(t *testing.T, block *evm.Block, txs []*evm.Transaction, receipts []*evm.TransactionReceipt) chains.ConversionResult {
	t.Helper()
	result, err := evm.NewConverter(nil).Convert(context.Background(), &evm.BlockBundle{
		Block:        block,
		Transactions: txs,
		Receipts:     receipts,
	})
	require.NoError(t, err)
	return result
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

	t.Run("returns ctx.Err() when ctx is cancelled during backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		calls := 0
		err := h.writeBatchWithRetry(ctx, 100, "log", func() error {
			calls++
			return fmt.Errorf("transaction conflict") //nolint:err113
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, calls)
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
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetBlockCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colBlock),
	}
	h := newMockHandler(t, db)
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetTxCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colTransaction),
	}
	h := newMockHandler(t, db)
	result := buildSigGroups(t, testBlock(), []*evm.Transaction{testTx()}, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_GetLogCol_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithErrorForCol(colLog),
	}
	h := newMockHandler(t, db)
	result := buildSigGroups(t, testBlock(), []*evm.Transaction{testTx()}, []*evm.TransactionReceipt{testReceipt()})

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	tx.AccessList = []evm.AccessListEntry{{Address: "0x01", StorageKeys: []string{"0x02"}}}
	result := buildSigGroups(t, testBlock(), []*evm.Transaction{tx}, []*evm.TransactionReceipt{testReceipt()})

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query docIDs for")
}

func TestExistingSig_CIDRetry_CollectError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") //nolint:err113
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return nil, nil
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

func TestExistingSig_CIDRetry_TxnError(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: execReqFnWithDocIDs(),
	}
	h := newMockHandler(t, db)
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing txn error")
}

func TestExistingSig_SignBlock_Error(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return nil, fmt.Errorf("sign error") //nolint:err113
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign error")
}

func TestExistingSig_NilBlockSig(t *testing.T) {
	t.Parallel()
	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return []cid.Cid{oneTestCID()}, nil
	}
	h.signBatchFn = func(_ context.Context, _ *node.BatchCIDCollector) (*node.BatchSignature, error) {
		return &node.BatchSignature{MerkleRoot: make([]byte, 32)}, nil //nolint:mnd
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit error")
}

// --- CID retry backoff paths ---

func TestExistingSig_BuildLogDoc_Continue(t *testing.T) {
	t.Parallel()

	db := &mockBlockDB{
		execReqFn: emptyExecReqFn(),
	}
	h := newMockHandler(t, db)
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("no cids") //nolint:err113
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
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
	h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
		return nil, fmt.Errorf("collect error") //nolint:err113
	}
	result := buildSigGroups(t, testBlock(), nil, nil)

	_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CIDs found")
}

// --- CID retry cancellation + annotated failure message (#362) ---

func TestExistingSig_CIDRetry_Cancellation(t *testing.T) {
	t.Parallel()
	collectErr := fmt.Errorf("collect error") //nolint:err113

	tests := []struct {
		name       string
		preCancel  bool          // cancel before the call vs during collect
		cancelCall int           // 1-based collect call that cancels the ctx
		collectErr error         // non-nil → collect takes the query-error path
		backoff    time.Duration // injected retryBackoffFn
		maxRetries int
		wantCalls  int
	}{
		{
			name:       "query error path exits within one backoff tick",
			cancelCall: 1,
			collectErr: collectErr,
			backoff:    5 * time.Second,
			maxRetries: 3,
			wantCalls:  1,
		},
		{
			name:       "partial coverage path exits within one backoff tick",
			cancelCall: 1, // collect returns nil,nil → 0/1 CIDs: partial coverage
			backoff:    5 * time.Second,
			maxRetries: 3,
			wantCalls:  1,
		},
		{
			name:       "mid-sequence cancellation after an elapsed wait",
			cancelCall: 2,
			backoff:    10 * time.Millisecond, // first wait completes for real
			maxRetries: 5,
			wantCalls:  2,
		},
		{
			name:       "pre-cancelled ctx stops the first query",
			preCancel:  true,
			maxRetries: 3,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.preCancel {
				cancel()
			}

			calls := 0
			db := &mockBlockDB{
				execReqFn: execReqFnWithDocIDs(),
			}
			h := newMockHandler(t, db)
			h.maxCIDRetries = tc.maxRetries
			h.retryBackoffFn = func(int) time.Duration { return tc.backoff }
			h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
				calls++
				if calls == tc.cancelCall {
					cancel()
				}
				return nil, tc.collectErr
			}
			result := buildSigGroups(t, testBlock(), nil, nil)

			start := time.Now()
			_, err := h.SignExisting(ctx, result, "0xhash", 100)
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, tc.wantCalls, calls, "no query may be issued after cancellation")
			assert.Less(t, elapsed, time.Second, "exit must be well within one backoff tick")
		})
	}
}

func TestExistingSig_CIDRetry_IncompleteCoverageMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		maxRetries int
		collect    func(call int) ([]cid.Cid, error)
		wantMsg    string
	}{
		{
			name:       "count observed on the final attempt",
			maxRetries: 2,
			collect: func(call int) ([]cid.Cid, error) {
				if call == 1 {
					return nil, nil
				}
				return []cid.Cid{oneTestCID()}, nil
			},
			wantMsg: "1/2 docs as of attempt 2/2",
		},
		{
			name:       "count from an earlier attempt, later attempts error",
			maxRetries: 3,
			collect: func(call int) ([]cid.Cid, error) {
				if call == 1 {
					return []cid.Cid{oneTestCID()}, nil // 1/2 observed on attempt 1
				}
				return nil, fmt.Errorf("collect error") //nolint:err113
			},
			wantMsg: "1/2 docs as of attempt 1/3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			db := &mockBlockDB{
				execReqFn: execReqFnWithDocIDs(),
			}
			h := newMockHandler(t, db)
			h.maxCIDRetries = tc.maxRetries
			h.collectDocCIDsFn = func(_ context.Context, _ []string, _ []string) ([]cid.Cid, error) {
				calls++
				return tc.collect(calls)
			}
			// Two groups give allDocIDs=2, so partial-but-nonzero coverage is reachable and
			// the failure message reports the count with the attempt it was observed on.
			result := chains.ConversionResult{
				Groups: []chains.DocumentGroup{
					{Collection: colBlock},
					{Collection: colTransaction},
				},
				SignatureCollection: colBlockSignature,
			}

			_, err := h.SignExisting(context.Background(), result, "0xhash", 100)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "incomplete CID coverage")
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
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
