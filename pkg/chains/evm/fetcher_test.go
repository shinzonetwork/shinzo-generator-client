package evm

import (
	"context"
	stderrors "errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewFetcher(t *testing.T) {
	t.Parallel()
	client := &fakeRPCClient{}
	f := NewFetcher(client, 8)
	assert.NotNil(t, f.client)
	assert.Equal(t, 8, f.receiptWorkers)
}

// ---------------------------------------------------------------------------
// FetchHighestBlockNumber (moved from adapter_test.go)
// ---------------------------------------------------------------------------

func TestFetcher_FetchHighestBlockNumber(t *testing.T) {
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
			client := &fakeRPCClient{latestNum: tc.latestNum, latestErr: tc.latestErr}
			f := NewFetcher(client, 8)

			n, err := f.FetchHighestBlockNumber(context.Background())
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
// FetchBlock: happy path with batch receipts
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_BatchSuccess(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blockNum := int64(12345)
	block := fakeBlockWithTxs(blockNum, fakeTx(txHash))
	receipt := fakeReceipt(txHash, blockNum)

	client := &fakeRPCClient{
		block:         block,
		batchReceipts: []*types.TransactionReceipt{receipt},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), blockNum)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.Equal(t, block, bundle.Block)
	assert.Len(t, bundle.Transactions, 1)
	assert.Equal(t, txHash, bundle.Transactions[0].Hash)
	assert.Len(t, bundle.Receipts, 1)
	assert.Equal(t, receipt, bundle.Receipts[0])
}

func TestFetcher_FetchBlock_NoTransactions(t *testing.T) {
	t.Parallel()

	blockNum := int64(100)
	block := fakeBlock(blockNum)

	client := &fakeRPCClient{
		block:         block,
		batchReceipts: []*types.TransactionReceipt{},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), blockNum)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.Equal(t, block, bundle.Block)
	assert.Empty(t, bundle.Transactions)
	assert.Empty(t, bundle.Receipts)
}

// ---------------------------------------------------------------------------
// FetchBlock: block not found (no retry)
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_BlockNotFound(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*types.Block, error) {
			calls++
			return nil, stderrors.New("block not found")
		},
	}
	f := NewFetcher(client, 8)

	_, err := f.FetchBlock(context.Background(), 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.Equal(t, 1, calls, "should not retry on not-found errors")
}

// ---------------------------------------------------------------------------
// FetchBlock: RPC retry (success on 2nd attempt)
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_RPCRetrySuccess(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*types.Block, error) {
			calls++
			if calls < 2 {
				return nil, stderrors.New("connection reset")
			}
			return fakeBlock(999), nil
		},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), 999)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.NotNil(t, bundle.Block)
	assert.Equal(t, 2, calls, "should succeed on 2nd attempt")
}

// ---------------------------------------------------------------------------
// FetchBlock: RPC retry exhausted
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_RPCRetryExhausted(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*types.Block, error) {
			calls++
			return nil, stderrors.New("connection refused")
		},
	}
	f := NewFetcher(client, 8)

	_, err := f.FetchBlock(context.Background(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch block")
	assert.Contains(t, err.Error(), "connection refused")
	assert.Equal(t, maxRPCRetries, calls, "should exhaust all retries")
}

// ---------------------------------------------------------------------------
// FetchBlock: pre-canceled context
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_ContextAlreadyCanceled(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*types.Block, error) {
			calls++
			return fakeBlock(1), nil
		},
	}
	f := NewFetcher(client, 8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.FetchBlock(ctx, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, calls, "should not call RPC with pre-canceled context")
}

// ---------------------------------------------------------------------------
// Receipt fallback (moved from adapter_test.go — now pure unit tests)
// ---------------------------------------------------------------------------

func TestFetcher_ReceiptFallback(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name         string
		blockNum     int64
		txs          []types.Transaction
		txReceiptFn  func(_ context.Context, _ string) (*types.TransactionReceipt, error)
		wantReceipts int
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
			wantReceipts: 1,
		},
		{
			name:     "IndividualFail",
			blockNum: 100001,
			txs:      []types.Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
				return nil, fmt.Errorf("receipt not available")
			},
			wantReceipts: 0,
		},
		{
			name:     "IndividualReceiptSuccess",
			blockNum: 0xccc0,
			txs:      []types.Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*types.TransactionReceipt, error) {
				return fakeReceipt(txHash, 0xccc0), nil
			},
			wantReceipts: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			block := fakeBlock(tc.blockNum)
			if len(tc.txs) > 0 {
				block = fakeBlockWithTxs(tc.blockNum, tc.txs...)
			}

			client := &fakeRPCClient{
				block:       block,
				batchErr:    fmt.Errorf("eth_getBlockReceipts not supported"),
				txReceiptFn: tc.txReceiptFn,
			}
			f := NewFetcher(client, 8)

			raw, err := f.FetchBlock(context.Background(), tc.blockNum)
			require.NoError(t, err)

			bundle, ok := raw.(*BlockBundle)
			require.True(t, ok)
			assert.Equal(t, block, bundle.Block)
			assert.Len(t, bundle.Receipts, tc.wantReceipts)
		})
	}
}

// ---------------------------------------------------------------------------
// Context cancellation during receipt fetch (moved from adapter_test.go)
// ---------------------------------------------------------------------------

func TestFetcher_ReceiptFallback_ContextCancelTiming(t *testing.T) {
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
			f := NewFetcher(client, 8)

			ctx, cancel := context.WithTimeout(context.Background(), tc.ctxTimeout)
			defer cancel()

			_, err := f.FetchBlock(ctx, tc.blockNum)
			t.Logf("FetchBlock error: %v", err)
		})
	}
}

func TestFetcher_ReceiptFallback_ContextCancelDuringSemaphoreWait(t *testing.T) {
	t.Parallel()

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
	f := NewFetcher(client, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := f.FetchBlock(ctx, 0xddd0)
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
		t.Logf("FetchBlock error: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for FetchBlock to complete")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestFetcher_Close(t *testing.T) {
	t.Parallel()

	client := &fakeRPCClient{}
	f := NewFetcher(client, 8)

	require.NoError(t, f.Close())
	assert.True(t, client.closed)
}

func TestFetcher_Close_NilClient(t *testing.T) {
	t.Parallel()

	f := &Fetcher{}
	require.NoError(t, f.Close())
}
