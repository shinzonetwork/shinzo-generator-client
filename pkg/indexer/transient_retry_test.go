package indexer

// Tests for retrying transient RPC errors: the block fetch retries an
// unreachable endpoint without skipping the block, and startup optionally
// waits for the chain RPC instead of exiting.

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

var errDialRefused = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED} //nolint:gochecknoglobals // read-only test fixture

func TestTransientRetryDelay(t *testing.T) {
	t.Parallel()
	assert.Equal(t, RPCErrorRetryBaseDelay, transientRetryDelay(1))
	assert.Equal(t, 2*RPCErrorRetryBaseDelay, transientRetryDelay(2))
	assert.Equal(t, 4*RPCErrorRetryBaseDelay, transientRetryDelay(3))
	assert.Equal(t, MaxTransientRetryDelay, transientRetryDelay(8), "capped")
	assert.Equal(t, MaxTransientRetryDelay, transientRetryDelay(1000), "no overflow on long outages")
}

// TestFetchBlockWithRetry_TransientErrorsDoNotExhaust proves an unreachable
// endpoint is retried past MaxRPCRetries: the block is returned, not skipped.
func TestFetchBlockWithRetry_TransientErrorsDoNotExhaust(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	failures := MaxRPCRetries + 1
	calls := 0
	mock := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, _ int64) (any, error) {
			calls++
			if calls <= failures {
				return nil, errDialRefused
			}
			return "block-data", nil
		},
	}
	p := &ConcurrentBlockProcessor{fetcher: mock}

	got, err := p.fetchBlockWithRetry(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "block-data", got)
	assert.Len(t, mock.FetchBlockCalls, failures+1)
}

func TestFetchBlockWithRetry_TransientRetryStopsOnCancel(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	mock := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, _ int64) (any, error) { return nil, errDialRefused },
	}
	p := &ConcurrentBlockProcessor{fetcher: mock}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := p.fetchBlockWithRetry(ctx, 42)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestFetchChainTip(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	tipAfter := func(failures int, failWith error) *testutils.MockFetcher {
		calls := 0
		return &testutils.MockFetcher{
			FetchHighestBlockNumberFn: func(_ context.Context) (int64, error) {
				calls++
				if calls <= failures {
					return 0, failWith
				}
				return 6355209, nil
			},
		}
	}

	t.Run("waits for an unreachable RPC", func(t *testing.T) {
		t.Parallel()
		mock := tipAfter(1, errDialRefused)
		i := &ChainIndexer{fetcher: mock}
		tip, err := i.fetchChainTip(context.Background(), 10*time.Second)
		require.NoError(t, err)
		assert.EqualValues(t, 6355209, tip)
		assert.Equal(t, 2, mock.FetchHighestBlockNumberCalls)
	})

	t.Run("no wait fails fast", func(t *testing.T) {
		t.Parallel()
		mock := tipAfter(1, errDialRefused)
		i := &ChainIndexer{fetcher: mock}
		_, err := i.fetchChainTip(context.Background(), 0)
		require.ErrorIs(t, err, syscall.ECONNREFUSED)
		assert.Equal(t, 1, mock.FetchHighestBlockNumberCalls)
	})

	t.Run("non-transient error is not retried", func(t *testing.T) {
		t.Parallel()
		mock := tipAfter(1, errors.New("invalid response"))
		i := &ChainIndexer{fetcher: mock}
		_, err := i.fetchChainTip(context.Background(), 10*time.Second)
		require.Error(t, err)
		assert.Equal(t, 1, mock.FetchHighestBlockNumberCalls)
	})

	t.Run("gives up after the wait", func(t *testing.T) {
		t.Parallel()
		mock := tipAfter(1000, errDialRefused)
		i := &ChainIndexer{fetcher: mock}
		_, err := i.fetchChainTip(context.Background(), 1200*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "still unreachable")
	})
}
