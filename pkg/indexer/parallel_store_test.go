package indexer

// parallel_store_test.go covers the parallel store pool: the collector never
// stores, the store dispatch is gated only by the pop-order continuity check
// against fetched hashes, completions apply in height order through the
// reorder buffer, the in-flight range drains before every walk-back, and the
// pool-failure rules mirror the serial commit path (refusal fatal, stale
// orphan at a failed height opens the episode, an empty height warns the
// hole and the run continues). Pure unit tests - no DefraDB, no RPC server;
// helpers (mockBlockStorer, runProcessor, waitForCommits, commitRecorder)
// are reused from the other test files.

import (
	"context"
	"fmt"
	"strings"
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

// parkedStorer is a scripted storer that publishes stored hashes on success
// (the way a real store makes a height visible to GetStoredBlockHash),
// optionally parks one height's store until released, and tracks in-flight
// store concurrency.
type parkedStorer struct {
	reorg        *mockReorgHandler
	failHeights  map[int64]int64 // height → the number of initial failures
	storeBlocked int64           // the height whose store blocks on released

	released   chan struct{}
	releasedC  chan struct{} // closed the first time the parked store is released
	inFlight   atomic.Int64
	storeCalls atomic.Int64
	once       sync.Once
}

func (s *parkedStorer) Store(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
	num := blockNumOfResult(result)
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	s.storeCalls.Add(1)

	if s.failHeights != nil && s.failHeights[num] > 0 {
		s.failHeights[num]--
		return nil, fmt.Errorf("store exploded")
	}

	if s.storeBlocked == num {
		<-s.released
		s.once.Do(func() { close(s.releasedC) })
	}
	if s.reorg != nil {
		s.reorg.mu.Lock()
		s.reorg.storedHashes[num] = extractBlockHash(result.Groups)
		s.reorg.mu.Unlock()
	}
	return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
}

func (s *parkedStorer) SignExisting(_ context.Context, _ chains.ConversionResult, _ string, _ int64) (string, error) {
	return "mock-sig-id", nil
}

// inFlightWatcher wraps a ReorgHandler and flags every stored-hash query or
// rollback that ran while pool stores were still in flight - the walk-back
// and the rollback must observe a quiescent DB.
type inFlightWatcher struct {
	defra.ReorgHandler

	inFlight   func() int64
	queries    atomic.Int64
	violations atomic.Int64
}

func newInFlightWatcher(inner defra.ReorgHandler, inFlight func() int64) *inFlightWatcher {
	return &inFlightWatcher{ReorgHandler: inner, inFlight: inFlight}
}

func (w *inFlightWatcher) GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error) {
	w.queries.Add(1)
	if w.inFlight() > 0 {
		w.violations.Add(1)
	}
	return w.ReorgHandler.GetStoredBlockHash(ctx, blockNumber)
}

func (w *inFlightWatcher) RollbackBlocks(ctx context.Context, from, to int64, chainHashes map[int64]string) error {
	if w.inFlight() > 0 {
		w.violations.Add(1)
	}
	return w.ReorgHandler.RollbackBlocks(ctx, from, to, chainHashes)
}

// waitParkedReleased blocks until the parked store has been released, or
// fails the test on timeout.
func waitParkedReleased(t *testing.T, s *parkedStorer) {
	t.Helper()
	select {
	case <-s.releasedC:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the parked store to be released")
	}
}

// canonicalOnlyFetcher serves the canonical chain for every height and keeps
// the by-hash header index the walk-back descends through.
func canonicalOnlyFetcher(lo, hi int64) *testutils.MockFetcher {
	byHash := canonicalHashIndex(lo, hi)
	return &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockHeaderFn: headerLookup(byHash),
	}
}

// TestParallelStores_OverlapWithOrderedCallbacks is the overlap proof: two
// checked stores block on a mutual barrier so they demonstrably run at the
// same time on the bounded pool, while the ordered apply still fires
// onBlockProcessed in strict height order.
func TestParallelStores_OverlapWithOrderedCallbacks(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	// A mutual barrier: the first two store calls wait for each other and
	// then are released together, so both are inside Store at the same
	// moment.
	var (
		entered  atomic.Int64
		gate     = make(chan struct{})
		gateOnce sync.Once
	)
	storeCalls := atomic.Int64{}

	overlapStorer := &mockBlockStorer{
		storeFn: func(_ context.Context, _ chains.ConversionResult) (*defra.BlockCreationResult, error) {
			if storeCalls.Add(1) <= 2 && entered.Add(1) <= 2 {
				if entered.Load() == 2 {
					gateOnce.Do(func() { close(gate) })
				}
				select {
				case <-gate:
				case <-time.After(5 * time.Second):
					t.Log("barrier timeout")
				}
				// Hold both inside until the overlap was observed.
				time.Sleep(50 * time.Millisecond)
			}
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	rec := &commitRecorder{}
	reorg := &mockReorgHandler{}
	p := NewConcurrentBlockProcessor(canonicalOnlyFetcher(95, 115), chainConverter(), overlapStorer, reorg, 2, 0)

	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 4, errCh)
	cancel()
	<-errCh

	assert.GreaterOrEqual(t, storeCalls.Load(), int64(2), "the pool stored the checked blocks")
	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	for i := 1; i < len(got); i++ {
		require.Greater(t, got[i], got[i-1],
			"onBlockProcessed fired out of height order after an overlapping parallel store")
	}
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4], "the ordered apply starts at the run's start heights")
	assert.Empty(t, reorg.calls(), "a chain-consistent run detects no reorg")
}

// TestParallelStores_StaleOrphanAtFailedStoreOpensEpisode pins the pool-side
// failure handler: a transient store failure at 100 with a stale orphan left
// stored there is verified against the failed item's fetched hash and opens
// the stale-pinned episode. Completions for stores dispatched past the
// failed height (they land before the episode could hold them back) apply
// suppressed, so their callbacks only fire after the restart re-commits
// those heights in order.
func TestParallelStores_StaleOrphanAtFailedStoreOpensEpisode(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	// The stale orphan at 100 is pre-existing stored state (a previous run)
	// the failing store never overwrote; canonical 99 sits below it.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{
		99:  canonicalHash(99),
		100: orphanHash(100),
	}}
	reorg.rollbackFn = func(from, to int64, _ map[int64]string) error {
		return purgeStoredHashes(reorg, from, to)
	}
	storer := &parkedStorer{reorg: reorg, failHeights: map[int64]int64{100: 1}}

	rec := &commitRecorder{}
	p := NewConcurrentBlockProcessor(canonicalOnlyFetcher(95, 115), chainConverter(), storer, reorg, 1, 0)

	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 4, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4],
		"the stale-pinned rollback must restore the canonical chain in order")

	calls := reorg.calls()
	require.Len(t, calls, 1, "one stale-orphan verify is one rollback episode")
	assert.Equal(t, int64(100), calls[0].from, "the walk-back descends from the failed height's orphan")

	reorg.mu.Lock()
	assert.Equal(t, canonicalHash(100), reorg.storedHashes[100], "the stale orphan must be rolled back and re-indexed canonically")
	reorg.mu.Unlock()
}

// TestParallelStores_HoleWarnsAndRunContinues covers the hole side of the
// failure handler: a generic store failure at 100 with nothing stored there
// warns the hole and the run keeps indexing past it - no episode, no
// rollback, and the failed height stays a hole the restart re-fills.
func TestParallelStores_HoleWarnsAndRunContinues(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	reorg := &mockReorgHandler{storedHashes: map[int64]string{99: canonicalHash(99)}}
	storer := &parkedStorer{reorg: reorg, failHeights: map[int64]int64{100: 1}}

	rec := &commitRecorder{}
	p := NewConcurrentBlockProcessor(canonicalOnlyFetcher(95, 115), chainConverter(), storer, reorg, 1, 0)

	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 3, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 3)
	assert.Equal(t, []int64{101, 102, 103}, got[:3], "the failed height stays a hole; canonical indexing resumes above it")
	assert.Empty(t, reorg.calls(), "an empty failed height must not open an episode")
}

// TestParallelStores_DrainBeforeWalkBack pins the drain-before-walk-back
// invariant: when the confirm arrives while a checked store is still parked
// in flight, the episode finalization waits for it - the walk-back's
// stored-hash queries and the rollback run only after the pool is quiescent,
// and nothing lands after the rollback was handed back.
func TestParallelStores_DrainBeforeWalkBack(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	// The orphan at 100 is served on the first fetch; canonical afterwards.
	var fetched100 atomic.Bool
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 100 && fetched100.CompareAndSwap(false, true) {
				return &servedBlock{height: height, hash: orphanHash(height), parentHash: canonicalHash(height - 1)}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockHeaderFn: headerLookup(canonicalHashIndex(95, 115)),
	}

	// The orphan store at 100 parks: the confirm pops at 102 while block
	// 100's store is still in flight, so the finalization's drain must wait
	// for it before querying stored hashes.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{99: canonicalHash(99)}}
	reorg.rollbackFn = func(from, to int64, _ map[int64]string) error {
		return purgeStoredHashes(reorg, from, to)
	}
	storer := &parkedStorer{reorg: reorg, storeBlocked: 100, released: make(chan struct{}), releasedC: make(chan struct{})}
	watcher := newInFlightWatcher(reorg, func() int64 { return storer.inFlight.Load() })

	rec := &commitRecorder{}
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, watcher, 1, 0)

	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// Keep the store parked for a window: the walk-back must not run while
	// it is in flight.
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, reorg.calls(), "no rollback may run while the in-flight store is parked")
	assert.Equal(t, int64(0), watcher.violations.Load(),
		"no stored-hash query or rollback may run while pool stores are in flight")

	// Release the parked store: the drain applies it, then the walk-back and
	// the rollback run over a quiescent pool.
	close(storer.released)
	waitParkedReleased(t, storer)

	waitForCommits(t, rec, 6, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 6)
	assert.Equal(t, []int64{100, 100, 101, 102, 103, 104}, got[:6],
		"the parked orphan store lands pre-mismatch and re-commits canonically after the rollback")

	calls := reorg.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, int64(100), calls[0].from)
	assert.Equal(t, int64(0), watcher.violations.Load(),
		"the walk-back and the rollback must have run without in-flight stores")
}

// TestParallelStores_CallbackOutsideProcessorLock pins the callback contract:
// onBlockProcessed fires after pendingMu is released, so a callback that
// re-enters a read path taking pendingMu (fatalFailure) cannot self-deadlock
// the collector.
func TestParallelStores_CallbackOutsideProcessorLock(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	rec := &commitRecorder{}
	reorg := &mockReorgHandler{}
	p := NewConcurrentBlockProcessor(canonicalOnlyFetcher(95, 115), chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	// fatalDuringRun is written only by the collector goroutine (the callback
	// fires there) and read after the run returned, so the errCh send
	// provides the ordering.
	var fatalDuringRun error
	cancel, errCh := runProcessor(p, 100, func(blockNum int64) {
		rec.record(blockNum)
		fatalDuringRun = p.fatalFailure()
	})
	defer cancel()

	waitForCommits(t, rec, 3, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
	assert.NoError(t, fatalDuringRun, "the callback's re-entrant state read must not see a fatal error")
	assert.Empty(t, reorg.calls())
}

// TestParallelStores_RefusalThroughPoolFailsRun pins the refusal's fatal
// classification on the async failure path: a deleted-docID refusal
// surfaced by a pool worker stops the run with that refusal as the indexing
// error instead of skipping the height.
func TestParallelStores_RefusalThroughPoolFailsRun(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	refusingStorer := &mockBlockStorer{
		storeFn: func(_ context.Context, _ chains.ConversionResult) (*defra.BlockCreationResult, error) {
			return nil, fmt.Errorf("create block: a document with the given ID has been deleted")
		},
	}

	rec := &commitRecorder{}
	reorg := &mockReorgHandler{}
	p := NewConcurrentBlockProcessor(canonicalOnlyFetcher(95, 115), chainConverter(), refusingStorer, reorg, 1, 0)

	_, errCh := runProcessor(p, 100, rec.record)

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refused")
		assert.Contains(t, strings.ToLower(err.Error()), "deleted")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the pool refusal to stop the run")
	}

	assert.Empty(t, rec.snapshot(), "the refused height must not commit or skip")
	assert.Empty(t, reorg.calls())
}
