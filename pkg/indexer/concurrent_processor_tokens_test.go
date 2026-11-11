package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// While a height's store is retried in place, no height DefaultWorkersAhead*workers or more above it
// is stored; once it succeeds, the later heights follow in order.
func TestProcessBlocks_RetriedStoreHoldsBackDispatch(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	const workers = 2
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}
	var (
		mu            sync.Mutex
		highestStored int64
		stores101     atomic.Int64
		released      atomic.Bool
	)
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			height := blockNumOfResult(result)
			if height == 101 {
				stores101.Add(1)
				if !released.Load() {
					return nil, errors.New("store failed")
				}
			}
			mu.Lock()
			highestStored = max(highestStored, height)
			mu.Unlock()
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, &mockReorgHandler{}, workers, 0)
	p.RetryFailedHeights()

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// The second attempt at 101 comes FailedHeightRetryDelay after the first, long enough for a
	// dispatch that is not held back to store far past it.
	require.Eventually(t, func() bool { return stores101.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	mu.Lock()
	assert.Less(t, highestStored, int64(101+workers*DefaultWorkersAhead))
	mu.Unlock()

	released.Store(true)
	waitForCommits(t, rec, 10, errCh)
	assert.Equal(t, []int64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109}, rec.snapshot()[:10])
	cancel()
	assert.ErrorIs(t, <-errCh, context.Canceled)
}

// A processor set to stop on reorgs stops the run with ErrReorgStopped and rolls nothing back,
// whether the reorg shows as a parent hash that breaks from the stored block or as a different
// block found stored after a failed store.
func TestReorgProcessor_StopOnReorgRollsNothingBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		fetcher func() *testutils.MockFetcher
		storer  *mockBlockStorer
		stored  map[int64]string
	}{
		{
			name: "parent hash breaks",
			fetcher: func() *testutils.MockFetcher {
				var orphanFetched atomic.Bool
				return &testutils.MockFetcher{
					FetchBlockFn: func(_ context.Context, height int64) (any, error) {
						if height == 100 && orphanFetched.CompareAndSwap(false, true) {
							return &servedBlock{height: 100, hash: orphanHash(100), parentHash: canonicalHash(99)}, nil
						}
						return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
					},
					FetchBlockHeaderFn: headerLookup(canonicalHashIndex(95, 115)),
				}
			},
			storer: &mockBlockStorer{},
			stored: map[int64]string{99: canonicalHash(99)},
		},
		{
			name:    "stale block after a failed store",
			fetcher: func() *testutils.MockFetcher { return canonicalOnlyFetcher(95, 115) },
			storer: &mockBlockStorer{
				storeFn: func(context.Context, chains.ConversionResult) (*defra.BlockCreationResult, error) {
					return nil, fmt.Errorf("store failed")
				},
			},
			stored: map[int64]string{99: canonicalHash(99), 100: orphanHash(100)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)
			reorg := &mockReorgHandler{storedHashes: tc.stored}
			p := NewConcurrentBlockProcessor(tc.fetcher(), chainConverter(), tc.storer, reorg, 1, 0)
			p.StopOnReorg()

			_, errCh := runProcessor(p, 100, nil)
			select {
			case err := <-errCh:
				require.ErrorIs(t, err, ErrReorgStopped)
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for the reorg to stop the run")
			}
			assert.Empty(t, reorg.calls())
		})
	}
}
