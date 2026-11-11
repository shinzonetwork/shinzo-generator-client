package indexer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// Dispatch waits while the finalized height is unknown, stops at it, and moves on as it rises.
func TestProcessBlocks_DispatchesFinalizedOnly(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	var finalized atomic.Int64
	finalized.Store(-1)
	var mu sync.Mutex
	var highestFetched int64
	fetched := func() int64 {
		mu.Lock()
		defer mu.Unlock()
		return highestFetched
	}
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			mu.Lock()
			highestFetched = max(highestFetched, height)
			mu.Unlock()
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchFinalizedBlockNumberFn: func(context.Context) (int64, error) {
			if f := finalized.Load(); f >= 0 {
				return f, nil
			}
			return 0, errors.New("finalized tag not served")
		},
	}
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, &mockReorgHandler{}, 2, 0)
	p.DispatchFinalizedOnly(0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	assert.Never(t, func() bool { return fetched() > 0 }, 2*FinalizedPollInterval, 50*time.Millisecond,
		"nothing is dispatched while the finalized height is unknown")

	finalized.Store(102)
	waitForCommits(t, rec, 3, errCh)
	assert.Never(t, func() bool { return fetched() > 102 }, 2*FinalizedPollInterval, 50*time.Millisecond)

	finalized.Store(104)
	waitForCommits(t, rec, 5, errCh)
	assert.Equal(t, []int64{100, 101, 102, 103, 104}, rec.snapshot())
	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
}
