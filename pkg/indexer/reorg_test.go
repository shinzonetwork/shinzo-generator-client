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
//
// The scenarios here double as the Phase 3 unit coverage for the episode
// bounds: walk-back depth below the first mismatch (MultiBlockReorgConverges,
// SeedCheckRestartSeam), the finalized-floor fatal, the confirmation wait
// re-anchoring on a mid-wait chain flip without deleting anything, the
// guard reset on intervening commits, and the gap fallback catching a
// partially stored seam. The by-hash/finalized fetcher behavior itself is
// covered in pkg/chains/evm.

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

// flipHash derives the hash of the chain-side block the RPC node serves
// after it flips to a second fork mid-confirmation-wait ("0xbeef" prefix,
// same 64-hex-digit shape as canonicalHash/orphanHash).
func flipHash(height int64) string {
	return fmt.Sprintf("0xbeef%062x", height)
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

// TestReorgProcessor_InterruptedRollbackResumesWithoutHole pins the
// descending-height delete order: a two-orphan rollback interrupted after
// its first deletion (the highest verified orphan, per the roller's
// descending order) leaves the lowest orphan visible, so the resumed run's
// walk-back descends onto the stored mismatch and finds the real fork point
// instead of treating the emptied height as the lowest-stored boundary -
// and both heights re-index with no permanent hole at fork+1.
func TestReorgProcessor_InterruptedRollbackResumesWithoutHole(t *testing.T) {
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

	// A successful store must keep the height visible in the handler's
	// stored-hash map, the way a real store does.
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			num := blockNumOfResult(result)
			hash := extractBlockHash(result.Groups)
			reorg.mu.Lock()
			reorg.storedHashes[num] = hash
			reorg.mu.Unlock()
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	// The failing-once pass-through rollback: the first RollbackBlocks call
	// emulates the roller interrupted after its first (highest, descending
	// order) deletion - the top stored height in the verified range is
	// soft-deleted (hidden) and then the rollback dies. Later calls pass
	// through to the full purge emulation.
	var interrupted atomic.Bool
	reorg.rollbackFn = func(from, to int64) error {
		if interrupted.CompareAndSwap(false, true) {
			reorg.mu.Lock()
			top := int64(-1)
			for height := to; height >= from; height-- {
				if _, stored := reorg.storedHashes[height]; stored {
					top = height
					break
				}
			}
			if top >= 0 {
				delete(reorg.storedHashes, top)
			}
			reorg.mu.Unlock()
			return fmt.Errorf("rollback interrupted after deleting height %d", top)
		}
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	// Run 1: both orphans commit, canonical 102 opens the episode, the
	// walk-back resolves the fork point at 99, and the rollback dies after
	// deleting the highest verified orphan (101) - a fatal run error.
	_, errCh := runProcessor(p, 100, nil)
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rollback interrupted")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the interrupted rollback")
	}

	calls := reorg.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, int64(100), calls[0].from)
	assert.GreaterOrEqual(t, calls[0].to, int64(101))

	// Interrupted state: the lowest orphan (100, fork+1) stays visible, the
	// highest (101) is hidden - exactly what the descending order promises.
	reorg.mu.Lock()
	assert.Equal(t, orphanHash(100), reorg.storedHashes[100], "the lowest orphan must stay visible for the resumed walk-back")
	_, hidden := reorg.storedHashes[101]
	assert.False(t, hidden, "the highest orphan must be the one deleted before the interruption")
	reorg.mu.Unlock()

	// Resumed run: restarts at the highest surviving stored height + 1; the
	// seed check detects the mismatch against the still-stored orphan 100,
	// the walk-back descends to the real fork point (99), the retried
	// rollback re-rolls the remaining orphan, and both heights re-index.
	rec := &commitRecorder{}
	cancel, errCh2 := runProcessor(p, 101, rec.record)
	defer cancel()

	waitForCommits(t, rec, 6, errCh2)
	cancel()
	err := <-errCh2
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 6)
	assert.Equal(t, []int64{100, 101, 102, 103, 104, 105}, got[:6],
		"the resumed run must re-index from fork+1 with no permanent hole at the emptied seam")

	require.Len(t, reorg.calls(), 2,
		"the resumed run retries the rollback once before re-indexing")
	assert.Equal(t, int64(100), reorg.calls()[1].from,
		"the retried rollback must start at the real fork point + 1, not the emptied height + 2")

	reorg.mu.Lock()
	assert.Equal(t, canonicalHash(100), reorg.storedHashes[100], "fork+1 must be re-indexed with the canonical hash")
	assert.Equal(t, canonicalHash(101), reorg.storedHashes[101], "fork+2 must be re-indexed with the canonical hash")
	reorg.mu.Unlock()
}

// TestReorgProcessor_WalkBackDescendsPastStoreHole pins the walk-back's hole
// handling: a failed store leaves height 101 empty between two stored orphans
// (100 and 102), and the descent must not take that empty height as the
// fork-point boundary. It descends past the hole to the stored common
// ancestor at 99, so the rollback covers both orphans and the restart
// re-indexes 100 upward, re-filling the hole at 101.
func TestReorgProcessor_WalkBackDescendsPastStoreHole(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	var fetch100 atomic.Int64
	var fetch101 atomic.Int64
	var fetch102 atomic.Int64
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			switch height {
			case 100:
				if fetch100.Add(1) == 1 {
					return &servedBlock{height: 100, hash: orphanHash(100), parentHash: canonicalHash(99)}, nil
				}
			case 101:
				// The node serves the orphan chain at 101 consistently with
				// the committed orphan 100, so a store failure - not a
				// mismatch - is what leaves the height empty.
				if fetch101.Add(1) == 1 {
					return &servedBlock{height: 101, hash: orphanHash(101), parentHash: orphanHash(100)}, nil
				}
			case 102:
				if fetch102.Add(1) == 1 {
					return &servedBlock{height: 102, hash: orphanHash(102), parentHash: orphanHash(101)}, nil
				}
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 115)),
	}

	// Stored state at the episode: canonical 99, orphan 100, the hole at 101
	// (the store below dies transiently), and orphan 102 committed over the
	// unfillable gap fallback.
	reorg := &mockReorgHandler{
		storedHashes: map[int64]string{
			99:  canonicalHash(99),
			100: orphanHash(100),
		},
	}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	// A store that emulates a real one: successful stores keep the height
	// visible in the handler's stored-hash map, and the first store at 101
	// dies transiently so the height stays empty between the two orphans.
	var store101 atomic.Int64
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			num := blockNumOfResult(result)
			if num == 101 && store101.Add(1) == 1 {
				return nil, fmt.Errorf("store exploded")
			}
			reorg.mu.Lock()
			reorg.storedHashes[num] = extractBlockHash(result.Groups)
			reorg.mu.Unlock()
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// [orphan 100, orphan 102, then the restart re-commits 100, fills the
	// hole at 101, and re-commits 102 canonically]
	waitForCommits(t, rec, 8, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 8)
	assert.Equal(t, []int64{100, 102, 100, 101, 102, 103, 104, 105}, got[:8],
		"the hole at 101 must be re-indexed after the walk-back descends past it")

	calls := reorg.calls()
	require.Len(t, calls, 1, "the hole sits inside a single rollback episode")
	assert.Equal(t, int64(100), calls[0].from,
		"the walk-back must not take the empty height 101 as the fork point; the rollback restarts at the real fork+1")
	assert.GreaterOrEqual(t, calls[0].to, int64(102), "the purge must cover the orphan above the hole")

	reorg.mu.Lock()
	assert.Equal(t, canonicalHash(100), reorg.storedHashes[100], "orphan 100 must roll back and re-index canonically")
	assert.Equal(t, canonicalHash(101), reorg.storedHashes[101], "the failed-store hole must be re-filled")
	assert.Equal(t, canonicalHash(102), reorg.storedHashes[102], "orphan 102 must roll back and re-index canonically")
	reorg.mu.Unlock()
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
// case: the re-store of the rolled-back height 100 fails with a generic
// store error (the soft-deleted docID refusal is classified as fatal and
// pinned by RestoreRefusalStopsRun instead) and the stale stored hash at
// 100 keeps the gap fallback firing at 101. The by-hash server keeps the
// walk-back resolvable (a garbage parent hash fetches a chain-consistent
// fork block) so the guard itself is what stops the loop.
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
	// 101's mismatch a contiguous-rule detection); every later store fails
	// generically so nothing ever commits again and the guards accumulate.
	// The failure message must match neither the deleted-docID refusal
	// (fatal since the classification) nor the already-exists idempotence
	// (an idempotent success would commit and reset the guards).
	storeCalls := atomic.Int64{}
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			if blockNumOfResult(result) == 100 && storeCalls.Add(1) > 1 {
				return nil, fmt.Errorf("create block: store exploded")
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

// TestReorgProcessor_RestoreRefusalStopsRun pins the fatal classification of
// the DefraDB deleted-docID refusal: an A->B->A sequence stores B content at
// 100 over the prior run's A content, the confirmed A chain rolls the forked
// height back (the A and B content-derived dockeys both go soft-deleted),
// and the chain still serves the same A content at 100 for the resumed run.
// The byte-identical re-store is refused ("a document with the given ID has
// been deleted"); the run must stop with that refusal as the indexing error
// instead of skipping the height silently and leaving a permanently
// un-indexable hole behind (the pruner is the only mechanism that can
// reclaim the soft-deleted docIDs).
func TestReorgProcessor_RestoreRefusalStopsRun(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	// The chain serves B content at 100 and 101 on the first fetches, which
	// makes the A content stored under the previous run the verified
	// orphans; every later fetch serves the canonical A content again, so
	// the resumed run re-fetches byte-identical content for 100.
	var fetched100 atomic.Int64
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 100 {
				if fetched100.Add(1) == 1 {
					return &servedBlock{height: 100, hash: flipHash(100), parentHash: canonicalHash(99)}, nil
				}
				return &servedBlock{height: 100, hash: canonicalHash(100), parentHash: canonicalHash(99)}, nil
			}
			if height == 101 {
				return &servedBlock{height: 101, hash: flipHash(101), parentHash: flipHash(100)}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 110)),
	}

	// A content stored at 99 and 100 under the previous run; the first fetch
	// of 100 serves B content over the verified seed seam, so the walk-back
	// sees B's hash at 100 and rolls the A content back as the verified
	// orphan of a fork at 99.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{
		99:  canonicalHash(99),
		100: canonicalHash(100),
	}}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	// The re-store of the rolled-back A content is refused with the real
	// DefraDB message; every fresh-content store succeeds. The store mirrors
	// a real store by publishing the stored hash, and the rollback purges it
	// like a real soft-delete, so the walk-back sees the same DB state a real
	// DefraDB would.
	var stored100 atomic.Int64
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			if blockNumOfResult(result) == 100 && stored100.Add(1) > 1 {
				return nil, fmt.Errorf("create block: a document with the given ID has been deleted")
			}
			reorg.mu.Lock()
			reorg.storedHashes[blockNumOfResult(result)] = extractBlockHash(result.Groups)
			reorg.mu.Unlock()
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}

	rec := &commitRecorder{}
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	_, errCh := runProcessor(p, 100, rec.record)

	select {
	case fatalErr := <-errCh:
		require.Error(t, fatalErr)
		assert.Contains(t, fatalErr.Error(), "refused")
		assert.Contains(t, strings.ToLower(fatalErr.Error()), "deleted")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the refused re-store to stop the run")
	}

	assert.Equal(t, []int64{100, 101}, rec.snapshot(),
		"only the forked B commits may land before the rollback; the refused A re-store in the resumed run must not commit or skip")

	calls := reorg.calls()
	require.Len(t, calls, 1, "the A->B flip must roll back exactly once before the refusal stops the run")
	assert.Equal(t, int64(100), calls[0].from)
}

// TestReorgProcessor_FinalizedFloorStopsWalkBack proves the walk-back's
// finalized bound: the chain reports a finalized height, the stored hashes
// disagree with the served chain all the way down to it, and the episode
// fails fatally at the floor instead of rolling back anything at or below
// the safety bound. Nothing is deleted on that path.
func TestReorgProcessor_FinalizedFloorStopsWalkBack(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(0, 110)),
		// The chain reports finalized 98; the walk never descends past it.
		FetchFinalizedBlockNumberFn: func(_ context.Context) (int64, error) {
			return 98, nil
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
		assert.Contains(t, err.Error(), "finalized height")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the finalized-floor fatal")
	}

	assert.Empty(t, reorg.calls(), "no deletion may happen at or past the finalized height")
}

// TestReorgProcessor_ConfirmationWaitReanchorsOnNewestChain covers the
// confirmation wait's flip handling: a mid-wait disagreement re-anchors the
// episode on the newest chain observation and keeps waiting, and the episode
// is only confirmed - and rolled back - once a block chains onto the newest
// anchor. Nothing is ever stored or deleted during the wait (exactly one
// rollback episode fires, after the confirm), so a flip-flopping chain costs
// re-plans, not deletions.
func TestReorgProcessor_ConfirmationWaitReanchorsOnNewestChain(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetch101 := atomic.Int64{}
	fetch102 := atomic.Int64{}
	fetch103 := atomic.Int64{}
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			switch height {
			case 101:
				// First fetch: chain A, disagreeing with the committed
				// canonical 100 - the mismatch that opens the episode.
				if fetch101.Add(1) == 1 {
					return &servedBlock{height: 101, hash: orphanHash(101), parentHash: forkParentHash()}, nil
				}
			case 102:
				// First fetch: chain B - breaks the pending continuity of
				// chain A, so the wait re-anchors before anything confirms.
				if fetch102.Add(1) == 1 {
					return &servedBlock{height: 102, hash: flipHash(102), parentHash: flipHash(101)}, nil
				}
			case 103:
				// First fetch only: chain B's next block, chaining onto the
				// re-anchored newest observation to confirm the episode.
				// Later fetches (the restart after the rollback) serve the
				// canonical chain so the re-commit does not re-detect.
				if fetch103.CompareAndSwap(0, 1) {
					return &servedBlock{height: 103, hash: canonicalHash(103), parentHash: flipHash(102)}, nil
				}
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		// The walk-back must descend chain B past the seam: the served
		// chain-B block at 101 leads into the stored canonical 100.
		FetchBlockByHashFn: byHashLookup(map[string]*servedBlock{
			strings.ToLower(flipHash(101)): {height: 101, hash: flipHash(101), parentHash: canonicalHash(100)},
		}),
	}

	// The handler carries the pre-reorg stored state; the rollback purges
	// like a real soft-delete would.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{100: canonicalHash(100)}}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// [100 canonical, then the restart re-commits 101, 102, 103]
	waitForCommits(t, rec, 4, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4])

	calls := reorg.calls()
	require.Len(t, calls, 1,
		"the wait re-anchors without deleting: a single rollback fires after the confirm")
	assert.Equal(t, int64(101), calls[0].from,
		"the rollback restarts above the newest chain's common ancestor (the stored 100)")
	assert.GreaterOrEqual(t, calls[0].to, int64(102),
		"the rollback must cover the heights dispatched during the wait")
}

// TestReorgProcessor_GuardResetsOnProgress pins the episode-based guard
// semantics: 65+ rollbacks that are each interleaved with a successful
// commit never trip the per-run limit, because every commit resets the
// guards. The scenario keeps cycling a one-block reorg (persistently
// forked fork at 101, stores always succeeding, mock rollback a no-op),
// so the test itself stops the loop once the cap has been passed.
func TestReorgProcessor_GuardResetsOnProgress(t *testing.T) {
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

	// Every store succeeds, so each rollback cycle interleaves a commit
	// (at the restart height 100) before the next mismatch at 101.
	storer := &mockBlockStorer{}

	// The no-op rollback preserves the stored hashes, so every cycle
	// resolves to the same fork point at 99 and restarts from 100.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{
		99:  canonicalHash(99),
		100: canonicalHash(100),
	}}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	cancel, errCh := runProcessor(p, 100, nil)

	deadline := time.After(30 * time.Second)
	for len(reorg.calls()) <= maxRollbacksPerRun {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %d interleaved rollback cycles (got %d)",
				maxRollbacksPerRun+1, len(reorg.calls()))
		case err := <-errCh:
			t.Fatalf("ProcessBlocks exited early: %v", err)
		}
	}
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled,
		"a reorg per committed block must never trip the guards")

	calls := reorg.calls()
	require.Greater(t, len(calls), maxRollbacksPerRun,
		"the test must have driven more rollbacks than the per-run limit to prove the resets")
	for _, call := range calls {
		assert.Equal(t, int64(100), call.from)
	}
}

// TestReorgProcessor_GapFallbackCatchesPartialStore covers the partial-store
// variant of the gap fallback from the spec: the first store of height 100
// writes its block doc and then fails, so the generated orphan keeps a
// visible block doc (exactly what a crash or a later-group failure leaves
// behind) and nothing commits at the seam. Block 101's predecessor was not
// committed in this run, so the gap fallback queries the stored hash at 100,
// finds the partially stored orphan, and opens the episode instead of
// silently indexing over the suspect height.
func TestReorgProcessor_GapFallbackCatchesPartialStore(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockByHashFn: byHashLookup(canonicalHashIndex(95, 110)),
	}

	// storedHashes starts empty but non-nil: the scripted storer writes the
	// partially stored block doc into it, mirroring what a real partial
	// store leaves visible.
	reorg := &mockReorgHandler{storedHashes: map[int64]string{}}
	var storeCalls atomic.Int64
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
			// The first store of 100 persists the block doc (the visible
			// partial state) and then fails, so nothing commits at the seam.
			if blockNumOfResult(result) == 100 && storeCalls.Add(1) == 1 {
				reorg.mu.Lock()
				reorg.storedHashes[100] = orphanHash(100)
				reorg.mu.Unlock()
				return nil, fmt.Errorf("write tx docs: store exploded")
			}
			return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
		},
	}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	waitForCommits(t, rec, 4, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4])

	calls := reorg.calls()
	require.Len(t, calls, 1,
		"the partially stored seam must be detected by the gap fallback and rolled back once")
	assert.Equal(t, int64(100), calls[0].from)
}
