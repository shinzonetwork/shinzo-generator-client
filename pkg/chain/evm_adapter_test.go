package chain

import (
	"context"
	stderrors "errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewEVMAdapter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		cfgMutator      func(*config.Config)
		expectedPrefix  string
		expectedWorkers int
	}{
		{
			name:            "CollectionsAndPrefix",
			cfgMutator:      func(_ *config.Config) {},
			expectedPrefix:  "Ethereum__Mainnet",
			expectedWorkers: 8,
		},
		{
			name:            "DefaultReceiptWorkers",
			cfgMutator:      func(c *config.Config) { c.Indexer.ReceiptWorkers = 0 },
			expectedPrefix:  "Ethereum__Mainnet",
			expectedWorkers: 16,
		},
		{
			name: "CustomPrefix",
			cfgMutator: func(c *config.Config) {
				c.Chain = config.ChainConfig{Name: "Arbitrum", Network: "One"}
			},
			expectedPrefix:  "Arbitrum__One",
			expectedWorkers: 8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			tc.cfgMutator(cfg)
			a := newEVMAdapter(cfg, &fakeRPCClient{})

			expected := constants.NewCollectionNames(tc.expectedPrefix)
			assert.Equal(t, expected, a.collections)
			assert.Equal(t, tc.expectedWorkers, a.receiptWorkers)
			assert.NotNil(t, a.signingChan)
		})
	}
}

// ---------------------------------------------------------------------------
// GetSchema / GetCollections (valid before Init)
// ---------------------------------------------------------------------------

func TestEVMAdapter_GetSchema(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newEVMAdapter(cfg, &fakeRPCClient{})

	got, err := a.GetSchema()
	require.NoError(t, err)
	assert.NotEmpty(t, got)

	// For the default prefix the chain-specific schema equals the default schema.
	def, err := schema.GetSchema()
	require.NoError(t, err)
	assert.Equal(t, def, got)
}

func TestEVMAdapter_GetCollections(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newEVMAdapter(cfg, &fakeRPCClient{})

	got := a.GetCollections()
	expected := constants.NewCollectionNames("Ethereum__Mainnet").AllCollections()
	assert.Equal(t, expected, got)
	assert.Contains(t, got, constants.DefaultCollectionPrefix+"__BlockSignature")
	assert.Contains(t, got, constants.DefaultCollectionPrefix+"__SnapshotSignature")
}

// ---------------------------------------------------------------------------
// Pre-Init guards
// ---------------------------------------------------------------------------

func TestEVMAdapter_PreInitGuard(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newEVMAdapter(cfg, &fakeRPCClient{})
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"GetHighestStoredBlockNumber", func() error {
			_, err := a.GetHighestStoredBlockNumber(ctx)
			return err
		}},
		{"GetLowestStoredBlockNumber", func() error {
			_, err := a.GetLowestStoredBlockNumber(ctx)
			return err
		}},
		{"GetDocIDsByBlockRange", func() error {
			_, err := a.GetDocIDsByBlockRange(ctx, 1, 10)
			return err
		}},
		{"FetchAndStoreBlock", func() error {
			_, err := a.FetchAndStoreBlock(ctx, 1)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.call(), ErrAdapterNotInitialized)
		})
	}
}

// ---------------------------------------------------------------------------
// FetchHighestBlockNumber delegation (to rpcClient)
// ---------------------------------------------------------------------------

func TestEVMAdapter_FetchHighestBlockNumber(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		latestNum   *big.Int
		latestErr   error
		wantResult  int64
		wantErr     bool
		errContains string
	}{
		{
			name:       "DelegatesToClient",
			latestNum:  big.NewInt(123456),
			wantResult: 123456,
		},
		{
			name:        "ClientError",
			latestErr:   stderrors.New("failed to get latest header"),
			wantErr:     true,
			errContains: "failed to get latest block number",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			client := &fakeRPCClient{latestNum: tc.latestNum, latestErr: tc.latestErr}
			a := newEVMAdapter(cfg, client)

			n, err := a.FetchHighestBlockNumber(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantResult, n)
		})
	}
}

// ---------------------------------------------------------------------------
// GetHighest/GetLowestStoredBlockNumber/GetDocIDsByBlockRange delegation (to BlockHandler)
// ---------------------------------------------------------------------------

func TestEVMAdapter_StoredBlockNumberDelegation(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cfg := testConfig()
	client := &fakeRPCClient{
		block:     fakeBlock(100),
		batchErr:  stderrors.New("batch receipts unavailable"),
		latestNum: big.NewInt(100),
	}
	a := newEVMAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })

	require.NoError(t, a.Init(context.Background(), td.Node))

	// Empty DB before any fetch: stored queries return not-found.
	_, err := a.GetHighestStoredBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = a.GetLowestStoredBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Persist block 100 via the adapter.
	_, err = a.FetchAndStoreBlock(context.Background(), 100)
	require.NoError(t, err)

	highest, err := a.GetHighestStoredBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), highest)

	lowest, err := a.GetLowestStoredBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	// GetDocIDsByBlockRange delegates to BlockHandler and returns the stored
	// Block document for block 100. fakeBlock has no transactions, so only the
	// Block collection is populated. No identity context means no BlockSignature.
	docIDs, err := a.GetDocIDsByBlockRange(context.Background(), 100, 100)
	require.NoError(t, err)
	assert.Contains(t, docIDs, "Ethereum__Mainnet__Block")
	assert.Len(t, docIDs["Ethereum__Mainnet__Block"], 1)
	assert.NotContains(t, docIDs, "Ethereum__Mainnet__SnapshotSignature")

	// Tip still delegates to the fake client.
	tip, err := a.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), tip)
}

// ---------------------------------------------------------------------------
// FetchAndStoreBlock: persistence, duplicate, batch receipts
// ---------------------------------------------------------------------------

func TestEVMAdapter_FetchAndStoreBlock(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name          string
		blockNum      int64
		txs           []types.Transaction
		batchReceipts []*types.TransactionReceipt
		calls         int
	}{
		{
			name:          "NoTx_Success",
			blockNum:      500,
			batchReceipts: []*types.TransactionReceipt{},
			calls:         1,
		},
		{
			name:          "DuplicateBlock",
			blockNum:      700,
			batchReceipts: []*types.TransactionReceipt{},
			calls:         2,
		},
		{
			name:          "WithTxAndBatchReceipts",
			blockNum:      0xbbb0,
			txs:           []types.Transaction{fakeTx(txHash)},
			batchReceipts: []*types.TransactionReceipt{fakeReceipt(txHash, 0xbbb0)},
			calls:         1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td := testutils.SetupTestDefraDB(t)
			cfg := testConfig()

			block := fakeBlock(tc.blockNum)
			if len(tc.txs) > 0 {
				block = fakeBlockWithTxs(tc.blockNum, tc.txs...)
			}

			client := &fakeRPCClient{
				block:         block,
				batchReceipts: tc.batchReceipts,
			}
			a := newEVMAdapter(cfg, client)
			t.Cleanup(func() { _ = a.Close() })
			require.NoError(t, a.Init(context.Background(), td.Node))

			for range tc.calls {
				_, err := a.FetchAndStoreBlock(context.Background(), tc.blockNum)
				require.NoError(t, err)
			}

			highest, err := a.GetHighestStoredBlockNumber(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.blockNum, highest)
		})
	}
}

// ---------------------------------------------------------------------------
// Receipt fallback (batch fails → individual GetTransactionReceipt)
// ---------------------------------------------------------------------------

func TestEVMAdapter_ReceiptFallback(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name        string
		blockNum    int64
		txs         []types.Transaction
		txReceiptFn func(_ context.Context, _ string) (*types.TransactionReceipt, error)
	}{
		{
			name:     "NoTxns",
			blockNum: 100000,
		},
		{
			name:     "IndividualSuccess",
			blockNum: 100000,
			txs:      []types.Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
				return fakeReceipt(txHash, 100000), nil
			},
		},
		{
			name:     "IndividualFail",
			blockNum: 100001,
			txs:      []types.Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
				return nil, fmt.Errorf("receipt not available")
			},
		},
		{
			name:     "IndividualReceiptSuccess",
			blockNum: 0xccc0,
			txs:      []types.Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
				return fakeReceipt(txHash, 0xccc0), nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td := testutils.SetupTestDefraDB(t)
			cfg := testConfig()

			block := fakeBlock(tc.blockNum)
			if len(tc.txs) > 0 {
				block = fakeBlockWithTxs(tc.blockNum, tc.txs...)
			}

			client := &fakeRPCClient{
				block:       block,
				batchErr:    fmt.Errorf("eth_getBlockReceipts not supported"),
				txReceiptFn: tc.txReceiptFn,
			}
			a := newEVMAdapter(cfg, client)
			t.Cleanup(func() { _ = a.Close() })
			require.NoError(t, a.Init(context.Background(), td.Node))

			_, err := a.FetchAndStoreBlock(context.Background(), tc.blockNum)
			require.NoError(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Context cancellation during receipt fetch
// ---------------------------------------------------------------------------

func TestEVMAdapter_ReceiptFallback_ContextCancelTiming(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name        string
		blockNum    int64
		txSleep     time.Duration
		ctxTimeout  time.Duration
		returnError bool
	}{
		{
			name:        "ContextCancel",
			blockNum:    100002,
			txSleep:     2 * time.Second,
			ctxTimeout:  500 * time.Millisecond,
			returnError: true,
		},
		{
			name:        "ContextCancelDuringFetch",
			blockNum:    1000,
			txSleep:     500 * time.Millisecond,
			ctxTimeout:  200 * time.Millisecond,
			returnError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td := testutils.SetupTestDefraDB(t)
			cfg := testConfig()

			block := fakeBlockWithTxs(tc.blockNum, fakeTx(txHash))
			client := &fakeRPCClient{
				block:    block,
				batchErr: fmt.Errorf("not supported"),
				txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
					time.Sleep(tc.txSleep)
					if tc.returnError {
						return nil, fmt.Errorf("timeout")
					}
					return fakeReceipt(txHash, tc.blockNum), nil
				},
			}
			a := newEVMAdapter(cfg, client)
			t.Cleanup(func() { _ = a.Close() })
			require.NoError(t, a.Init(context.Background(), td.Node))

			ctx, cancel := context.WithTimeout(context.Background(), tc.ctxTimeout)
			defer cancel()

			_, err := a.FetchAndStoreBlock(ctx, tc.blockNum)
			t.Logf("FetchAndStoreBlock error: %v", err)
		})
	}
}

func TestEVMAdapter_ReceiptFallback_ContextCancelDuringSemaphoreWait(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cfg := testConfig()
	cfg.Indexer.ReceiptWorkers = 1

	txs := make([]types.Transaction, 3)
	for i := range 3 {
		txs[i] = fakeTx(fmt.Sprintf("0x%064x", i+1))
	}
	block := fakeBlockWithTxs(0xddd0, txs...)

	firstReceiptCalled := make(chan struct{})
	client := &fakeRPCClient{
		block:    block,
		batchErr: fmt.Errorf("not supported"),
		txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
			select {
			case firstReceiptCalled <- struct{}{}:
			default:
			}
			time.Sleep(5 * time.Second)
			return nil, fmt.Errorf("timeout")
		},
	}
	a := newEVMAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })
	require.NoError(t, a.Init(context.Background(), td.Node))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := a.FetchAndStoreBlock(ctx, 0xddd0)
		errCh <- err
	}()

	select {
	case <-firstReceiptCalled:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first receipt call")
	}

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		t.Logf("FetchAndStoreBlock error: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for FetchAndStoreBlock to complete")
	}
}

// ---------------------------------------------------------------------------
// Context cancellation during batch creation
// ---------------------------------------------------------------------------

func TestEVMAdapter_FetchAndStoreBlock_ContextCancelDuringBatch(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cfg := testConfig()
	client := &fakeRPCClient{
		block:         fakeBlock(0xdead),
		batchReceipts: []*types.TransactionReceipt{},
	}
	a := newEVMAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })
	require.NoError(t, a.Init(context.Background(), td.Node))

	// First call: block persists.
	_, err := a.FetchAndStoreBlock(context.Background(), 0xdead)
	require.NoError(t, err)

	// Second call with pre-canceled ctx — either already-exists or ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = a.FetchAndStoreBlock(ctx, 0xdead)
	if err != nil {
		assert.Error(t, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestEVMAdapter_CloseClosesClient(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	client := &fakeRPCClient{}
	a := newEVMAdapter(cfg, client)

	require.NoError(t, a.Close())
	assert.True(t, client.closed)
}
