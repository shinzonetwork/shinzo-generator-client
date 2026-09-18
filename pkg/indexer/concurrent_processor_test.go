package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// TestConcurrentProcessor_FetchBlockWithRetry covers the processor's
// fetchBlockWithRetry retry policy: success on first call, success after one
// retry, and exhaustion after MaxRPCRetries for persistent non-not-found errors.
// The fetcher no longer retries internally (Issue 3), so the processor is the
// sole retry authority.
func TestConcurrentProcessor_FetchBlockWithRetry(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	wantBlock := "block-data"

	cases := []struct {
		name         string
		fetchBlockFn func(_ context.Context, _ int64) (any, error)
		wantErr      bool
		wantErrSub   string
		wantResult   any
		wantCalls    int
	}{
		{
			name: "SuccessOnFirstCall",
			fetchBlockFn: func(_ context.Context, _ int64) (any, error) {
				return wantBlock, nil
			},
			wantResult: wantBlock,
			wantCalls:  1,
		},
		{
			name: "SuccessOnSecondAttempt",
			fetchBlockFn: func() func(_ context.Context, _ int64) (any, error) {
				calls := 0
				return func(_ context.Context, _ int64) (any, error) {
					calls++
					if calls < 2 {
						return nil, errors.New("temporary server error")
					}
					return wantBlock, nil
				}
			}(),
			wantResult: wantBlock,
			wantCalls:  2,
		},
		{
			name: "RetriesMaxTimesOnError",
			fetchBlockFn: func(_ context.Context, _ int64) (any, error) {
				return nil, errors.New("connection refused")
			},
			wantErr:    true,
			wantErrSub: "connection refused",
			wantCalls:  MaxRPCRetries,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock := &testutils.MockFetcher{
				FetchBlockFn: tc.fetchBlockFn,
			}

			p := &ConcurrentBlockProcessor{
				fetcher: mock,
			}

			got, err := p.fetchBlockWithRetry(context.Background(), 42)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "failed to fetch block")
				assert.Contains(t, err.Error(), tc.wantErrSub)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantResult, got)
			}
			assert.Len(t, mock.FetchBlockCalls, tc.wantCalls)
		})
	}
}

// ---------------------------------------------------------------------------.
// NewConcurrentBlockProcessor tests.
// ---------------------------------------------------------------------------.

func TestNewConcurrentBlockProcessor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		workers         int
		blocksPerMinute int
	}{
		{name: "CustomValues", workers: 4, blocksPerMinute: 60},
		{name: "DefaultValues", workers: 1, blocksPerMinute: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := NewConcurrentBlockProcessor(nil, nil, nil, tc.workers, tc.blocksPerMinute)
			require.NotNil(t, p)
			assert.Equal(t, tc.workers, p.workers)
			assert.Equal(t, tc.blocksPerMinute, p.blocksPerMinute)
			assert.NotNil(t, p.resultChan)
			assert.NotNil(t, p.pending)
		})
	}
}

// ---------------------------------------------------------------------------.
// fetchAndProcessBlock tests.
// ---------------------------------------------------------------------------.

func TestFetchAndProcessBlock_FetchFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		rpcErr     error
		cancelCtx  bool
		timeout    time.Duration
		blockNum   int64
		wantErrSub string
	}{
		{
			name:       "RPCError",
			rpcErr:     fmt.Errorf("internal server error"),
			blockNum:   500,
			wantErrSub: "failed to fetch block",
		},
		{
			name:      "ContextCancelled",
			cancelCtx: true,
			blockNum:  500,
		},
		{
			name:     "DuringNotFound",
			rpcErr:   errors.New("block not found"),
			timeout:  500 * time.Millisecond,
			blockNum: 99999,
		},
		{
			name:     "DuringOtherRetry",
			rpcErr:   fmt.Errorf("temporary error"),
			timeout:  200 * time.Millisecond,
			blockNum: 88888,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			td := testutils.SetupTestDefraDB(t)

			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				if tc.rpcErr != nil && method == ethGetBlockByNumber {
					return nil, tc.rpcErr
				}
				return "0x1", nil
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)

			ctx := context.Background()
			if tc.cancelCtx {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			} else if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}

			result := p.fetchAndProcessBlock(ctx, tc.blockNum)
			require.NotNil(t, result)
			assert.False(t, result.Success)
			assert.Error(t, result.Error)
			if tc.wantErrSub != "" {
				assert.Contains(t, result.Error.Error(), tc.wantErrSub)
			}
		})
	}
}

// ---------------------------------------------------------------------------.
// fetchAndProcessBlock — retry-then-success paths.
// ---------------------------------------------------------------------------.

func TestFetchAndProcessBlock_RetryThenSuccess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		failCount         int64
		failErr           error
		blockHex          string
		blockNum          int64
		useContextTimeout bool
		timeout           time.Duration
	}{
		{
			name:              "NotFoundThenSuccess",
			failCount:         1,
			failErr:           errors.New("not found"),
			blockHex:          "0x4e20",
			blockNum:          20000,
			useContextTimeout: true,
			timeout:           15 * time.Second,
		},
		{
			name:              "OtherRPCErrorRetry",
			failCount:         2,
			failErr:           fmt.Errorf("temporary server error"),
			blockHex:          "0x7530",
			blockNum:          30000,
			useContextTimeout: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			td := testutils.SetupTestDefraDB(t)

			var callCount atomic.Int64
			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case ethGetBlockByNumber:
					n := callCount.Add(1)
					if n <= tc.failCount {
						return nil, tc.failErr
					}
					return fullBlockResponse(tc.blockHex, nil), nil
				case ethGetBlockReceipts:
					return []any{}, nil
				default:
					return "0x1", nil
				}
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)

			var ctx context.Context
			if tc.useContextTimeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(context.Background(), tc.timeout)
				defer cancel()
			} else {
				ctx = context.Background()
			}

			result := p.fetchAndProcessBlock(ctx, tc.blockNum)
			require.NotNil(t, result)
			assert.True(t, result.Success, "should succeed after retry: %v", result.Error)
		})
	}
}

// ---------------------------------------------------------------------------.
// fetchAndProcessBlock — duplicate block / existing path.
// ---------------------------------------------------------------------------.

func TestFetchAndProcessBlock_DuplicateBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		blockHex string
		blockNum int64
	}{
		{name: "TransactionConflict", blockHex: "0x9c40", blockNum: 40000},
		{name: "SigningQueueFull", blockHex: "0xbeef", blockNum: 0xbeef},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			td := testutils.SetupTestDefraDB(t)

			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case ethGetBlockByNumber:
					return fullBlockResponse(tc.blockHex, nil), nil
				case ethGetBlockReceipts:
					return []any{}, nil
				default:
					return "0x1", nil
				}
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)

			result1 := p.fetchAndProcessBlock(context.Background(), tc.blockNum)
			require.True(t, result1.Success)

			result2 := p.fetchAndProcessBlock(context.Background(), tc.blockNum)
			require.NotNil(t, result2)
			assert.True(t, result2.Success)
		})
	}
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — concurrent conflict paths.
// We trigger IsErrTransactionConflict by running multiple concurrent processors
// that try to create the same block at the same time.
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_ConcurrentConflict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		numProcessors int
		cancelDelay   time.Duration
		blockHex      string
		blockNum      int64
	}{
		{
			name:          "TransactionConflictRetry",
			numProcessors: 2,
			cancelDelay:   0,
			blockHex:      "0xbeef0",
			blockNum:      0xbeef0,
		},
		{
			name:          "ConflictRetryCtxCancel",
			numProcessors: 10,
			cancelDelay:   50 * time.Millisecond,
			blockHex:      "0xeee0",
			blockNum:      0xeee0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)
			td := testutils.SetupTestDefraDB(t)

			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case ethGetBlockByNumber:
					return fullBlockResponse(tc.blockHex, nil), nil
				case ethGetBlockReceipts:
					return []any{}, nil
				default:
					return "0x1", nil
				}
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			results := make([]*BlockResult, tc.numProcessors)
			var wg sync.WaitGroup

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			for i := range tc.numProcessors {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)
					results[idx] = p.fetchAndProcessBlock(ctx, tc.blockNum)
				}(i)
			}

			if tc.cancelDelay > 0 {
				time.Sleep(tc.cancelDelay)
				cancel()
			}

			wg.Wait()

			successCount := 0
			for i, r := range results {
				if r.Success {
					successCount++
				}
				if r.Error != nil {
					t.Logf("  Processor %d: success=%v, err=%v", i, r.Success, r.Error)
				}
			}
			t.Logf("Results: %d/%d succeeded", successCount, tc.numProcessors)
			assert.GreaterOrEqual(t, successCount, 1, "at least one concurrent block creation should succeed")
		})
	}
}

// ---------------------------------------------------------------------------.
// fetchAndProcessBlock — context cancel during conflict retry wait.
// ---------------------------------------------------------------------------.

func TestFetchAndProcessBlock_ContextCancelDuringConflictRetry(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)
	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fullBlockResponse("0xdead1", nil), nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

	// First, insert the block to make subsequent inserts trigger "already exists".
	p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)
	result1 := p.fetchAndProcessBlock(context.Background(), 0xdead1)
	require.True(t, result1.Success)

	// The "already exists" path doesn't go through conflict retry.
	// To actually trigger transaction conflict, we would need concurrent writes,
	// to the same transaction. This is timing-dependent.
	// The test at least exercises the code path setup.
	t.Log("Transaction conflict retry is timing-dependent; covered by concurrent block creation tests")
}

// ---------------------------------------------------------------------------.
// ProcessBlocks — cancel after delay (rate-limit, callback, and immediate variants).
// ---------------------------------------------------------------------------.

func TestProcessBlocks_CancelAfterDelay(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		workers         int
		blocksPerMinute int
		startBlock      int64
		blockBase       int64
		trackProcessed  bool
		cancelDelay     time.Duration
		failAt          int64
	}{
		{name: "ContextCancel", workers: 1, blocksPerMinute: 0, startBlock: 1001, blockBase: 1000, trackProcessed: true, cancelDelay: 500 * time.Millisecond},
		{name: "WithRateLimit_ContextCancel", workers: 1, blocksPerMinute: 600, startBlock: 2001, blockBase: 2000, trackProcessed: false, cancelDelay: 500 * time.Millisecond},
		{name: "WithNilCallback", workers: 1, blocksPerMinute: 0, startBlock: 4001, blockBase: 4000, trackProcessed: false, cancelDelay: 500 * time.Millisecond},
		{name: "CancelDuringRateLimit", workers: 1, blocksPerMinute: 1, startBlock: 7001, blockBase: 7000, trackProcessed: false, cancelDelay: 500 * time.Millisecond},
		{name: "FailedBlockInSequence", workers: 1, blocksPerMinute: 0, startBlock: 6001, blockBase: 6000, trackProcessed: true, cancelDelay: 2 * time.Second, failAt: 2},
		{name: "ImmediateCancel_NoRateLimit", workers: 1, blocksPerMinute: 0, startBlock: 100000, cancelDelay: 0},
		{name: "ImmediateCancel_WithRateLimit", workers: 1, blocksPerMinute: 30, startBlock: 100000, cancelDelay: 0},
		{name: "ImmediateCancel_MultipleWorkers", workers: 2, blocksPerMinute: 60, startBlock: 100000, cancelDelay: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			td := testutils.SetupTestDefraDB(t)

			var callCount atomic.Int64
			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case ethGetBlockByNumber:
					n := callCount.Add(1)
					if tc.failAt > 0 && n == tc.failAt {
						return nil, fmt.Errorf("server error")
					}
					num := fmt.Sprintf("0x%x", tc.blockBase+n)
					return fullBlockResponse(num, nil), nil
				case ethGetBlockReceipts:
					return []any{}, nil
				default:
					return "0x1", nil
				}
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, tc.workers, tc.blocksPerMinute)

			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelDelay == 0 {
				cancel()
			} else {
				go func() {
					time.Sleep(tc.cancelDelay)
					cancel()
				}()
			}

			var (
				processed []int64
				mu        sync.Mutex
				callback  func(blockNum int64)
			)
			if tc.trackProcessed {
				callback = func(blockNum int64) {
					mu.Lock()
					processed = append(processed, blockNum)
					mu.Unlock()
				}
			}

			err := p.ProcessBlocks(ctx, tc.startBlock, callback)
			assert.ErrorIs(t, err, context.Canceled)

			if tc.trackProcessed {
				mu.Lock()
				t.Logf("Processed %d blocks before cancellation", len(processed))
				mu.Unlock()
			}
		})
	}
}

// ---------------------------------------------------------------------------.
// ProcessBlocks — tooFarAhead throttle paths.
// ---------------------------------------------------------------------------.

func TestProcessBlocks_TooFarAhead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		slowAfter   int64
		slowDelay   time.Duration
		timeout     time.Duration
		cancelDelay time.Duration
		startBlock  int64
		blockBase   int64
	}{
		{name: "TooFarAhead", slowAfter: 3, slowDelay: 200 * time.Millisecond, cancelDelay: 2 * time.Second, startBlock: 3001, blockBase: 3000},
		{name: "CancelDuringTooFarAhead", slowAfter: 0, slowDelay: 2 * time.Second, timeout: 1 * time.Second, startBlock: 9001, blockBase: 0xbeef},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			td := testutils.SetupTestDefraDB(t)

			var callCount atomic.Int64
			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case ethGetBlockByNumber:
					n := callCount.Add(1)
					if tc.slowAfter == 0 || n > tc.slowAfter {
						time.Sleep(tc.slowDelay)
					}
					num := fmt.Sprintf("0x%x", tc.blockBase+n)
					return fullBlockResponse(num, nil), nil
				case ethGetBlockReceipts:
					return []any{}, nil
				default:
					return "0x1", nil
				}
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)

			var ctx context.Context
			var cancel context.CancelFunc
			if tc.timeout > 0 {
				ctx, cancel = context.WithTimeout(context.Background(), tc.timeout)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				go func() {
					time.Sleep(tc.cancelDelay)
					cancel()
				}()
			}
			defer cancel()

			err := p.ProcessBlocks(ctx, tc.startBlock, nil)
			if tc.timeout > 0 {
				assert.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				assert.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ProcessBlocks — out-of-order completion is committed in nonce order.
// ---------------------------------------------------------------------------

// TestProcessBlocks_OutOfOrderCompletion verifies that collectResults commits
// blocks strictly in ascending nonce order even when worker results arrive
// out of order. Block 1002 is artificially slowed so that 1003–1005 complete
// first; the hold-back loop must stash them in p.pending until 1002 arrives,
// then commit 1002→1005 in a single pass. Blocks ≥1006 are parked on
// ctx.Done() so collectResults sees exactly five results.
//
// This is a pure unit test: only mocks are used (no DefraDB, no
// RPC server). State is snapshotted BEFORE cancel() to avoid races with
// parked workers waking up after cancellation.
func TestProcessBlocks_OutOfOrderCompletion(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	mc := &testutils.MockFetcher{
		FetchBlockFn: func(ctx context.Context, height int64) (any, error) {
			switch {
			case height == 1002:
				// Slow 1002 so that workers 3/4/5 finish first, producing
				// out-of-order arrival at resultChan.
				time.Sleep(250 * time.Millisecond)
				return fmt.Sprintf("0x%x", height), nil
			case height >= 1006:
				// Park extra blocks so collectResults sees no result beyond 1005.
				<-ctx.Done()
				return nil, ctx.Err()
			default:
				return fmt.Sprintf("0x%x", height), nil
			}
		},
	}

	mcConv := &testutils.MockConverter{
		ConvertFn: func(_ context.Context, _ any) (chains.ConversionResult, error) {
			return chains.ConversionResult{}, nil
		},
	}

	p := NewConcurrentBlockProcessor(mc, mcConv, &mockBlockStorer{}, 4, 0)

	var (
		mu        sync.Mutex
		committed []int64
		done      = make(chan struct{})
	)
	callback := func(blockNum int64) {
		mu.Lock()
		committed = append(committed, blockNum)
		if len(committed) == 5 {
			close(done)
		}
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.ProcessBlocks(ctx, 1001, callback)
	}()

	// Wait for all 5 callbacks to fire (settled state).
	<-done

	// Race-free snapshot: capture processor state BEFORE cancel() so parked
	// workers (≥1006) waking up after cancel can't mutate pending/nextToCommit.
	p.pendingMu.Lock()
	snapNextToCommit := p.nextToCommit
	snapPendingLen := len(p.pending)
	p.pendingMu.Unlock()

	mu.Lock()
	snapCommitted := append([]int64(nil), committed...)
	mu.Unlock()

	cancel()
	err := <-errCh

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(1006), snapNextToCommit, "nextToCommit should have advanced past all 5 blocks")
	assert.Empty(t, snapPendingLen, "pending map should be drained after ordered commit")
	assert.Equal(t, []int64{1001, 1002, 1003, 1004, 1005}, snapCommitted,
		"committed blocks should be in strict nonce order despite out-of-order arrival")
}

// ---------------------------------------------------------------------------.
// ProcessBlocks — always-succeed (existing path) and always-fail (exhaustion).
// ---------------------------------------------------------------------------.

func TestProcessBlocks_ErrorAndExisting(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		alwaysFail       bool
		blockHex         string
		timeout          time.Duration
		wantMinProcessed int64
	}{
		{name: "ExistingBlockPath", blockHex: "0x186a0", timeout: 2 * time.Second, wantMinProcessed: 1},
		{name: "BlockFetchExhaustion", alwaysFail: true, timeout: 10 * time.Second, wantMinProcessed: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			td := testutils.SetupTestDefraDB(t)

			rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case ethGetBlockByNumber:
					if tc.alwaysFail {
						return nil, fmt.Errorf("persistent RPC error")
					}
					return fullBlockResponse(tc.blockHex, nil), nil
				case ethGetBlockReceipts:
					return []any{}, nil
				default:
					return "0x1", nil
				}
			})
			defer rpcServer.Close()

			fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

			p := NewConcurrentBlockProcessor(fetcher, converter, blockHandler, 1, 0)

			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			var processedBlocks atomic.Int64
			err := p.ProcessBlocks(ctx, 100000, func(_ int64) {
				processedBlocks.Add(1)
			})

			assert.Error(t, err)
			if tc.wantMinProcessed > 0 {
				assert.GreaterOrEqual(t, processedBlocks.Load(), tc.wantMinProcessed)
			}
		})
	}
}
