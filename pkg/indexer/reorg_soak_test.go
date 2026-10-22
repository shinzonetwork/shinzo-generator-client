package indexer

// reorg_soak_test.go is the seeded stochastic coverage the deterministic
// reorg tests cannot provide: it drives the parallel store pool and the
// episode procedure through randomized chain shapes (forks of random base
// and depth), randomized transient store failures, pre-seeded stale orphans,
// random worker counts, and mid-run cancellations with restarts, then
// asserts schedule-independent invariants: the stored set converges to the
// canonical chain prefix, stored-hash reads never overlap an in-flight store
// of the same height, and rollbacks run only against a quiescent pool. Each
// invocation must also end in a clean cancellation, and a hung run cancels
// through a watchdog and flags itself, so a pool deadlock surfaces as an
// assertion failure instead of a dead test. The seed fixes the scenario, not
// the goroutine scheduling, so re-runs explore different interleavings.
//
// Chaos covered deterministically elsewhere is deliberately left out:
// fetch-failure holes and their 500ms+ retry sleeps (GapFallback*,
// HoleWarnsAndRunContinues), rate limiting, and fine-grained callback
// ordering (TestParallelStores_OverlapWithOrderedCallbacks).

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"strconv"
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

// soakSeedEnvVar lets a failing seed be re-run in isolation, e.g.
// REORG_SOAK_SEEDS=7 or REORG_SOAK_SEEDS=3-12; the fixed default range keeps
// CI deterministic while scheduling stays free.
const soakSeedEnvVar = "REORG_SOAK_SEEDS"

// soakBranchHash derives a distinguishable fork-chain hash: the 0xabc prefix
// keeps the 64 hex digit shape and never collides with canonical, orphan, or
// flip hashes.
func soakBranchHash(height int64) string {
	return fmt.Sprintf("0xabc%061x", height)
}

// soakFork is one branch event: for every covered height the node serves the
// branch block for the first fetches of that height and canonical
// content afterwards. The per-height counters live in the node (not the
// plan) and persist across invocations, so a cancelled run can interrupt a
// switch mid-resolution and the restart resolves it.
type soakFork struct {
	base    int64
	length  int64
	fetches int64
	flip    bool // flip forks serve flipHash blocks (the re-anchor shape)
}

func (f soakFork) covers(height int64) bool {
	return height >= f.base && height < f.base+f.length
}

// soakForkHash is the branch hash at a covered height.
func soakForkHash(f soakFork, height int64) string {
	if f.flip {
		return flipHash(height)
	}
	return soakBranchHash(height)
}

// soakForkParent chains a covered height onto the covered height below it
// when the fork is deep past its base, else onto the canonical block below
// the fork base, so a branch serve chains from whatever the run last checked
// and a disconnect still resolves through the episode procedure.
func soakForkParent(f soakFork, height int64) string {
	if f.covers(height - 1) {
		return soakForkHash(f, height-1)
	}
	return canonicalHash(height - 1)
}

// soakPlan is the per-seed scenario: the canonical window, the branch
// events, the pre-seeded stale orphans (each also carrying at least one
// store failure so the pool-side stale verification has a disagreement to
// catch), the other transient failure heights, the finalized floor, the
// worker count, and the number of mid-run cancellations before the settle
// phase.
type soakPlan struct {
	start, end  int64
	forks       []soakFork
	stale       map[int64]bool
	failHeights map[int64]int64
	floor       int64 // negative: the mock reports an error and the descent falls back to its depth cap
	workers     int
	restarts    int
}

// overlaps reports whether a candidate fork window shares a height with the
// plan's existing forks.
func (s soakPlan) overlaps(base, length int64) bool {
	for _, f := range s.forks {
		if f.covers(base) || f.covers(base+length-1) ||
			(base < f.base && f.base < base+length) {
			return true
		}
	}
	return false
}

func buildSoakPlan(rng *rand.Rand) soakPlan {
	start := int64(100)
	heights := int64(12 + rng.IntN(13)) // 12..24 canonical blocks
	end := start + heights
	plan := soakPlan{
		start:       start,
		end:         end,
		stale:       map[int64]bool{},
		failHeights: map[int64]int64{},
	}

	for range rng.IntN(3) {
		for range 12 {
			base := start + 1 + int64(rng.IntN(int(end-start-6)))
			length := int64(1 + rng.IntN(3))
			if base+length > end-1 || plan.overlaps(base, length) {
				continue
			}
			plan.forks = append(plan.forks, soakFork{
				base:    base,
				length:  length,
				fetches: int64(1 + rng.IntN(2)),
				flip:    rng.IntN(4) == 0,
			})
			break
		}
	}

	for range rng.IntN(3) {
		height := start + int64(rng.IntN(int(heights)))
		if plan.failHeights[height] == 0 {
			plan.failHeights[height] = int64(1 + rng.IntN(2))
		}
	}
	if rng.IntN(2) == 0 {
		height := start + int64(rng.IntN(int(heights)))
		plan.stale[height] = true
		if plan.failHeights[height] == 0 {
			plan.failHeights[height] = 1
		}
	}

	if rng.IntN(4) == 0 {
		plan.floor = -2 // the mock reports an error; the descent uses its depth cap
	} else {
		plan.floor = start - int64(3+rng.IntN(10))
	}
	plan.workers = 1 + rng.IntN(4)
	plan.restarts = 1 + rng.IntN(2)
	return plan
}

// describe renders the plan for failure diagnostics.
func (s soakPlan) describe() string {
	var forks []string
	for _, f := range s.forks {
		shape := "fork"
		if f.flip {
			shape = "flip"
		}
		forks = append(forks, fmt.Sprintf("%d+%d/%dx%s",
			f.base, f.length, f.fetches, shape))
	}
	return fmt.Sprintf("start=%d end=%d workers=%d floor=%d restarts=%d forks=[%s] failHeights=%v stale=%v",
		s.start, s.end, s.workers, s.floor, s.restarts,
		strings.Join(forks, ", "), s.failHeights, s.stale)
}

// soakNode replays the plan as the RPC node: by-number fetches serve branch
// blocks while the height's counter is below the fork's budget and canonical
// content afterwards; the plan's data is never mutated so describe() stays
// truthful.
type soakNode struct {
	plan        soakPlan
	mu          sync.Mutex
	fetchCounts map[int64]int64
}

func (n *soakNode) serve(height int64) *servedBlock {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, f := range n.plan.forks {
		if !f.covers(height) {
			continue
		}
		if n.fetchCounts[height] < f.fetches {
			n.fetchCounts[height]++
			return &servedBlock{
				height:     height,
				hash:       soakForkHash(f, height),
				parentHash: soakForkParent(f, height),
			}
		}
		break
	}
	return &servedBlock{
		height:     height,
		hash:       canonicalHash(height),
		parentHash: canonicalHash(height - 1),
	}
}

// soakHashIndex builds the by-hash entries the walk-back descents could
// follow: the canonical chain and every branch block.
func soakHashIndex(plan soakPlan) map[string]*servedBlock {
	index := canonicalHashIndex(plan.start-5, plan.end+10)
	for _, f := range plan.forks {
		for height := f.base; height < f.base+f.length; height++ {
			index[strings.ToLower(soakForkHash(f, height))] = &servedBlock{
				height:     height,
				hash:       soakForkHash(f, height),
				parentHash: soakForkParent(f, height),
			}
		}
	}
	return index
}

// soakStorer is the scripted storer for the soak: it publishes stored hashes
// on success (the way a real store makes a height visible to
// GetStoredBlockHash), applies the plan's transient failures, and tracks the
// heights currently inside a store call for the height-aware watcher.
type soakStorer struct {
	reorg       *mockReorgHandler
	failHeights map[int64]int64

	mu       sync.Mutex
	inFlight map[int64]int
}

func newSoakStorer(plan soakPlan, reorg *mockReorgHandler) *soakStorer {
	failures := make(map[int64]int64, len(plan.failHeights))
	maps.Copy(failures, plan.failHeights)
	return &soakStorer{
		reorg:       reorg,
		failHeights: failures,
		inFlight:    map[int64]int{},
	}
}

func (s *soakStorer) Store(_ context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
	num := blockNumOfResult(result)
	s.mu.Lock()
	if remaining := s.failHeights[num]; remaining > 0 {
		s.failHeights[num] = remaining - 1
		s.mu.Unlock()
		return nil, fmt.Errorf("store exploded")
	}
	s.inFlight[num]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight[num]--
		if s.inFlight[num] == 0 {
			delete(s.inFlight, num)
		}
		s.mu.Unlock()
	}()

	s.reorg.mu.Lock()
	s.reorg.storedHashes[num] = extractBlockHash(result.Groups)
	s.reorg.mu.Unlock()
	return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
}

func (s *soakStorer) SignExisting(_ context.Context, _ chains.ConversionResult, _ string, _ int64) (string, error) {
	return "mock-sig-id", nil
}

func (s *soakStorer) heightInFlight(height int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[height] > 0
}

// anyInFlight reports whether any store call is currently running. A store
// that already returned cannot write anymore, so call-level tracking is the
// precise signal for the rollback invariant (no writes inside or after a
// rollback range); an item still queued for the pool cannot reach the store
// during an episode drain because the drain waits for its completion first.
func (s *soakStorer) anyInFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inFlight) > 0
}

// drainFailures zeroes the remaining transient failures before the settle
// phase: chaos-phase holes re-fill on re-invocation, but a fresh failure
// inside the settle run would starve the convergence predicate.
func (s *soakStorer) drainFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for height := range s.failHeights {
		s.failHeights[height] = 0
	}
}

// soakWatcher wraps the reorg handler with the two quiescence invariants the
// pool must hold: a stored-hash read at height h must not overlap an
// in-flight store of the same height (it must observe a stable value), and a
// rollback must never run while a store is in flight (nothing may land
// inside or after a rollback range). Reads at other heights while sibling
// stores run are legal (the pool-side failure handler and the pop gap
// fallback both do them), so the read check is height-aware; the blanket
// any-in-flight rule from TestParallelStores_DrainBeforeWalkBack would
// false-positive on them here.
type soakWatcher struct {
	defra.ReorgHandler

	heightInFlight func(height int64) bool
	anyInFlight    func() bool
	violations     atomic.Int64
}

func (w *soakWatcher) GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error) {
	if w.heightInFlight(blockNumber) {
		w.violations.Add(1)
	}
	return w.ReorgHandler.GetStoredBlockHash(ctx, blockNumber)
}

func (w *soakWatcher) RollbackBlocks(ctx context.Context, from, to int64, chainHashes map[int64]string) error {
	if w.anyInFlight() {
		w.violations.Add(1)
	}
	return w.ReorgHandler.RollbackBlocks(ctx, from, to, chainHashes)
}

// soakRollbackFn models pkg/defra/reorg_roller.go semantics on the mock: a
// height is deleted only when the rollback carries a verified new-chain hash
// for it that disagrees with what is stored; heights without a verified
// entry (canonical landed stores above a stale-pinned episode's descent, for
// example) are kept, so convergence is a real property and not exhaustion by
// blind purging.
func soakRollbackFn(reorg *mockReorgHandler) func(from, to int64, chainHashes map[int64]string) error {
	return func(from, to int64, chainHashes map[int64]string) error {
		reorg.mu.Lock()
		defer reorg.mu.Unlock()
		for height := from; height <= to; height++ {
			// The real roller keeps heights uncovered, or covered with an
			// empty hash, exactly as untouched.
			verified, ok := chainHashes[height]
			if !ok || verified == "" {
				continue
			}
			if stored := reorg.storedHashes[height]; stored != "" && stored != verified {
				delete(reorg.storedHashes, height)
			}
		}
		return nil
	}
}

func soakStoredHashes(reorg *mockReorgHandler) map[int64]string {
	reorg.mu.Lock()
	defer reorg.mu.Unlock()
	snapshot := make(map[int64]string, len(reorg.storedHashes))
	maps.Copy(snapshot, reorg.storedHashes)
	return snapshot
}

// soakConverged reports whether every canonical window height is stored with
// the canonical hash - the soak's headline property.
func soakConverged(reorg *mockReorgHandler, plan soakPlan) bool {
	stored := soakStoredHashes(reorg)
	for height := plan.start; height < plan.end; height++ {
		if stored[height] != canonicalHash(height) {
			return false
		}
	}
	return true
}

// soakRunOutcome reports one invocation's terminal error and whether only
// the watchdog (rather than the scenario's own trigger) brought the
// invocation down - a watchdog hit means the run was hung, which is always
// a failure.
type soakRunOutcome struct {
	err      error
	watchdog bool
}

// soakRun invokes ProcessBlocks on a fresh context (the restart shape: the
// same processor is called again after a cancelled run) and tears it down
// from a canceller goroutine: chaos invocations cancel once the invocation's
// own commits reach the given count, settle invocations cancel once the
// stored set has converged. A 20s watchdog cancels any hung run and flags it
// in the outcome, so a pool deadlock surfaces as an assertion failure
// instead of a dead test.
func soakRun(
	t *testing.T,
	p *ConcurrentBlockProcessor,
	rec *commitRecorder,
	reorg *mockReorgHandler,
	plan soakPlan,
	cancelAfterCommits int64,
) soakRunOutcome {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := len(rec.snapshot())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- p.ProcessBlocks(ctx, plan.start, rec.record)
	}()

	var watchdog atomic.Bool
	stopPolling := make(chan struct{})
	go func() {
		defer cancel()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case <-stopPolling:
				return
			case <-deadline:
				watchdog.Store(true)
				return
			case <-time.After(5 * time.Millisecond):
			}
			if cancelAfterCommits >= 0 && len(rec.snapshot()) >= base+int(cancelAfterCommits) {
				return
			}
			if soakConverged(reorg, plan) {
				return
			}
		}
	}()
	defer close(stopPolling)

	// The watchdog cancels at 20s; a hang the cancellation cannot unwind
	// still cannot block the test: once the watchdog has fired, a 3s grace
	// cap reports the hang through the outcome (watchdog=true fails before
	// the error assertion ever runs).
	for {
		select {
		case err := <-doneCh:
			return soakRunOutcome{err: err, watchdog: watchdog.Load()}
		case <-time.After(10 * time.Millisecond):
		}
		if watchdog.Load() {
			select {
			case err := <-doneCh:
				return soakRunOutcome{err: err, watchdog: true}
			case <-time.After(3 * time.Second):
				return soakRunOutcome{
					err:      fmt.Errorf("hung: the invocation did not return even after the watchdog cancellation"),
					watchdog: true,
				}
			}
		}
	}
}

// soakSeedRange resolves the seed window: the fixed default range plus the
// isolation override.
func soakSeedRange() (from, to int64) {
	from, to = 1, 30
	if raw := os.Getenv(soakSeedEnvVar); raw != "" {
		parts := strings.SplitN(raw, "-", 2)
		if v, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64); err == nil && v > 0 {
			from = v
		}
		to = from
		if len(parts) == 2 {
			if v, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); err == nil && v >= from {
				to = v
			}
		}
	}
	return from, to
}

// TestReorgSoak_Converges drives the seeded scenarios: randomized forks,
// store failures, stale pre-seeds, worker counts, and cancellations with
// restarts, then asserts convergence, quiescence invariants, and clean
// cancellations with the seed in every failure message.
func TestReorgSoak_Converges(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	from, to := soakSeedRange()
	require.LessOrEqual(t, int(to-from), 200, "keep the seed window bounded")
	for seed := from; seed <= to; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			runSoakSeed(t, seed)
		})
	}
}

func runSoakSeed(t *testing.T, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
	plan := buildSoakPlan(rng)

	reorg := &mockReorgHandler{storedHashes: map[int64]string{
		plan.start - 1: canonicalHash(plan.start - 1),
	}}
	for height := range plan.stale {
		reorg.storedHashes[height] = orphanHash(height)
	}
	reorg.rollbackFn = soakRollbackFn(reorg)

	storer := newSoakStorer(plan, reorg)
	node := &soakNode{plan: plan, fetchCounts: map[int64]int64{}}
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			return node.serve(height), nil
		},
		FetchBlockHeaderFn: headerLookup(soakHashIndex(plan)),
	}
	if plan.floor >= 0 {
		fetcher.FetchFinalizedBlockNumberFn = func(_ context.Context) (int64, error) {
			return plan.floor, nil
		}
	} else {
		fetcher.FetchFinalizedBlockNumberFn = func(_ context.Context) (int64, error) {
			return 0, fmt.Errorf("finalized height unavailable")
		}
	}

	rec := &commitRecorder{}
	watcher := &soakWatcher{ReorgHandler: reorg}
	watcher.heightInFlight = storer.heightInFlight
	p := NewConcurrentBlockProcessor(fetcher, chainConverter(), storer, watcher, plan.workers, 0)
	watcher.anyInFlight = storer.anyInFlight

	var outcomes []soakRunOutcome
	cancelCommits := int64(2 + rng.IntN(12))
	for range plan.restarts {
		outcomes = append(outcomes, soakRun(t, p, rec, reorg, plan, cancelCommits))
	}
	storer.drainFailures()
	for range 8 {
		outcomes = append(outcomes, soakRun(t, p, rec, reorg, plan, -1))
		if soakConverged(reorg, plan) {
			break
		}
	}

	for i, outcome := range outcomes {
		require.False(t, outcome.watchdog,
			"seed %d invocation %d hung: %s", seed, i+1, plan.describe())
		require.ErrorIs(t, outcome.err, context.Canceled,
			"seed %d invocation %d: %s", seed, i+1, plan.describe())
	}
	assert.True(t, soakConverged(reorg, plan),
		"seed %d: the stored set must converge to the canonical chain prefix (%s), stored=%v",
		seed, plan.describe(), soakStoredHashes(reorg))
	assert.Zero(t, watcher.violations.Load(),
		"seed %d: every stored-hash read must see a stable height and every rollback a quiescent pool (%s)",
		seed, plan.describe())
}
