package indexer

// reorg_test.go covers the parentHash continuity check and the rollback
// orchestration: orphan chains are detected at commit time, the dispatched
// range is rolled back via the ReorgHandler, and indexing restarts from the
// rollback height. Pure unit tests — no DefraDB, no RPC server; a scripted
// fetcher serves the chain state and a mock converter carries the served
// hash/parentHash into the DocumentGroup contract the check consumes.

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
// committed orphan. The processor must roll back from 100, re-fetch the
// canonical 100, and resume ordered commits. onBlockProcessed fires once for
// the (briefly stored) orphan commit and again for the canonical re-commit.
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
	// after the 6th — assert the expected prefix, not an exact total.
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
// orphan 100 and 101 commit in-run, canonical 102 disagrees, the first
// rollback peels 101, the restarted run detects the stored orphan 100 via
// its seed, and the second rollback peels it — one level per rollback, then
// canonical indexing resumes.
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
	require.Len(t, calls, 2, "a two-deep reorg peels one level per rollback")
	assert.Equal(t, int64(101), calls[0].from)
	assert.Equal(t, int64(100), calls[1].from)
}

// TestReorgProcessor_SeedCheckRestartSeam covers the resume-after-downtime
// path: an orphan sits stored at startBlock-1 from a previous run, the first
// fetched block's parentHash disagrees with the seed, and the rollback peels
// the stale seam without any in-run predecessor.
func TestReorgProcessor_SeedCheckRestartSeam(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
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

// TestReorgProcessor_AlreadyExistsBackstop covers the resume seam where the
// tail reorged while the indexer was down: the store reports "already
// exists", the stored hash differs from the fetched one, and the processor
// rolls back from that height instead of signing a stale block forward. An
// already-exists with an equal stored hash keeps the idempotent sign path.
func TestReorgProcessor_AlreadyExistsBackstop(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}

	var signCalls atomic.Int64
	storer := &mockBlockStorer{
		storeFn: func(_ context.Context, _ chains.ConversionResult) (*defra.BlockCreationResult, error) {
			return nil, fmt.Errorf("block already exists")
		},
		signExistingFn: func(_ context.Context, _ chains.ConversionResult, _ string, _ int64) (string, error) {
			signCalls.Add(1)
			return "sig", nil
		},
	}

	reorg := &mockReorgHandler{
		storedHashes: map[int64]string{
			100: orphanHash(100),
			// 102 stored with the canonical hash: the equal-hash backstop
			// must keep the routine sign path, not trigger a rollback.
			102: canonicalHash(102),
		},
	}
	reorg.rollbackFn = func(from, to int64) error {
		return purgeStoredHashes(reorg, from, to)
	}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, reorg, 1, 0)

	rec := &commitRecorder{}
	cancel, errCh := runProcessor(p, 100, rec.record)
	defer cancel()

	// 100 rolls back, then everything after commits via the sign path.
	waitForCommits(t, rec, 4, errCh)
	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)

	got := rec.snapshot()
	require.GreaterOrEqual(t, len(got), 4)
	assert.Equal(t, []int64{100, 101, 102, 103}, got[:4])

	calls := reorg.calls()
	require.Len(t, calls, 1, "only the stale stored block must trigger a rollback")
	assert.Equal(t, int64(100), calls[0].from)
	assert.GreaterOrEqual(t, signCalls.Load(), int64(1), "idempotent already-exists must go through SignExisting")
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

// TestReorgProcessor_GuardSameHeightRollbacks trips the per-height guard: a
// forked RPC persistently serves 101 with a wrong parentHash, so the same
// height rolls back again and again until the guard errors out.
func TestReorgProcessor_GuardSameHeightRollbacks(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 101 {
				return &servedBlock{height: 101, hash: canonicalHash(101), parentHash: forkParentHash()}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}

	reorg := &mockReorgHandler{storedHashes: map[int64]string{99: canonicalHash(99)}}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

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

// TestReorgProcessor_GuardTotalRollbacks trips the per-run guard: a reorg
// handler whose stored hashes never match any parent makes every restart
// peel one level further down, until the total count exceeds the limit.
func TestReorgProcessor_GuardTotalRollbacks(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
	}

	reorg := &mockReorgHandler{defaultHash: forkParentHash()}

	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), &mockBlockStorer{}, reorg, 1, 0)

	_, errCh := runProcessor(p, 100, nil)

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "per-run limit")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the total rollback guard")
	}

	assert.NotEmpty(t, reorg.calls())
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
