//go:build acceptance
// +build acceptance

package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// defaultTargetBlockTime is Ethereum mainnet's block interval.
	// The verdict asserts the pipeline keeps up with the chain, so the default is a hard acceptance bar, not an
	// informational number.
	defaultTargetBlockTime = 12 * time.Second

	// fixtureDir holds per-machine capture fixtures produced by
	// cmd/fetch_blocks; they are gitignored. Fixtures for every chain share
	// one directory and the filename prefix identifies the chain
	// (ethereum_blocks_<from>_<to>.json).
	fixtureDir = "../testdata"

	// fixtureGlob matches this chain's captures so the newest complete
	// fixture wins when no explicit path is given. resolveFixturePath skips
	// _partial captures; they are still loadable via ETHEREUM_REPLAY_FIXTURE.
	// resolveFixturePath skips _partial captures; they are still loadable
	// via ETHEREUM_REPLAY_FIXTURE.
	fixtureGlob = "ethereum_blocks_*.json"

	// fixturePartialSuffix marks an interrupted capture's fixture, mirroring
	// the constant in cmd/fetch_blocks (test packages cannot import a main
	// package, so the on-disk naming is duplicated here).
	fixturePartialSuffix = "_partial"
)

// TestEthereumReplayAcceptance answers "can we index Ethereum blocks at the tip": it
// replays a captured fixture through a mock JSON-RPC node into the full
// production pipeline and drives it with the real production orchestration —
// the indexer's ConcurrentBlockProcessor, with the worker count the shipped
// config carries. Output is Fetcher → Converter → BlockHandler.Store under
// the processor's worker pool, retry classification, and in-order commit.
//
// The verdict is a throughput gate: the effective per-block interval (wall
// clock from first dispatch to the last block's in-order commit, divided by
// the block count) must stay within the chain's block interval — the same
// question as "do blocks pile up forever", which concurrency answers
// differently than per-block latency does.
//
// blocks_per_minute pacing stays deliberately bypassed (0 disables the
// processor's rate limiter) — this measures raw pipeline capacity against
// the chain's BPS, so the rate limiter must not soak up the measurement.
func TestEthereumReplayAcceptance(t *testing.T) {
	fixturePath := resolveFixturePath(t)
	if fixturePath == "" {
		t.Skipf("no replay fixture found: run 'make replay-fetch' (needs GETH_RPC_URL) to capture one into %s, or point ETHEREUM_REPLAY_FIXTURE at an existing fixture", fixtureDir)
	}

	fx := loadReplayFixture(t, fixturePath)
	if len(fx.Blocks) == 0 {
		t.Skipf("fixture %s contains no blocks - recapture it", fixturePath)
	}

	target := resolveTargetBlockTime(t)
	blocks, numbers := capSample(t, fx, os.Getenv("ETHEREUM_REPLAY_MAX_BLOCKS"))

	logger.Testf("Ethereum replay acceptance: %d blocks from fixture %s, target %s per block", len(blocks), fixturePath, target)

	mockNode := newReplayRPCServer(fx)
	defer mockNode.Close()

	storePath := t.TempDir()
	cfg := newReplayConfig(t, mockNode.URL(), storePath)
	resolveReplayServices(t, cfg)

	// Embedded DefraDB with the Ethereum-prefixed schema. The ForChain variant is
	// required: LoadSchemaSDL returns the raw embedded Ethereum__Mainnet
	// content without the prefix swap, which would create the wrong
	// collections.
	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	sdl, err := schema.LoadSchemaSDLForChain(cols)
	require.NoError(t, err)

	// The node runs with the production bootstrap's node options: the
	// identity comes from the real file keyring at {storePath}/keys and is
	// set as the node identity, and badger gets the value-log file size the
	// shipped config carries (defra.go reads the same field). P2P stays off
	// to keep the measurement free of libp2p background traffic.
	nodeIdent, err := defradb.GetOrCreateNodeIdentity(cfg)
	require.NoError(t, err, "open the replay keyring")
	td := testutils.SetupTestDefraDBWithSchemaOpts(t, sdl, storePath,
		func(nb *options.NodeOptionsBuilder) { nb.DB().SetNodeIdentity(nodeIdent) },
		func(nb *options.NodeOptionsBuilder) {
			nb.Store().SetBadgerFileSize(defradb.ValueLogFileSizeBytes(cfg))
		},
	)

	// The signing identity is required: the BlockSignature correctness
	// assertion below depends on signature docs being written. This resolves
	// the same keyring entry the node options set, mirroring the indexer's
	// identity-context wiring.
	ctx, err := defradb.GetIdentityContext(context.Background(), cfg)
	require.NoError(t, err)

	client, err := evm.NewEthereumClient(ctx, mockNode.URL(), "", "", "", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	fetcher := evm.NewFetcher(client, cfg.Indexer.ReceiptWorkers)
	converter := evm.NewConverter(cfg)
	handler, err := defra.NewBlockHandler(td.Node, cfg.Indexer.MaxDocsPerTxn)
	require.NoError(t, err)

	// Background services run beside the timing loop: the pruner deletes
	// past retention and the snapshotter archives to disk, so their IO lands
	// in the samples the way production background load does.
	services := startReplayServices(t, cfg, td.Node, converter, handler, ctx)

	// The worker count comes from the shipped yaml (concurrent_blocks), the
	// same field production reads: the scrub above keeps any stray
	// INDEXER_* env override from changing the measured configuration.
	workers := cfg.Indexer.ConcurrentBlocks
	require.Greater(t, workers, 0, "config must enable at least one concurrent worker")

	timings := newReplayTimings()
	processor := indexer.NewConcurrentBlockProcessor(
		&timedFetcher{inner: fetcher, timings: timings},
		&timedConverter{inner: converter, timings: timings},
		&timedStorer{inner: handler, timings: timings},
		nil, // no reorgs in a replay: the contiguous fixture range cannot fork, and nil makes any detected reorg fatal instead
		workers,
		0, // pacing off: blocks_per_minute's rate limiter must not soak up the measurement
	)

	// The dispatcher runs to infinity; the run ends when the last fixture
	// block commits in order — at that point every fixture block is stored,
	// so the ctx cancel is the expected termination, not a failure. The
	// wall clock stops at that same commit: ProcessBlocks' return additionally
	// waits out the in-flight beyond-tip fetch retries (≤ a few seconds),
	// which are not part of the measurement.
	first, last := int64(numbers[0]), int64(numbers[len(numbers)-1])
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// The dispatcher's only cancel path is the last block's commit. A block
	// that fails inside the processor (fetch/convert/store error) is logged
	// as a warning instead of committed, so nextToCommit never advances and
	// the run would hang to the go-test timeout. The watchdog keeps that
	// honest: no commit for 30s while blocks remain → cancel, and the
	// missing timing sample then names the stuck block.
	const (
		stallLimit      = 30 * time.Second
		stallCheckEvery = 5 * time.Second
	)
	var (
		lastCommit   atomic.Int64
		commitCount  atomic.Int64
		watchdogStop = make(chan struct{})
	)
	defer close(watchdogStop)
	lastCommit.Store(time.Now().UnixNano())
	go func() {
		tick := time.NewTicker(stallCheckEvery)
		defer tick.Stop()
		for {
			select {
			case <-watchdogStop:
				return
			case <-tick.C:
				if commitCount.Load() >= int64(len(blocks)) {
					continue // all committed; the last-block cancel is winding the run down
				}
				if time.Since(time.Unix(0, lastCommit.Load())) > stallLimit {
					logger.Testf("no commit for %s with %d/%d blocks committed - cancelling the stalled replay", stallLimit, commitCount.Load(), len(blocks))
					cancelRun()
				}
			}
		}
	}()

	var wallEnd time.Time
	wallStart := time.Now()
	err = processor.ProcessBlocks(runCtx, first, func(blockNum int64) {
		done := commitCount.Add(1)
		lastCommit.Store(time.Now().UnixNano())
		if done%25 == 0 {
			logger.Testf("committed %d/%d blocks (last: %d)", done, len(blocks), blockNum)
		}
		if blockNum == last {
			wallEnd = time.Now()
			cancelRun()
		}
	})
	if !errors.Is(err, context.Canceled) {
		require.NoError(t, err, "replay processor terminated unexpectedly")
	}
	wall := wallEnd.Sub(wallStart)

	timingsList, err := timings.snapshotOrdered(first, last)
	require.NoError(t, err, "every replayed block must have a timing sample - a missing sample means a block never stored (blocked by a failed fetch/convert/store or the stall watchdog)")
	require.Len(t, timingsList, len(blocks),
		"sample count must equal the fixture block count - a missing sample means a block failed inside the processor")

	phaseAvg, err := timings.phaseAverages(first, last)
	require.NoError(t, err, "every replayed block must have complete phase records - fetch success precedes convert, convert precedes any store, so a total sample implies all three")

	// Stop the services before the report and assertions: the pruner must
	// not race the final count queries, and the snapshotter's stats are
	// final once stopped. Idempotent: the Cleanup registered at startup
	// becomes a no-op after this.
	services.stop()

	reportResults(t, blocks, numbers, timingsList, phaseAvg, workers, wall, target)
	logServiceStats(t, services)
	assertVerdict(t, numbers, wall, workers, target)
	assertCorrectness(t, td, ctx, cols, converter, numbers, len(blocks), services.prunedBlocks())
}

// resolveFixturePath picks the fixture to replay: ETHEREUM_REPLAY_FIXTURE wins,
// otherwise the newest capture in testdata is used. Empty means none found.
func resolveFixturePath(t *testing.T) string {
	t.Helper()
	if explicit := os.Getenv("ETHEREUM_REPLAY_FIXTURE"); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			t.Fatalf("ETHEREUM_REPLAY_FIXTURE %s not readable: %v", explicit, err)
		}
		return explicit
	}

	matches, err := filepath.Glob(filepath.Join(fixtureDir, fixtureGlob))
	if err != nil || len(matches) == 0 {
		return ""
	}

	// Newest complete capture wins; the filename embeds the block range,
	// not the capture time, so modtime is the only reliable ordering.
	// _partial files are skipped: an interrupted capture must neither
	// preempt the newest complete capture (a newer modtime would win) nor
	// - when the interrupt hit before any block saved - starve the suite of
	// the complete fixture sitting in the same directory. They still run on
	// purpose via ETHEREUM_REPLAY_FIXTURE.
	newest := ""
	var newestMod time.Time
	for _, match := range matches {
		if strings.Contains(match, fixturePartialSuffix) {
			continue
		}
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newest = match
			newestMod = info.ModTime()
		}
	}
	return newest
}

// resolveTargetBlockTime parses ETHEREUM_TARGET_BLOCK_TIME when set (e.g. 10s
// to gate a slow host harder) and falls back to the current mainnet interval.
func resolveTargetBlockTime(t *testing.T) time.Duration {
	t.Helper()
	if raw := os.Getenv("ETHEREUM_TARGET_BLOCK_TIME"); raw != "" {
		d, err := time.ParseDuration(raw)
		require.NoError(t, err, "invalid ETHEREUM_TARGET_BLOCK_TIME %q: want a duration like 10s or 12s", raw)
		return d
	}
	return defaultTargetBlockTime
}

// capSample applies the ETHEREUM_REPLAY_MAX_BLOCKS cap for slow hosts, returning
// the capped blocks and their parsed numbers (kept aligned for the report).
func capSample(t *testing.T, fx *replayFixture, maxBlocksEnv string) ([]fixtureBlock, []uint64) {
	t.Helper()
	blocks := fx.Blocks
	if maxBlocksEnv != "" {
		n, err := parsePositiveInt(maxBlocksEnv)
		require.NoError(t, err, "invalid ETHEREUM_REPLAY_MAX_BLOCKS %q: want a positive integer", maxBlocksEnv)
		if n > 0 && n < len(blocks) {
			blocks = blocks[:n]
			logger.Testf("ETHEREUM_REPLAY_MAX_BLOCKS=%d caps the sample", n)
		}
	}

	numbers := make([]uint64, len(blocks))
	for i, fb := range blocks {
		num, err := parseHexUint(fb.Number)
		require.NoError(t, err)
		numbers[i] = num
	}
	return blocks, numbers
}

// parsePositiveInt parses a positive integer or returns an error.
func parsePositiveInt(raw string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", n)
	}
	return n, nil
}

// reportResults prints the timing report line by line in the replay
// report's format. Every content line is space-padded to the banner width so
// the two sections align; lines longer than the width get no padding (never
// truncated). The report splits in two: the throughput section (concurrency,
// total time, effective per-block interval) backs the acceptance verdict,
// while the sequential block processing stats are per-block phase sums:
// the time the pipeline spent working each block, queue wait between the
// processor's pools excluded. The phase line decomposes the average into
// fetch / convert / store; the store share is the throughput ceiling if
// stores are ever serialized. Its block range is the replayed sample's
// range, not the fixture's full range.
func reportResults(t *testing.T, blocks []fixtureBlock, numbers []uint64, timings blockTimings, phaseAvg replayPhases, workers int, wall time.Duration, target time.Duration) {
	t.Helper()
	avg := timings.avg()
	effective := wall / time.Duration(len(blocks))

	const width = 100

	pad := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		logger.Test(line + strings.Repeat(" ", max(width-len(line), 0)))
	}

	// Top and bottom banners close the report; the underscore divider
	// separates the throughput section from the per-block stats. All three
	// match the content width exactly.
	logger.Test(strings.Repeat("=", width))
	// The replayed sample's own range, not the fixture's full range: a
	// ETHEREUM_REPLAY_MAX_BLOCKS cap trims the prefix, so the fixture's
	// To would name blocks this run never touched.
	pad("Blocks Replayed: %d - %d   (%d blocks)", numbers[0], numbers[len(numbers)-1], len(blocks))
	pad("Concurrent Blocks: %d", workers)
	pad("Total Time:  %s", wall)
	pad("Target Throughput:  %s / block", target)
	pad("Effective Throughput:  %s / block", effective)
	pad("Headroom:    %.1f%%", headroomPct(target, effective))
	pad("Processing Budget (Network Latency, P2P, State Queries): %s / block", target-effective)

	logger.Test(strings.Repeat("_", width))
	pad("Sequential Block Processing Stats:")
	pad(" -  Average Block Time:  %s", avg)
	pad(" -  Min: %s || p50: %s || p95: %s || Max: %s",
		timings.min(), timings.percentile(50), timings.percentile(95), timings.max())
	pad(" -  Average Phase Times:  fetch %s || convert %s || store %s", phaseAvg.fetch, phaseAvg.convert, phaseAvg.store)
	pad(" - Outliers:")
	for _, line := range timings.outlierLines(numbers, avg) {
		pad("    - %s", line)
	}
	logger.Test(strings.Repeat("=", width))
}

// headroomPct returns how much headroom the measured effective interval
// leaves below the target, as a percentage of the target (negative when the
// target is missed).
func headroomPct(target, effective time.Duration) float64 {
	return float64(target-effective) / float64(target) * 100
}

// assertVerdict is the hard acceptance assertion: the effective per-block
// interval — wall clock from first dispatch to the last in-order commit,
// divided by the block count — must stay within the chain's block interval,
// or the chain outruns the indexer and blocks pile up forever. Per-block
// latency is not gated: contention inflates individual latencies even when
// the concurrent pipeline comfortably keeps up. Its failure message names
// the replayed sample's range (numbers may be a capped prefix of the
// fixture), not the fixture's full range.
func assertVerdict(t *testing.T, numbers []uint64, wall time.Duration, workers int, target time.Duration) {
	t.Helper()
	blockCount := len(numbers)
	effective := wall / time.Duration(blockCount)
	first, last := numbers[0], numbers[len(numbers)-1]
	require.LessOrEqual(t, effective, target,
		"acceptance FAILED: effective per-block interval %s exceeds the %s block interval at %d workers (blocks %d..%d) - the concurrent pipeline cannot keep up with Ethereum at the tip",
		effective, target, workers, first, last)
}

// assertCorrectness is the "can index" half of the verdict, and it reads the
// store itself: the block collection must fill the whole range from the
// lowest stored block to the last with no gaps, and every block in that
// range must carry a BlockSignature (a signed block is a complete block).
// The verdict does not consume the pruner's block counter: the counter is a
// deletion-event tally that can count one block under both of the pruner's
// strategies, so a healthy store can legitimately disagree with
// `processed - pruned`. The counter still feeds an informational
// disagreement line below.
func assertCorrectness(t *testing.T, td *testutils.TestDefraDB, ctx context.Context, cols chains.Collections, converter chains.Converter, numbers []uint64, processed int, pruned int64) {
	t.Helper()

	// The capture is a contiguous range and the cap keeps a prefix of it, and
	// pruning only removes blocks from the low end, so the surviving store is
	// a contiguous [lowest, last]. `lowest` comes from the same production
	// query the pruner and snapshotter use, so the verdict reads the store
	// exactly the way the services do.
	first, last := int64(numbers[0]), int64(numbers[len(numbers)-1])
	lowest, err := converter.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err, "query the lowest stored block: the store holds no blocks, so nothing was indexed")
	require.GreaterOrEqual(t, lowest, first, "nothing can be stored below the fixture's first block %d", first)
	expected := int(last - lowest + 1)

	blockCount, err := graphqlCountInRange(ctx, td.Node, cols.BlockCollection(),
		constants.NumberFieldName, lowest, last, processed+1)
	require.NoError(t, err)
	assert.Equal(t, expected, blockCount,
		"stored blocks must fill %d-%d with no gaps - a shortfall means a block silently failed or was pruned inside the range",
		lowest, last)

	sigCount, err := graphqlCountInRange(ctx, td.Node, cols.BlockSignatureCollection(),
		constants.BlockNumberFieldName, lowest, last, processed+1)
	require.NoError(t, err)
	// The pruner deletes a block's signature together with its docs (the
	// queue tracks it per block), so the signature rows must match the block
	// rows one for one. A shortfall names the real damage, not a counting
	// error: a block that was committed through the already-exists path
	// without a signature counts toward zero here. This is the assertion
	// that catches lost blocks under concurrent write contention.
	assert.Equal(t, expected, sigCount,
		"every stored block in %d-%d must carry a BlockSignature - a shortfall means a block was committed with a partial store or its signature could not be created",
		lowest, last)
	logger.Testf("✓ correctness: blocks %d-%d stored with no gaps, %d block signatures",
		lowest, last, sigCount)

	// Informational, never failed: distinct pruned blocks are exactly
	// lowest - first (pruning only removes a low-end prefix), so the
	// pruner's counter should agree with the store history. A disagreement
	// reports the inflated tally instead of turning the indexer's verdict
	// into a service bookkeeping check.
	if pruned != lowest-first {
		logger.Testf("pruner counter disagreement: counted %d blocks, %d measured from the store (lowest %d)",
			pruned, lowest-first, lowest)
	}
}
