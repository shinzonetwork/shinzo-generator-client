//go:build acceptance
// +build acceptance

package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin"
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

// bitcoinFixtureDir holds per-machine capture fixtures produced by
// cmd/fetch_blocks --adapter bitcoin; they are gitignored. Fixtures for every
// chain share one directory and the filename prefix identifies the chain
// (bitcoin_blocks_<from>_<to>.json). From replay/bitcoin the shared directory
// is one level up, like it is from replay/evm and replay/tron.
const bitcoinFixtureDir = "../testdata"

// Method names and error codes of Bitcoin Core's JSON-RPC surface the mock
// dispatches on, duplicated from the unexported spellings in
// pkg/chains/bitcoin (test packages in different directories cannot import
// them). Core answers a height past the node's head with -8, an unknown block
// hash with -5, and everything else with a -1 misc error.
const (
	bitcoinMethodGetBlockCount  = "getblockcount"
	bitcoinMethodGetBlockHash   = "getblockhash"
	bitcoinMethodGetBlock       = "getblock"
	bitcoinMethodGetBlockHeader = "getblockheader"

	bitcoinRPCCodeMisc        = -1
	bitcoinRPCCodeNotFound    = -5
	bitcoinRPCCodeHeightRange = -8

	// bitcoinVerbosityUndoData is the getblock verbosity the production
	// fetcher issues: parsed transactions with per-input prevout undo data
	// and per-transaction fees.
	bitcoinVerbosityUndoData = 3
)

// Fixture types duplicated from cmd/fetch_blocks: test packages cannot
// import a main package, so the on-disk format is mirrored here. Keep both
// sides in sync when the capture format changes.
//
// Receipts is always an empty JSON array: the Bitcoin capture has no receipt
// pass, and the field exists only so the on-disk envelope stays identical
// across adapters.

// bitcoinFixtureBlock holds one block's verbatim node responses.
type bitcoinFixtureBlock struct {
	Number      string          `json:"number"`       // decimal block height (Core heights are decimal)
	Block       json.RawMessage `json:"block"`        // getblock verbosity-3 result, verbatim
	Receipts    json.RawMessage `json:"receipts"`     // always [], the capture has no receipt pass
	ReceiptMode string          `json:"receipt_mode"` // "none"
}

// bitcoinFixtureMeta describes the capture; it carries no endpoint or key
// material.
type bitcoinFixtureMeta struct {
	Chain          string `json:"chain"`
	Network        string `json:"network"`
	From           uint64 `json:"from"`
	To             uint64 `json:"to"`
	BlockCount     int    `json:"block_count"`
	CapturedAt     string `json:"captured_at"`
	CapturePartial bool   `json:"capture_partial,omitempty"`
}

// bitcoinReplayFixture is the on-disk format produced by cmd/fetch_blocks
// --adapter bitcoin.
type bitcoinReplayFixture struct {
	Meta   bitcoinFixtureMeta    `json:"meta"`
	Blocks []bitcoinFixtureBlock `json:"blocks"`
}

// TestMain initializes the console-only logger for the acceptance suite.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// loadBitcoinFixture reads and validates a capture fixture for the Bitcoin
// suite.
func loadBitcoinFixture(t *testing.T, path string) *bitcoinReplayFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}

	var fx bitcoinReplayFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	// The chain label is free-form (it is typed at capture time), so match
	// case-insensitively; collection prefixes remain canonical downstream.
	if !strings.EqualFold(fx.Meta.Chain, "bitcoin") {
		t.Fatalf("fixture %s is for chain %q, want bitcoin", path, fx.Meta.Chain)
	}
	// Partial captures replay fine on purpose (the FIXTURE env), but
	// the verdict covers only what the file contains, so keep that visible:
	// an unexplained 20-block run would otherwise pass unnoticed.
	if fx.Meta.CapturePartial {
		logger.Testf("WARNING: fixture %s is a partial capture (%d blocks); the verdict covers only what it contains", path, len(fx.Blocks))
	}
	return &fx
}

// bitcoinCoreServer dispatches the production client's RPC surface
// (pkg/chains/bitcoin/bitcoin_client.go) from the fixture, serving the node's
// result bytes verbatim in Core's JSON-RPC 1.0 dialect. The tip is the
// highest fixture block; heights beyond it answer Core's -8 "Block height out
// of range", the routine beyond-tip answer the production fetcher wraps into
// not-found, so the processor's wait-for-tip retry sees the same
// classification a live node produces. RPC failures ride HTTP 500 plus an
// error object the way Core reports them, so rpcclient decodes a typed error
// exactly as it does against a real node. getblockheader serves the reorg
// walk-back's FetchBlockHeaderByHash (unreachable while the replay passes a
// nil reorg handler, served so the mock stays complete like the EVM and Tron
// ones), synthesized from the header fields the verbosity-3 body carries.
type bitcoinCoreServer struct {
	server    *httptest.Server
	byHeight  map[int64]string               // height -> block hash
	byHash    map[string]bitcoinFixtureBlock // block hash -> fixture block
	tipHeight int64
}

// newBitcoinCoreServer indexes the fixture and starts the mock node.
func newBitcoinCoreServer(fx *bitcoinReplayFixture) *bitcoinCoreServer {
	s := &bitcoinCoreServer{
		byHeight: make(map[int64]string, len(fx.Blocks)),
		byHash:   make(map[string]bitcoinFixtureBlock, len(fx.Blocks)),
	}
	for _, fb := range fx.Blocks {
		num, err := strconv.ParseInt(fb.Number, 10, 64)
		if err != nil {
			continue // unreachable by number; an unparsable height would have failed capture
		}
		var body struct {
			Hash string `json:"hash"`
		}
		if err := json.Unmarshal(fb.Block, &body); err != nil || body.Hash == "" {
			continue // unreachable by hash; an unparsable body would have failed capture
		}
		s.byHeight[num] = body.Hash
		s.byHash[body.Hash] = fb
		if num > s.tipHeight {
			s.tipHeight = num
		}
	}

	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// bitcoinRPCError is the error object Core rides in the JSON-RPC envelope.
type bitcoinRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// bitcoinRPCErrf builds one typed error; Sprintf into a fixed envelope shape
// cannot fail, so no error return exists.
func bitcoinRPCErrf(code int, format string, args ...any) *bitcoinRPCError {
	return &bitcoinRPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// handle decodes one JSON-RPC request and answers with Core's envelope. A
// typed error rides HTTP 500 plus an error object the way Core reports RPC
// failures, so rpcclient decodes it into a typed error and the production
// error classification sees the real code.
func (s *bitcoinCoreServer) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}

	result, rpcErr := s.serve(req.Method, req.Params)
	w.Header().Set("Content-Type", "application/json")
	if rpcErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": rpcErr, "id": id})
		return
	}
	// RawMessage marshals verbatim: the mock serves exactly what the node
	// served when the fixture was captured.
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": id})
}

// serve resolves one RPC method against the fixture index.
func (s *bitcoinCoreServer) serve(method string, params json.RawMessage) (json.RawMessage, *bitcoinRPCError) {
	var args []json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil || len(args) == 0 {
		return nil, bitcoinRPCErrf(bitcoinRPCCodeMisc, "malformed params for %s", method)
	}

	switch method {
	case bitcoinMethodGetBlockCount:
		// The fixture's capture-time tip is the mock node's tip.
		return json.RawMessage(strconv.FormatInt(s.tipHeight, 10)), nil

	case bitcoinMethodGetBlockHash:
		var height int64
		if err := json.Unmarshal(args[0], &height); err != nil {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeHeightRange, "getblockhash: expected a numeric height parameter")
		}
		hash, ok := s.byHeight[height]
		if !ok {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeHeightRange, "Block height out of range")
		}
		return bitcoinQuotedHash(hash), nil

	case bitcoinMethodGetBlock:
		var hash string
		if err := json.Unmarshal(args[0], &hash); err != nil {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeHeightRange, "getblock: expected a block hash parameter")
		}
		var verbosity int
		if len(args) > 1 {
			_ = json.Unmarshal(args[1], &verbosity)
		}
		if verbosity != bitcoinVerbosityUndoData {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeHeightRange, "unexpected getblock verbosity %d: the production flow only issues verbosity 3", verbosity)
		}
		fixture, ok := s.byHash[hash]
		if !ok {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeNotFound, "Block not found")
		}
		return fixture.Block, nil

	case bitcoinMethodGetBlockHeader:
		var hash string
		if err := json.Unmarshal(args[0], &hash); err != nil {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeHeightRange, "getblockheader: expected a block hash parameter")
		}
		fixture, ok := s.byHash[hash]
		if !ok {
			return nil, bitcoinRPCErrf(bitcoinRPCCodeNotFound, "Block not found")
		}
		return s.synthHeader(fixture), nil

	default:
		return nil, bitcoinRPCErrf(bitcoinRPCCodeMisc, "unexpected RPC method %q: the replay flow only exercises block fetches", method)
	}
}

// bitcoinQuotedHash quotes one block hash for the getblockhash result.
// Marshaling a string cannot fail, so the error is discarded by construction.
func bitcoinQuotedHash(hash string) json.RawMessage {
	raw, _ := json.Marshal(hash)
	return raw
}

// synthHeader builds a verbose getblockheader answer from a verbosity-3 body,
// which carries every header field the walk-back consumes, spelled the way
// Core spells them in both shapes.
func (s *bitcoinCoreServer) synthHeader(fixture bitcoinFixtureBlock) json.RawMessage {
	var body struct {
		Hash              string  `json:"hash"`
		Height            int64   `json:"height"`
		Version           uint32  `json:"version"`
		MerkleRoot        string  `json:"merkleroot"`
		Time              int64   `json:"time"`
		MedianTime        int64   `json:"mediantime"`
		Nonce             uint64  `json:"nonce"`
		Bits              string  `json:"bits"`
		Difficulty        float64 `json:"difficulty"`
		ChainWork         string  `json:"chainwork"`
		PreviousBlockHash string  `json:"previousblockhash"`
		NextBlockHash     string  `json:"nextblockhash"`
	}
	if err := json.Unmarshal(fixture.Block, &body); err != nil {
		return json.RawMessage(`{}`)
	}
	header := map[string]any{
		"hash":          body.Hash,
		"confirmations": s.tipHeight - body.Height + 1,
		"height":        body.Height,
		"version":       body.Version,
		"merkleroot":    body.MerkleRoot,
		"time":          body.Time,
		"mediantime":    body.MedianTime,
		"nonce":         body.Nonce,
		"bits":          body.Bits,
		"difficulty":    body.Difficulty,
		"chainwork":     body.ChainWork,
	}
	if body.PreviousBlockHash != "" {
		header["previousblockhash"] = body.PreviousBlockHash
	}
	if body.NextBlockHash != "" {
		header["nextblockhash"] = body.NextBlockHash
	}
	raw, _ := json.Marshal(header)
	return raw
}

// Close shuts the mock node down.
func (s *bitcoinCoreServer) Close() { s.server.Close() }

// URL returns the mock node's base URL.
func (s *bitcoinCoreServer) URL() string { return s.server.URL }

// capSampleBitcoin applies the suite's REPLAY_MAX_BLOCKS cap for slow hosts,
// returning the capped blocks and their parsed numbers (kept aligned for the
// report). Numbers parse as decimal: Core heights are decimal.
func capSampleBitcoin(t *testing.T, fx *bitcoinReplayFixture, maxBlocksEnvName string) ([]bitcoinFixtureBlock, []uint64) {
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

// resolveBitcoinTargetBlockTime parses the suite's TARGET_BLOCK_TIME when set
// (for example 5m to gate a slow host harder) and falls back to Bitcoin's
// 10-minute mainnet block interval. Duplicated from the Tron suite's resolver:
// test packages in different directories cannot share unexported helpers, and
// promoting it to the shared harness would churn the other suites for ten
// lines.
func resolveBitcoinTargetBlockTime(t *testing.T, envName string, def time.Duration, durationHint string) time.Duration {
	t.Helper()
	if raw := os.Getenv(envName); raw != "" {
		d, err := time.ParseDuration(raw)
		require.NoError(t, err, "invalid %s %q: want a duration like %s", envName, raw, durationHint)
		return d
	}
	return def
}

// bitcoinReplaySpec carries everything that differs between the chain suites,
// the Bitcoin counterpart of the tron suite's spec: the chain label used for
// config validation and the verdict; the shipped config file the measurement
// boots from; the expected network identity inside that config; the env
// prefix composing the suite's variables (TEST_BITCOIN_TARGET_BLOCK_TIME,
// TEST_BITCOIN_REPLAY_MAX_BLOCKS, ...); the fixture glob; the target block
// interval with its parse error hint; and the capture command the skip
// message points at for fixture capture.
type bitcoinReplaySpec struct {
	chainLabel         string
	configPath         string
	expectedNetwork    string
	envPrefix          string
	fixtureGlob        string
	defaultTarget      time.Duration
	targetDurationHint string
	skipHint           string
}

// runBitcoinReplay answers "can we index Bitcoin blocks at the tip": it
// replays a captured fixture through a mock Bitcoin Core node into the full
// production pipeline and drives it with the real production orchestration,
// the indexer's ConcurrentBlockProcessor, with the worker count the shipped
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
// Bitcoin has no receipt pass, so the flow runs one fetch per block like the
// Tron flow, and that fetch is the two-call getblockhash plus getblock pair
// the production fetcher makes.
func runBitcoinReplay(t *testing.T, spec bitcoinReplaySpec) {
	fixtureEnv := spec.envPrefix + "_REPLAY_FIXTURE"
	fixturePath := replay.ResolveFixturePath(t, bitcoinFixtureDir, spec.fixtureGlob, fixtureEnv)
	if fixturePath == "" {
		t.Skipf("no replay fixture found: run '%s' (needs BITCOIN_RPC_URL) to capture one into %s, or point %s at an existing fixture", spec.skipHint, bitcoinFixtureDir, fixtureEnv)
	}

	fx := loadBitcoinFixture(t, fixturePath)
	if len(fx.Blocks) == 0 {
		t.Skipf("fixture %s contains no blocks - recapture it", fixturePath)
	}

	targetEnv := spec.envPrefix + "_TARGET_BLOCK_TIME"
	target := resolveBitcoinTargetBlockTime(t, targetEnv, spec.defaultTarget, spec.targetDurationHint)
	blocks, numbers := capSampleBitcoin(t, fx, spec.envPrefix+"_REPLAY_MAX_BLOCKS")

	logger.Testf("%s replay acceptance: %d blocks from fixture %s, target %s per block", spec.chainLabel, len(blocks), fixturePath, target)

	mockNode := newBitcoinCoreServer(fx)
	defer mockNode.Close()

	storePath := t.TempDir()
	cfg := replay.NewReplayConfig(t, spec.configPath, spec.chainLabel, spec.expectedNetwork, storePath,
		func(cfg *config.Config) {
			// The mock server replaces the endpoint; the archive endpoint and
			// the auth material are blanked so no endpoint or key literal
			// leaks to a future consumer. The suite's client below is built
			// directly against the mock with throwaway basic auth, so the
			// loader's placeholder values never reach the wire.
			cfg.Bitcoin.RPCURL = mockNode.URL()
			cfg.Bitcoin.ArchiveRPCURL = ""
			cfg.Bitcoin.RPCUser = ""
			cfg.Bitcoin.RPCPassword = ""
			cfg.Bitcoin.CookiePath = ""
			cfg.Bitcoin.APIKey = ""
			cfg.Bitcoin.APIKeyType = ""
		})
	replay.ResolveReplayServices(t, cfg, spec.envPrefix)

	// Embedded DefraDB with the chain-prefixed schema. The ForChain variant
	// is required: LoadSchemaSDL returns the raw embedded default-prefix
	// content without the prefix swap, which would create the wrong
	// collections.
	cols, err := bitcoin.NewCollectionsFromConfig(cfg)
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

	// The client is built directly against the mock with throwaway basic
	// auth: NewClient requires configured auth by validation, and the
	// pre-built fetcher seam skips Connect, whose chain-identity probe would
	// need getblockchaininfo answers the replay surface never serves.
	client, err := bitcoin.NewClient(config.BitcoinConfig{
		RPCURL:      mockNode.URL(),
		RPCUser:     "replay",
		RPCPassword: "replay",
	})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	fetcher := bitcoin.NewFetcher(client, int64(cfg.Bitcoin.ConfirmationDepth))
	converter := bitcoin.NewConverter(cfg)
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
