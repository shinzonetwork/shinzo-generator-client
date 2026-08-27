package indexer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
