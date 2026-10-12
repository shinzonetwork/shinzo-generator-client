package indexer

// reorg_test.go covers the parentHash continuity check and the episode
// procedure: orphan chains are detected in the ordered commit path (nothing
// is stored before the check passes), the mismatched block and everything
// served past it are held back during a bounded confirmation wait, the
// walk-back resolves the fork point by hash, and the outer loop rolls back
// the orphan range and restarts from forkPoint+1. Pure unit tests - no
// DefraDB, no RPC server; a scripted fetcher serves the chain state (by
// height and by hash, for the walk-back descent) and a mock converter
// carries the served hash/parentHash into the DocumentGroup contract the
// check consumes.

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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// servedBlock is the raw block a scripted fetcher hands to the converter.
type servedBlock struct {
	height     int64
	hash       string
	parentHash string
}

// canonicalHash derives the deterministic canonical hash for a height.
func canonicalHash(height int64) string {
	return fmt.Sprintf("0x%064x", height)
}

// orphanHash derives a distinguishable fork hash for a height: "0xdead" +
// 62 hex digits keeps the 0x-prefixed 64-digit shape.
func orphanHash(height int64) string {
	return fmt.Sprintf("0xdead%062x", height)
}

// forkParentHash is a persistently wrong parent hash served by a forked RPC
// node for the guard tests.
func forkParentHash() string {
	return "0xface" + strings.Repeat("f", 60)
}

// canonicalHashIndex builds the by-hash entries for the canonical chain over
// [lo, hi]: hash canonicalHash(h) resolves to the canonical block at h, the
// content the walk-back descent consumes when it follows the new chain's
// parent hashes downward.
func canonicalHashIndex(lo, hi int64) map[string]*servedBlock {
	index := make(map[string]*servedBlock, hi-lo+1)
	for h := lo; h <= hi; h++ {
		index[strings.ToLower(canonicalHash(h))] = &servedBlock{
			height:     h,
			hash:       canonicalHash(h),
			parentHash: canonicalHash(h - 1),
		}
	}
	return index
}

// byHashLookup turns a hash-keyed block index into a FetchBlockByHash
// function; an unknown hash surfaces as a not-found error so the walk-back's
// retry classification applies.
func byHashLookup(blocks map[string]*servedBlock) func(context.Context, string) (any, error) {
	return func(_ context.Context, hash string) (any, error) {
		if b, ok := blocks[strings.ToLower(hash)]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("block not found")
	}
}

// chainConverter adapts servedBlocks into conversion results carrying the
// served hash/parentHash, exercising the real extraction path.
func chainConverter() *testutils.MockConverter {
	return &testutils.MockConverter{
		ConvertFn: func(_ context.Context, raw any) (chains.ConversionResult, error) {
			served, ok := raw.(*servedBlock)
			if !ok {
				return chains.ConversionResult{}, fmt.Errorf("unexpected raw block %T", raw)
			}
			return chains.ConversionResult{
				Groups: []chains.DocumentGroup{
					{
						Collection: "block",
						Docs: []map[string]any{
							{
								constants.NumberFieldName: served.height,
								constants.HashFieldName:   served.hash,
								evm.ParentHashFieldName:   served.parentHash,
							},
						},
						BlockNumField:   constants.NumberFieldName,
						BlockHashField:  constants.HashFieldName,
						ParentHashField: evm.ParentHashFieldName,
					},
				},
			}, nil
		},
	}
}

// purgeStoredHashes drops the stored hashes in [from, to] on a
// mockReorgHandler, mirroring what a real soft-delete does to default
// stored-hash queries.
func purgeStoredHashes(reorg *mockReorgHandler, from, to int64) error {
	reorg.mu.Lock()
	for height := from; height <= to; height++ {
		delete(reorg.storedHashes, height)
	}
	reorg.mu.Unlock()
	return nil
}

// commitRecorder collects onBlockProcessed callbacks thread-safely.
type commitRecorder struct {
	mu        sync.Mutex
	committed []int64
}

func (r *commitRecorder) record(blockNum int64) {
	r.mu.Lock()
	r.committed = append(r.committed, blockNum)
	r.mu.Unlock()
}

func (r *commitRecorder) snapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.committed...)
}

// waitForCommits blocks until want callbacks have fired, failing on early
// ProcessBlocks exit or timeout.
func waitForCommits(t *testing.T, r *commitRecorder, want int, errCh <-chan error) {
	t.Helper()

	deadline := time.After(10 * time.Second)
	for {
		if len(r.snapshot()) >= want {
			return
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %d commits (got %d)", want, len(r.snapshot()))
		case err := <-errCh:
			t.Fatalf("ProcessBlocks exited early: %v", err)
		}
	}
}

// runProcessor launches ProcessBlocks in the background and returns its
// error channel; the caller cancels via the returned cancel.
func runProcessor(p *ConcurrentBlockProcessor, startBlock int64, onBlockProcessed func(int64)) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.ProcessBlocks(ctx, startBlock, onBlockProcessed)
	}()
	return cancel, errCh
}

// TestReorgProcessor_RollbackAndResume is the incident scenario: height 100
// is served as an orphan on the first fetch and canonically afterwards (the
// reorg lands between the two fetches), so canonical 101 disagrees with the
// committed orphan. The processor holds block 101 back, waits for chained
// 102 to confirm the new chain, walks back by hash to the stored common
// ancestor at 99, and rolls back only the orphan range [100, tip). onBlock-
// Processed fires once for the (briefly stored) orphan commit and again for
// the canonical re-commit.
func TestReorgProcessor_RollbackAndResume(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	var orphanFetched atomic.Bool
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 100 && orphanFetched.CompareAndSwap(false, true) {
				return &servedBlock{height: 100, hash: orphanHash(100), parentHash: canonicalHash(99)}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 115)),
	}

	rec := &commitRecorder{}
	reorg := &mockReorgHandler{storedHashes: map[int64]string{99: canonicalHash(99)}}
	// RollbackBlocks runs strictly after the run's full drain, so the
	// callbacks captured at the first rollback call are final for run 1:
	// none of them may sit past the recorded rollback point.
	var committedAtRollback []int64
	reorg.rollbackFn = func(_, _ int64) error {
		committedAtRollback = rec.snapshot()
		return nil
	}
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// [100 orphan, 100 canonical, 101, 102, 103, 104]
	waitForCommits(t, rec, 6, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	// Cancel races the collector: a few extra canonical commits may land
	// after the 6th - assert the expected prefix, not an exact total.
	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 6)
	assert.Equal(t, []int64{100, 100, 101, 102, 103, 104}, got[:6])

	calls := reorg.calls()
	require.Len(t, calls, 1, "a single-level reorg needs exactly one rollback")
	assert.Equal(t, int64(100), calls[0].from)
	assert.GreaterOrEqual(t, calls[0].to, int64(101), "the purge must cover dispatched heights past the rollback point")
	assert.LessOrEqual(t, calls[0].to, int64(110), "the purge ceiling must stay near the dispatch window")

	// onBlockProcessed must never fire for a height past the recorded
	// rollback point inside the run that detected the reorg: run 1 commits
	// only the orphan 100, and everything after the detection is drained
	// without committing.
	require.NotEmpty(t, committedAtRollback, "run 1 must have committed the orphan before detecting the reorg")
	for _, height := range committedAtRollback {
		assert.LessOrEqual(t, height, calls[0].from,
			"onBlockProcessed fired for height %d after the rollback point was recorded", height)
	}
}

// TestReorgProcessor_MultiBlockReorgConverges walks a two-deep orphan chain:
// orphan 100 and 101 commit in-run (both chain on the committed predecessors),
// canonical 102 disagrees and is held back, chained 103 confirms the new
// chain, and the walk-back descends past both orphans to the stored common
// ancestor at 99 - a single rollback episode covering [100, tip) replaces
// today's one-level-per-rollback peeling, then canonical indexing resumes.
func TestReorgProcessor_MultiBlockReorgConverges(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	var fetch100 atomic.Int64
	var fetch101 atomic.Int64
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			switch height {
			case 100:
				if fetch100.Add(1) == 1 {
					return &servedBlock{height: 100, hash: orphanHash(100), parentHash: canonicalHash(99)}, nil
				}
			case 101:
				if fetch101.Add(1) == 1 {
					return &servedBlock{height: 101, hash: orphanHash(101), parentHash: orphanHash(100)}, nil
				}
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 115)),
	}

	reorg := &mockReorgHandler{
		storedHashes: map[int64]string{
			99:  canonicalHash(99),
			100: orphanHash(100),
		},
	}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// [100 orphan, 101 orphan, 100 canonical, 101 canonical, 102, 103]
	waitForCommits(t, rec, 6, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 6)
	assert.Equal(t, []int64{100, 101, 100, 101, 102, 103}, got[:6])

	calls := reorg.calls()
	require.Len(t, calls, 1, "the walk-back collapses the two-deep reorg into a single rollback episode")
	assert.Equal(t, int64(100), calls[0].from)
	assert.GreaterOrEqual(t, calls[0].to, int64(101), "the purge must cover the orphan chain above the fork point")
}

// TestReorgProcessor_SeedCheckRestartSeam covers the resume-after-downtime
// path: an orphan sits stored at startBlock-1 from a previous run, the first
// fetched block's parentHash disagrees with the seed, and the episode's
// walk-back resolves the fork point below the lowest stored height (nothing
// is stored below the seam), deleting the orphan seam and re-committing the
// canonical content.
func TestReorgProcessor_SeedCheckRestartSeam(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 110)),
	}

	reorg := &mockReorgHandler{storedHashes: map[int64]string{100: orphanHash(100)}}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 101, rec.record)
	defer cancel()

	// Restart from 100 re-commits the canonical seam and the run's own range.
	waitForCommits(t, rec, 4, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4])

	calls := reorg.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, int64(100), calls[0].from)
	assert.GreaterOrEqual(t, calls[0].to, int64(101))
}

// TestReorgProcessor_GapFallbackDetectsStalePredecessor covers the gap
// fallback: a stale orphan from a previous run sits stored at startBlock
// while the fresh store of that same height fails transiently, so nothing
// commits at the seam. Block 101's predecessor was therefore not committed
// in this run - the gap fallback queries the stored hash at 100, finds the
// stale orphan hash, and starts the resolve episode from it, instead of
// silently indexing over the failed height.
func TestReorgProcessor_GapFallbackDetectsStalePredecessor(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 110)),
	}

	var storeCalls atomic.Int64
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			// Exactly one transient store failure so the seam height stays
			// uncommitted and the stale stored content becomes the only
			// visible predecessor.
			if blockNumOfResult(result) == 100 && storeCalls.Add(1) == 1 {
				return nil, fmt.Errorf("store exploded")
			}
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	reorg := &mockReorgHandler{
		storedHashes: map[int64]string{
			100: orphanHash(100),
		},
	}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// The failed seam makes the gap fallback detect the stored orphan from
	// block 101; the rolled-back restart re-commits the canonical seam and
	// resumes.
	waitForCommits(t, rec, 4, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4])

	calls := reorg.calls()
	require.Len(t, calls, 1, "the stale stored predecessor must be detected by the gap fallback and rolled back once")
	assert.Equal(t, int64(100), calls[0].from)
}

// blockNumOfResult extracts the block height from a conversion result's
// block group so scripted storers can fail selected heights; -1 when absent.
func blockNumOfResult(result chains.ConversionResult) int64 {
	for _, g := range result.Groups {
		if g.BlockNumField != "" && len(g.Docs) > 0 {
			if num, ok := g.Docs[0][g.BlockNumField].(int64); ok {
				return num
			}
		}
	}
	return -1
}

// TestReorgProcessor_GapNoFalseReorg proves a failed predecessor never
// produces a false reorg signal: block 100 fails to store, block 101 commits
// over the gap (no contiguous predecessor), and no rollback happens.
func TestReorgProcessor_GapNoFalseReorg(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}

	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			if blockNumOfResult(result) == 100 {
				return nil, fmt.Errorf("store exploded")
			}
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	reorg := &mockReorgHandler{}
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 2, errCh)
	cancel()
	<-errCh

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 2)
	assert.Equal(t, []int64{101, 102}, got[:2])
	assert.Empty(t, reorg.calls())
}

// TestReorgProcessor_GuardSameHeightRollbacks trips the per-height guard
// with consecutive rollbacks and no intervening commit: a forked RPC
// persistently serves 101 with a wrong parentHash, so the same height
// resolves to the same fork point and rolls back again and again. Any
// commit resets the episode guards, so the scenario pins the no-progress
// case: the re-store of the rolled-back height 100 is refused (the
// soft-deleted dockey behavior real DefraDB shows) and the stale stored
// hash at 100 keeps the gap fallback firing at 101. The by-hash server
// keeps the walk-back resolvable (a garbage parent hash fetches a
// chain-consistent fork block) so the guard itself is what stops the loop.
func TestReorgProcessor_GuardSameHeightRollbacks(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	forkBlock := &servedBlock{height: 100, hash: forkParentHash(), parentHash: canonicalHash(99)}
	byHash := canonicalHashIndex(95, 110)
	byHash[strings.ToLower(forkParentHash())] = forkBlock

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 101 {
				return &servedBlock{height: 101, hash: canonicalHash(101), parentHash: forkParentHash()}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(byHash),
	}

	// The first store at height 100 succeeds (the run-1 commit that makes
	// 101's mismatch a contiguous-rule detection); every later store is
	// refused with the DefraDB message for a soft-deleted content-derived
	// dockey, so nothing ever commits again and the guards accumulate.
	storeCalls := atomic.Int64{}
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			if blockNumOfResult(result) == 100 && storeCalls.Add(1) > 1 {
				return nil, fmt.Errorf("create block: a document with the given ID has been deleted")
			}
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	// The no-op mock rollback leaves the stored hashes untouched, so every
	// restart run sees the same stale stored 100 and rolls to the same fork
	// point at 99.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{
		99:  canonicalHash(99),
		100: canonicalHash(100),
	}}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	_, errCh := runProcessor(p, 100, nil)

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rolled back")
		assert.Contains(t, err.Error(), "limit")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the same-height rollback guard")
	}

	calls := reorg.calls()
	assert.Len(t, calls, maxRollbacksPerHeight,
		"the guard must stop the loop before the %dth rollback at the same height executes", maxRollbacksPerHeight+1)
	for _, call := range calls {
		assert.Equal(t, int64(100), call.from)
	}
}

// TestReorgProcessor_WalkBackCapFailsWithoutCommonAncestor proves the
// walk-back's hard bound: a chain whose stored hashes never agree with any
// served hash (persistently forked node) cannot converge, so the episode
// fails fatally once the descent exceeds the depth cap instead of rolling
// back on a guess. Nothing is deleted on that path.
func TestReorgProcessor_WalkBackCapFailsWithoutCommonAncestor(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(0, 110)),
		// Disable the finalized floor so the hard depth cap is the only
		// bound this test exercises.
		FetchFinalizedBlockNumberFn: func(_ context.Context) (int64, error) {
			return 0, fmt.Errorf("finalized block number unavailable")
		},
	}

	// A handler that serves a constant wrong hash for every height: no
	// common ancestor can exist anywhere.
	reorg := &mockReorgHandler{defaultHash: forkParentHash()}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	_, errCh := runProcessor(p, 100, nil)

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "walk-back")
		assert.Contains(t, err.Error(), "depth cap")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the walk-back depth cap")
	}

	assert.Empty(t, reorg.calls(), "no deletion may happen when no common ancestor can be verified")
}

// TestReorgProcessor_RollbackFailureIsFatal proves a failing rollback stops
// indexing loudly instead of re-indexing over a partially purged range.
func TestReorgProcessor_RollbackFailureIsFatal(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	var orphanFetched atomic.Bool
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 100 && orphanFetched.CompareAndSwap(false, true) {
				return &servedBlock{height: 100, hash: orphanHash(100), parentHash: canonicalHash(99)}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 110)),
	}

	reorg := &mockReorgHandler{storedHashes: map[int64]string{99: canonicalHash(99)}}
	reorg.rollbackFn = func(_, _ int64) error {
		return fmt.Errorf("rollback boom")
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	_, errCh := runProcessor(p, 100, nil)

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rollback boom")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the rollback failure")
	}
}
