//go:build acceptance
// +build acceptance

package benchmarking

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// defaultTargetBlockTime is BSC mainnet's block interval since the
	// Fermi hardfork (Jan 2026). The verdict asserts the pipeline keeps up
	// with the chain, so the default is a hard acceptance bar, not an
	// informational number.
	defaultTargetBlockTime = 450 * time.Millisecond

	// fixtureDir holds per-machine capture fixtures produced by
	// cmd/fetch_blocks; they are gitignored. Fixtures for every chain share
	// one directory and the filename prefix identifies the chain
	// (bsc_blocks_<from>_<to>.json).
	fixtureDir = "../testdata"

	// fixtureGlob matches this chain's captures so the newest fixture wins
	// when no explicit path is given.
	fixtureGlob = "bsc_blocks_*.json"

	// replayValueLogSizeMB matches the production badger value-log file size
	// default (defaultBadgerValueLogFileSizeMB in pkg/defradb, unexported),
	// carried in the config so the node-option callback below reads it the
	// same way the production buildNodeOptions does.
	replayValueLogSizeMB = 64

	// badgerVlogSizeShift is log2 of one mebibyte: the config field is in MB,
	// SetBadgerFileSize wants bytes.
	badgerVlogSizeShift = 20
)

// TestBSCReplayAcceptance answers "can we index BSC blocks at the tip": it
// replays a captured fixture through a mock JSON-RPC node into the full
// production pipeline (Fetcher → Converter → BlockHandler.Store) and asserts
// the average per-block processing time stays within the chain's block
// interval.
//
// The harness deliberately bypasses blocks_per_minute pacing — it measures
// raw pipeline capacity against the chain's BPS, so the processor's rate
// limiter must not soak up the measurement.
func TestBSCReplayAcceptance(t *testing.T) {
	fixturePath := resolveFixturePath(t)
	if fixturePath == "" {
		t.Skipf("no replay fixture found: run 'make bsc-bench-fetch' (needs GETH_RPC_URL) to capture one into %s, or point BSC_REPLAY_FIXTURE at an existing fixture", fixtureDir)
	}

	fx := loadReplayFixture(t, fixturePath)
	if len(fx.Blocks) == 0 {
		t.Skipf("fixture %s contains no blocks - recapture it", fixturePath)
	}

	target := resolveTargetBlockTime(t)
	blocks, numbers := capSample(t, fx, os.Getenv("BSC_REPLAY_MAX_BLOCKS"))

	logger.Testf("BSC replay acceptance: %d blocks from fixture %s, target %s per block", len(blocks), fixturePath, target)

	mockNode := newReplayRPCServer(fx)
	defer mockNode.Close()

	storePath := t.TempDir()
	cfg := newReplayConfig(mockNode.URL(), storePath)
	resolveReplayServices(t, cfg)

	// Embedded DefraDB with the BSC-prefixed schema. The ForChain variant is
	// required: LoadSchemaSDL returns the raw embedded Ethereum__Mainnet
	// content without the prefix swap, which would create the wrong
	// collections.
	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	sdl, err := schema.LoadSchemaSDLForChain(cols)
	require.NoError(t, err)

	// The node runs with the production bootstrap's node options: the
	// identity comes from the real file keyring at {storePath}/keys and is
	// set as the node identity, and badger gets the production value-log
	// file size. P2P stays off to keep the measurement free of libp2p
	// background traffic.
	nodeIdent, err := defradb.GetOrCreateNodeIdentity(cfg)
	require.NoError(t, err, "open the replay keyring")
	td := testutils.SetupTestDefraDBWithSchemaOpts(t, sdl, storePath,
		func(nb *options.NodeOptionsBuilder) { nb.DB().SetNodeIdentity(nodeIdent) },
		func(nb *options.NodeOptionsBuilder) {
			nb.Store().SetBadgerFileSize(cfg.DefraDB.Store.ValueLogFileSizeMB << badgerVlogSizeShift)
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

	timings := make(blockTimings, 0, len(blocks))
	for i, fb := range blocks {
		num, err := parseHexUint(fb.Number)
		require.NoError(t, err, "fixture block %d has an unparseable number", i)

		start := time.Now()
		bundle, err := fetcher.FetchBlock(ctx, int64(num))
		require.NoError(t, err, "replay fetch failed for block %d: the fixture must contain every block of the captured range", num)

		result, err := converter.Convert(ctx, bundle)
		require.NoError(t, err, "convert failed for block %d", num)

		_, err = handler.Store(ctx, result)
		require.NoError(t, err, "store failed for block %d", num)

		elapsed := time.Since(start)
		timings = append(timings, elapsed)
		if (i+1)%25 == 0 { //nolint:mnd
			logger.Testf("replayed %d/%d blocks (last: %s)", i+1, len(blocks), elapsed)
		}
	}

	// Stop the services before the report and assertions: the pruner must
	// not race the final count queries, and the snapshotter's stats are
	// final once stopped. Idempotent: the Cleanup registered at startup
	// becomes a no-op after this.
	services.stop()

	reportResults(t, fx, blocks, numbers, timings, target)
	logServiceStats(t, services)
	assertVerdict(t, fx, timings, target)
	assertCorrectness(t, td, ctx, cols, numbers, len(blocks), services.prunedBlocks())
}

// resolveFixturePath picks the fixture to replay: BSC_REPLAY_FIXTURE wins,
// otherwise the newest capture in testdata is used. Empty means none found.
func resolveFixturePath(t *testing.T) string {
	t.Helper()
	if explicit := os.Getenv("BSC_REPLAY_FIXTURE"); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			t.Fatalf("BSC_REPLAY_FIXTURE %s not readable: %v", explicit, err)
		}
		return explicit
	}

	matches, err := filepath.Glob(filepath.Join(fixtureDir, fixtureGlob))
	if err != nil || len(matches) == 0 {
		return ""
	}

	// Newest capture wins; the filename embeds the block range, not the
	// capture time, so modtime is the only reliable ordering.
	newest := matches[0]
	var newestMod time.Time
	for _, match := range matches {
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

// resolveTargetBlockTime parses BSC_TARGET_BLOCK_TIME when set (e.g. 750ms
// for a Maxwell-era fixture) and falls back to the current mainnet interval.
func resolveTargetBlockTime(t *testing.T) time.Duration {
	t.Helper()
	if raw := os.Getenv("BSC_TARGET_BLOCK_TIME"); raw != "" {
		d, err := time.ParseDuration(raw)
		require.NoError(t, err, "invalid BSC_TARGET_BLOCK_TIME %q: want a duration like 450ms or 750ms", raw)
		return d
	}
	return defaultTargetBlockTime
}

// capSample applies the BSC_REPLAY_MAX_BLOCKS cap for slow hosts, returning
// the capped blocks and their parsed numbers (kept aligned for the report).
func capSample(t *testing.T, fx *replayFixture, maxBlocksEnv string) ([]fixtureBlock, []uint64) {
	t.Helper()
	blocks := fx.Blocks
	if maxBlocksEnv != "" {
		n, err := parsePositiveInt(maxBlocksEnv)
		require.NoError(t, err, "invalid BSC_REPLAY_MAX_BLOCKS %q: want a positive integer", maxBlocksEnv)
		if n > 0 && n < len(blocks) {
			blocks = blocks[:n]
			logger.Testf("BSC_REPLAY_MAX_BLOCKS=%d caps the sample", n)
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
// the report forms a clean block; lines longer than the width get no padding
// (never truncated).
func reportResults(t *testing.T, fx *replayFixture, blocks []fixtureBlock, numbers []uint64, timings blockTimings, target time.Duration) {
	t.Helper()
	avg := timings.avg()

	const width = 100

	pad := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		logger.Test(line + strings.Repeat(" ", max(width-len(line), 0)))
	}

	// Banners match the content width exactly.
	logger.Test(strings.Repeat("=", width))
	pad("Blocks Replayed: %d - %d   (%d blocks)", fx.Meta.From, fx.Meta.To, len(blocks))
	pad("Target:  %s", target)
	pad("Average:  %s", avg)
	pad("Headroom:    %.1f%%", timings.headroomPct(target))
	pad("Min: %s || p50: %s || p95: %s || Max: %s",
		timings.min(), timings.percentile(50), timings.percentile(95), timings.max())
	logger.Test("Outliers:")
	for _, line := range timings.outlierLines(numbers, avg) {
		pad("- %s", line)
	}
	logger.Test(strings.Repeat("=", width))
}

// assertVerdict is the hard acceptance assertion: the average per-block
// processing time must stay within the chain's block interval, or the chain
// outruns the indexer and blocks pile up forever.
func assertVerdict(t *testing.T, fx *replayFixture, timings blockTimings, target time.Duration) {
	t.Helper()
	avg := timings.avg()
	require.LessOrEqual(t, avg, target,
		"acceptance FAILED: average block processing time %s exceeds the %s block interval (fixture blocks %d..%d) - the pipeline cannot keep up with BSC at the tip",
		avg, target, fx.Meta.From, fx.Meta.To)
}

// assertCorrectness is the "can index" half of the verdict: every processed
// block must be present in DefraDB unless the pruner removed it, and
// signature docs must exist for the stored range.
func assertCorrectness(t *testing.T, td *testutils.TestDefraDB, ctx context.Context, cols chains.Collections, numbers []uint64, processed int, pruned int64) {
	t.Helper()

	// The capture is a contiguous range and the cap keeps a prefix of it, so
	// the stored range is the first and last processed block number. Pruned
	// blocks come off the low end of that range, so the expected row count is
	// everything processed minus what the pruner deleted.
	first, last := int64(numbers[0]), int64(numbers[len(numbers)-1])
	expected := processed - int(pruned)

	blockCount, err := graphqlCountInRange(ctx, td.Node, mustBlockCollection(t, cols),
		constants.NumberFieldName, first, last, processed+1)
	require.NoError(t, err)
	assert.Equal(t, expected, blockCount,
		"block count in the stored range must equal blocks processed minus blocks the pruner removed (duplicate stores are rejected, so a mismatch means a block silently failed or was pruned unexpectedly)")

	sigCount, err := graphqlCountInRange(ctx, td.Node, mustSignatureCollection(t, cols),
		constants.BlockNumberFieldName, first, last, processed+1)
	require.NoError(t, err)
	assert.Greater(t, sigCount, 0,
		"no BlockSignature docs were written for the stored range")
	logger.Testf("✓ correctness: %d blocks stored, %d block signatures (expected %d blocks after pruning %d)",
		blockCount, sigCount, expected, pruned)
}

// mustBlockCollection resolves the chain's block collection name.
func mustBlockCollection(t *testing.T, cols chains.Collections) string {
	t.Helper()
	name, err := cols.GetCollection(chains.TypeBlock)
	require.NoError(t, err)
	return name
}

// mustSignatureCollection resolves the chain's block-signature collection name.
func mustSignatureCollection(t *testing.T, cols chains.Collections) string {
	t.Helper()
	name, err := cols.GetCollection(chains.TypeBlockSignature)
	require.NoError(t, err)
	return name
}
