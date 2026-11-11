package indexer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
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

// A height whose convert or store fails is retried until it succeeds and committed in order; each
// fails twice here, so a single retry is not enough.
func TestProcessBlocks_RetriesFailedHeights(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}
	var converts101, stores102 atomic.Int64
	converter := chainConverter()
	convert := converter.ConvertFn
	converter.ConvertFn = func(ctx context.Context, raw any) (chains.ConversionResult, error) {
		if raw.(*servedBlock).height == 101 && converts101.Add(1) <= 2 {
			return chains.ConversionResult{}, errors.New("convert failed")
		}
		return convert(ctx, raw)
	}
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			if extractBlockHash(result.Groups) == canonicalHash(102) && stores102.Add(1) <= 2 {
				return nil, errors.New("store failed")
			}
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}
	p := NewConcurrentBlockProcessor(fetcher, converter, storer, &mockReorgHandler{}, 2, 0)
	p.RetryFailedHeights()

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 4, errCh)
	assert.Equal(t, []int64{100, 101, 102, 103}, rec.snapshot()[:4])
	assert.Equal(t, int64(3), converts101.Load())
	assert.Equal(t, int64(3), stores102.Load())
	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
}
