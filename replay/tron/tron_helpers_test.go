//go:build acceptance
// +build acceptance

package replay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/replay"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/stretchr/testify/require"
)

// tronFixtureDir holds per-machine capture fixtures produced by
// cmd/fetch_blocks --adapter tron; they are gitignored. Fixtures for every
// chain share one directory and the filename prefix identifies the chain
// (tron_blocks_<from>_<to>.json). From replay/tron the shared directory is
// one level up, like it is from replay/evm.
const tronFixtureDir = "../testdata"

// Fixture types duplicated from cmd/fetch_blocks: test packages cannot
// import a main package, so the on-disk format is mirrored here. Keep both
// sides in sync when the capture format changes.
//
// Receipts is always a JSON array of receipt objects (the capture normalizes
// the wallet API's empty-object answer), so the replay serves the single
// wallet receipts path uniformly.

// tronFixtureBlock holds one block's verbatim node responses.
type tronFixtureBlock struct {
	Number      string          `json:"number"`       // decimal block height (the wallet API has no hex encoding)
	Block       json.RawMessage `json:"block"`        // getblockbynum body, verbatim
	Receipts    json.RawMessage `json:"receipts"`     // gettransactioninfobyblocknum body, verbatim
	ReceiptMode string          `json:"receipt_mode"` // "block"
}

// tronFixtureMeta describes the capture; it carries no endpoint or key
// material.
type tronFixtureMeta struct {
	Chain          string `json:"chain"`
	Network        string `json:"network"`
	From           uint64 `json:"from"`
	To             uint64 `json:"to"`
	BlockCount     int    `json:"block_count"`
	CapturedAt     string `json:"captured_at"`
	CapturePartial bool   `json:"capture_partial,omitempty"`
}

// tronReplayFixture is the on-disk format produced by cmd/fetch_blocks
// --adapter tron.
type tronReplayFixture struct {
	Meta   tronFixtureMeta    `json:"meta"`
	Blocks []tronFixtureBlock `json:"blocks"`
}

// TestMain initializes the console-only logger for the acceptance suite.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// loadTronFixture reads and validates a capture fixture for the Tron suite.
func loadTronFixture(t *testing.T, path string) *tronReplayFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}

	var fx tronReplayFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	// The chain label is free-form (it is typed at capture time), so match
	// case-insensitively; collection prefixes remain canonical downstream.
	if !strings.EqualFold(fx.Meta.Chain, "tron") {
		t.Fatalf("fixture %s is for chain %q, want tron", path, fx.Meta.Chain)
	}
	// Partial captures replay fine on purpose (the FIXTURE env), but
	// the verdict covers only what the file contains, so keep that visible:
	// an unexplained 20-block run would otherwise pass unnoticed.
	if fx.Meta.CapturePartial {
		logger.Testf("WARNING: fixture %s is a partial capture (%d blocks); the verdict covers only what it contains", path, len(fx.Blocks))
	}
	return &fx
}

// tronWalletServer dispatches the production client's wallet surface
// (pkg/chains/tron/tron_client.go) from the fixture, serving the node's body
// bytes verbatim. The tip is the highest fixture block; heights beyond it
// serve the empty object, the wallet API's not-found classification, so the
// processor's beyond-tip retries see the same answer a live node gives.
// Endpoint paths are duplicated from pkg/chains/tron/constants.go
// (unexported there) so this test package stays independent of the adapter.
type tronWalletServer struct {
	server *httptest.Server
	blocks map[int64]tronFixtureBlock  // decimal height -> fixture block
	byID   map[string]tronFixtureBlock // blockID -> fixture block, for the reorg walk-back read
}

// newTronWalletServer indexes the fixture and starts the mock node.
func newTronWalletServer(fx *tronReplayFixture) *tronWalletServer {
	s := &tronWalletServer{
		blocks: make(map[int64]tronFixtureBlock, len(fx.Blocks)),
		byID:   make(map[string]tronFixtureBlock, len(fx.Blocks)),
	}
	for _, fb := range fx.Blocks {
		num, err := strconv.ParseInt(fb.Number, 10, 64)
		if err != nil {
			continue // unreachable by number; an unparsable height would have failed capture
		}
		s.blocks[num] = fb
		var block struct {
			BlockID string `json:"blockID"`
		}
		if json.Unmarshal(fb.Block, &block) == nil && block.BlockID != "" {
			s.byID[block.BlockID] = fb
		}
	}

	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// handle routes one wallet-API request against the fixture index.
func (s *tronWalletServer) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Num   int64  `json:"num"`
		Value string `json:"value"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	w.Header().Set("Content-Type", "application/json")
	var out []byte
	switch r.URL.Path {
	case "/wallet/getnowblock", "/walletsolidity/getnowblock":
		out = s.tip().Block
	case "/wallet/getblockbynum":
		if fb, ok := s.blocks[req.Num]; ok {
			out = fb.Block
		} else {
			out = []byte(`{}`)
		}
	case "/wallet/gettransactioninfobyblocknum":
		if fb, ok := s.blocks[req.Num]; ok {
			out = fb.Receipts
		} else {
			out = []byte(`[]`)
		}
	case "/wallet/getblockbyid":
		if fb, ok := s.byID[req.Value]; ok {
			out = fb.Block
		} else {
			out = []byte(`{}`)
		}
	default:
		http.Error(w, "unexpected wallet path "+r.URL.Path, http.StatusNotFound)
		return
	}
	// RawMessage writes verbatim: the mock serves exactly what the node
	// served when the fixture was captured.
	_, _ = w.Write(out)
}

// tip returns the highest fixture block (zero value when empty; the suite
// skips empty fixtures before building the server).
func (s *tronWalletServer) tip() tronFixtureBlock {
	var highest int64
	var fb tronFixtureBlock
	for num, block := range s.blocks {
		if num > highest {
			highest, fb = num, block
		}
	}
	return fb
}

// Close shuts the mock node down.
func (s *tronWalletServer) Close() { s.server.Close() }

// URL returns the mock node's base URL.
func (s *tronWalletServer) URL() string { return s.server.URL }

// capSampleTron applies the suite's REPLAY_MAX_BLOCKS cap for slow hosts,
// returning the capped blocks and their parsed numbers (kept aligned for the
// report). Numbers parse as decimal: the wallet API has no hex encoding.
func capSampleTron(t *testing.T, fx *tronReplayFixture, maxBlocksEnvName string) ([]tronFixtureBlock, []uint64) {
	t.Helper()
	blocks := fx.Blocks
	if raw := os.Getenv(maxBlocksEnvName); raw != "" {
		n, err := replay.ParsePositiveInt(raw)
		require.NoError(t, err, "invalid %s %q: want a positive integer", maxBlocksEnvName, raw)
		if n > 0 && n < len(blocks) {
			blocks = blocks[:n]
			logger.Testf("%s=%d caps the sample", maxBlocksEnvName, n)
		}
	}

	numbers := make([]uint64, len(blocks))
	for i, fb := range blocks {
		num, err := strconv.ParseUint(fb.Number, 10, 64)
		require.NoError(t, err)
		numbers[i] = num
	}
	return blocks, numbers
}

// resolveTronTargetBlockTime parses the suite's TARGET_BLOCK_TIME when set
// (for example 2s to gate a slow host harder) and falls back to Tron's 3s
// mainnet block interval. Duplicated from the EVM suite's resolver: test
// packages in different directories cannot share unexported helpers, and
// promoting it to the shared harness would churn the EVM suites for ten
// lines.
func resolveTronTargetBlockTime(t *testing.T, envName string, def time.Duration, durationHint string) time.Duration {
	t.Helper()
	if raw := os.Getenv(envName); raw != "" {
		d, err := time.ParseDuration(raw)
		require.NoError(t, err, "invalid %s %q: want a duration like %s", envName, raw, durationHint)
		return d
	}
	return def
}

// tronReplaySpec carries everything that differs between the chain suites,
// the Tron counterpart of the EVM suites' replaySpec: the chain label used
// for config validation and the verdict; the shipped config file the
// measurement boots from; the expected network identity inside that config;
// the env prefix composing the suite's variables (TRON_TARGET_BLOCK_TIME,
// TRON_REPLAY_MAX_BLOCKS, ...); the fixture glob; the target block interval
// with its parse error hint; and the make target the skip message points at
// for fixture capture.
type tronReplaySpec struct {
	chainLabel         string
	configPath         string
	expectedNetwork    string
	envPrefix          string
	fixtureGlob        string
	defaultTarget      time.Duration
	targetDurationHint string
	skipHint           string
}

// runTronReplay answers "can we index Tron blocks at the tip": it replays a
// captured fixture through a mock wallet-API node into the full production
// pipeline and drives it with the real production orchestration, the
// indexer's ConcurrentBlockProcessor, with the worker count the shipped
// config carries. Output is Fetcher -> Converter -> BlockHandler.Store under
// the processor's worker pool, retry classification, and in-order commit.
//
// The verdict is a throughput gate: the effective per-block interval (wall
// clock from first dispatch to the last block's in-order commit, divided by
// the block count) must stay within the chain's block interval, which is the
// same question as "do blocks pile up forever", a question concurrency
// answers differently than per-block latency does.
//
// blocks_per_minute pacing stays deliberately bypassed (0 disables the
// processor's rate limiter): this measures raw pipeline capacity against the
// chain's BPS, so the rate limiter must not soak up the measurement.
//
// The wallet API has a single per-block receipts endpoint and no fallback
// path, so the Tron flow runs once per block where the EVM flow runs its
// batch/per-tx receipt-mode double-run.
func runTronReplay(t *testing.T, spec tronReplaySpec) {
	fixtureEnv := spec.envPrefix + "_REPLAY_FIXTURE"
	fixturePath := replay.ResolveFixturePath(t, tronFixtureDir, spec.fixtureGlob, fixtureEnv)
	if fixturePath == "" {
		t.Skipf("no replay fixture found: run '%s' (needs TRON_RPC_URL) to capture one into %s, or point %s at an existing fixture", spec.skipHint, tronFixtureDir, fixtureEnv)
	}

	fx := loadTronFixture(t, fixturePath)
	if len(fx.Blocks) == 0 {
		t.Skipf("fixture %s contains no blocks - recapture it", fixturePath)
	}

	targetEnv := spec.envPrefix + "_TARGET_BLOCK_TIME"
	target := resolveTronTargetBlockTime(t, targetEnv, spec.defaultTarget, spec.targetDurationHint)
	blocks, numbers := capSampleTron(t, fx, spec.envPrefix+"_REPLAY_MAX_BLOCKS")

	logger.Testf("%s replay acceptance: %d blocks from fixture %s, target %s per block", spec.chainLabel, len(blocks), fixturePath, target)

	mockNode := newTronWalletServer(fx)
	defer mockNode.Close()

	storePath := t.TempDir()
	cfg := replay.NewReplayConfig(t, spec.configPath, spec.chainLabel, spec.expectedNetwork, storePath,
		func(cfg *config.Config) {
			// The mock server replaces the endpoint; SolidityURL empty makes
			// the production client resolve /walletsolidity/* to the same
			// host, which the mock serves. The ${TRON_*} yaml placeholders
			// are blanked so no literal leaks to a future consumer.
			cfg.Tron.NodeURL = mockNode.URL()
			cfg.Tron.SolidityURL = ""
			cfg.Tron.APIKey = ""
			cfg.Tron.APIKeyType = ""
		})
	replay.ResolveReplayServices(t, cfg, spec.envPrefix)

	// Embedded DefraDB with the chain-prefixed schema. The ForChain variant
	// is required: LoadSchemaSDL returns the raw embedded default-prefix
	// content without the prefix swap, which would create the wrong
	// collections.
	cols, err := tron.NewCollectionsFromConfig(cfg)
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

	// The pre-connected constructor skips the fetcher's Connect: the mock
	// endpoint is already bound and the wallet API serves no dial phase.
	client := tron.NewClient(mockNode.URL(), "", "", "", 0)
	defer func() { _ = client.Close() }()

	fetcher := tron.NewFetcher(client)
	converter := tron.NewConverter(cfg)
	handler, err := defra.NewBlockHandler(td.Node, cfg.Indexer.MaxDocsPerTxn)
	require.NoError(t, err)

	// Background services run beside the timing loop: the pruner deletes
	// past retention and the snapshotter archives to disk, so their IO lands
	// in the samples the way production background load does.
	services := replay.StartReplayServices(t, cfg, td.Node, converter, handler, ctx)

	// The worker count comes from the shipped yaml (concurrent_blocks), the
	// same field production reads: the env scrub keeps any stray INDEXER_*
	// override from changing the measured configuration.
	workers := cfg.Indexer.ConcurrentBlocks
	require.Greater(t, workers, 0, "config must enable at least one concurrent worker")

	timings := replay.NewTimings()
	processor := indexer.NewConcurrentBlockProcessor(
		replay.NewTimedFetcher(fetcher, timings),
		replay.NewTimedConverter(converter, timings),
		replay.NewTimedStorer(handler, timings),
		nil, // no reorgs in a replay: the contiguous fixture range cannot fork, and nil makes any detected reorg fatal instead
		workers,
		0, // pacing off: blocks_per_minute's rate limiter must not soak up the measurement
	)

	// The dispatcher runs to infinity; the run ends when the last fixture
	// block commits in order. At that point every fixture block is stored,
	// so the ctx cancel is the expected termination, not a failure. The
	// wall clock stops at that same commit: ProcessBlocks' return additionally
	// waits out the in-flight beyond-tip fetch retries (a few seconds at
	// most), which are not part of the measurement.
	first, last := int64(numbers[0]), int64(numbers[len(numbers)-1])
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	watchdog := replay.StartStallWatchdog(len(blocks), cancelRun)
	defer watchdog.Stop()

	var wallEnd time.Time
	wallStart := time.Now()
	err = processor.ProcessBlocks(runCtx, first, func(blockNum int64) {
		done := watchdog.RecordCommit()
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

	timingsList, err := timings.SnapshotOrdered(first, last)
	require.NoError(t, err, "every replayed block must have a timing sample - a missing sample means a block never stored (blocked by a failed fetch/convert/store or the stall watchdog)")
	require.Len(t, timingsList, len(blocks),
		"sample count must equal the fixture block count - a missing sample means a block failed inside the processor")

	phaseAvg, err := timings.PhaseAverages(first, last)
	require.NoError(t, err, "every replayed block must have complete phase records - fetch success precedes convert, convert precedes any store, so a total sample implies all three")

	// Stop the services before the report and assertions: the pruner must
	// not race the final count queries, and the snapshotter's stats are
	// final once stopped. Idempotent: the Cleanup registered at startup
	// becomes a no-op after this.
	services.Stop()

	replay.ReportResults(t, len(blocks), numbers, timingsList, phaseAvg, workers, wall, target)
	replay.LogServiceStats(t, services)
	replay.AssertVerdict(t, spec.chainLabel, numbers, wall, workers, target)
	replay.AssertCorrectness(t, td, ctx, cols, converter, numbers, len(blocks), services.PrunedBlocks())
}
