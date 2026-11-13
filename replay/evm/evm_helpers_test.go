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

	pkgerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
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

// fixtureDir holds per-machine capture fixtures produced by cmd/fetch_blocks;
// they are gitignored. Fixtures for every chain share one directory and the
// filename prefix identifies the chain (ethereum_blocks_<from>_<to>.json).
const fixtureDir = "../testdata"

// Fixture types duplicated from cmd/fetch_blocks: test packages cannot import
// a main package, so the on-disk format is mirrored here. Keep both sides in
// sync when the capture format changes.
//
// Receipts is always a JSON array of receipt objects regardless of capture
// mode, so the replay serves both modes uniformly.

// fixtureBlock holds one block's verbatim node responses.
type fixtureBlock struct {
	Number      string          `json:"number"`       // hex block number
	Block       json.RawMessage `json:"block"`        // eth_getBlockByNumber(number, full=true) result, verbatim
	Receipts    json.RawMessage `json:"receipts"`     // receipt array, verbatim
	ReceiptMode string          `json:"receipt_mode"` // "batch" or "per-tx"
}

// fixtureMeta describes the capture; it carries no endpoint or key material.
type fixtureMeta struct {
	Chain          string `json:"chain"`
	Network        string `json:"network"`
	From           uint64 `json:"from"`
	To             uint64 `json:"to"`
	BlockCount     int    `json:"block_count"`
	CapturedAt     string `json:"captured_at"`
	CapturePartial bool   `json:"capture_partial,omitempty"`
}

// replayFixture is the on-disk format produced by cmd/fetch_blocks.
type replayFixture struct {
	Meta   fixtureMeta    `json:"meta"`
	Blocks []fixtureBlock `json:"blocks"`
}

// TestMain initializes the console-only logger for the acceptance suite and
// registers the evm adapter: replay helpers resolve collections through the
// chain factory registry.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	evm.Register()
	os.Exit(m.Run())
}

// loadReplayFixture reads and validates a capture fixture for chainLabel.
func loadReplayFixture(t *testing.T, chainLabel, path string) *replayFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}

	var fx replayFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	// The chain label is free-form (it is typed at capture time), so match
	// case-insensitively; collection prefixes remain canonical downstream.
	if !strings.EqualFold(fx.Meta.Chain, chainLabel) {
		t.Fatalf("fixture %s is for chain %q, want %s", path, fx.Meta.Chain, strings.ToLower(chainLabel))
	}
	// Partial captures replay fine on purpose (the FIXTURE env), but
	// the verdict covers only what the file contains, so keep that visible:
	// an unexplained 20-block run would otherwise pass unnoticed.
	if fx.Meta.CapturePartial {
		logger.Testf("WARNING: fixture %s is a partial capture (%d blocks); the verdict covers only what it contains", path, len(fx.Blocks))
	}
	return &fx
}

// replayRPCServer dispatches the production rpcClient method surface
// (pkg/chains/evm/fetcher.go) from the fixture, serving the node's result
// bytes verbatim. Batch-mode blocks answer eth_getBlockReceipts; per-tx-mode
// blocks answer eth_getTransactionReceipt and report the batch call as
// unsupported, mirroring the node the fixture was captured from.
// hashToNum and batchBlocks back the production fetcher's receipt path: it
// pins every receipts call to the block's content hash
// (eth_getBlockReceipts {"blockHash": ...}), so hash resolution must exist
// for the batch answer to ever be reachable, and eth_getBlockByHash serves
// the processor's reorg-path walk-back reads.
type replayRPCServer struct {
	server      *httptest.Server
	blocks      map[uint64]fixtureBlock
	receipts    map[string]json.RawMessage // tx hash -> verbatim receipt
	hashToNum   map[string]uint64          // block hash -> number
	batchBlocks map[uint64]bool            // numbers whose receipts were captured via eth_getBlockReceipts
}

// newReplayRPCServer indexes the fixture and starts the mock node.
func newReplayRPCServer(fx *replayFixture) *replayRPCServer {
	srv := &replayRPCServer{
		blocks:      make(map[uint64]fixtureBlock, len(fx.Blocks)),
		receipts:    make(map[string]json.RawMessage),
		hashToNum:   make(map[string]uint64, len(fx.Blocks)),
		batchBlocks: make(map[uint64]bool),
	}
	for _, fb := range fx.Blocks {
		if num, err := parseHexUint(fb.Number); err == nil {
			srv.blocks[num] = fb
			srv.batchBlocks[num] = fb.ReceiptMode == "batch"
			// Both batch and per-tx receipts are flattened into the tx
			// index: production nodes answer eth_getTransactionReceipt
			// for every indexed tx, so the fetcher's per-tx fallback
			// must not dead-end on blocks the capture took batch.
			indexReceipts(srv.receipts, fb.Receipts)
		}
		indexBlockHash(srv.hashToNum, fb)
	}

	srv.server = httptest.NewServer(http.HandlerFunc(srv.handle))
	return srv
}

// indexBlockHash records the block's content hash under its number. An
// unreadable hash is skipped: the block stays reachable by number only.
func indexBlockHash(into map[string]uint64, fb fixtureBlock) {
	var block struct {
		Hash string `json:"hash"`
	}
	if json.Unmarshal(fb.Block, &block) != nil || block.Hash == "" {
		return
	}
	if num, err := parseHexUint(fb.Number); err == nil {
		into[block.Hash] = num
	}
}

// indexReceipts flattens a per-tx receipt array into the hash -> receipt map.
func indexReceipts(into map[string]json.RawMessage, receipts json.RawMessage) {
	var arr []json.RawMessage
	if json.Unmarshal(receipts, &arr) != nil {
		return
	}
	for _, raw := range arr {
		var receipt struct {
			TransactionHash string `json:"transactionHash"`
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.TransactionHash == "" {
			continue
		}
		into[receipt.TransactionHash] = raw
	}
}

// handle routes one JSON-RPC request against the fixture index.
func (s *replayRPCServer) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	result, rpcErr := s.dispatch(req.Method, req.Params)
	w.Header().Set("Content-Type", "application/json")

	if rpcErr != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error":   map[string]any{"code": -32601, "message": rpcErr.Error()},
		})
		return
	}
	// RawMessage marshals verbatim: the mock serves exactly what the node
	// served when the fixture was captured.
	_, _ = w.Write(mustJSON(rpcEnvelope{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}))
}

// dispatch resolves one method call against the fixture.
func (s *replayRPCServer) dispatch(method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "eth_blockNumber":
		// The fixture's capture-time tip is the mock node's tip.
		return json.RawMessage(`"` + hexNum(s.tip()) + `"`), nil
	case "eth_getBlockByNumber":
		num, err := s.paramBlockNumber(params)
		if err != nil {
			return nil, err
		}
		fb, ok := s.blocks[num]
		if !ok {
			return json.RawMessage("null"), nil
		}
		return fb.Block, nil
	case "eth_getBlockByHash":
		// Serves the walk-back's FetchBlockHeaderByHash: resolve the
		// hash to the fixture block and return it verbatim; an unknown
		// hash reads as null like a node that never saw the block.
		hash, err := s.paramHash(params)
		if err != nil {
			return nil, err
		}
		num, ok := s.hashToNum[hash]
		if !ok {
			return json.RawMessage("null"), nil
		}
		return s.blocks[num].Block, nil
	case "eth_getBlockReceipts":
		num, err := s.paramBlockNumber(params)
		if err != nil {
			return nil, err
		}
		if s.batchBlocks[num] {
			return s.blocks[num].Receipts, nil
		}
		return nil, fmt.Errorf("eth_getBlockReceipts is not supported by this node")
	case "eth_getTransactionReceipt":
		hash, err := s.paramHash(params)
		if err != nil {
			return nil, err
		}
		raw, ok := s.receipts[hash]
		if !ok {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unexpected method %q", method)
	}
}

// tip returns the highest block number in the fixture (0 when empty).
func (s *replayRPCServer) tip() uint64 {
	var highest uint64
	for num := range s.blocks {
		if num > highest {
			highest = num
		}
	}
	return highest
}

// paramBlockNumber extracts the block number from the first params element,
// which may be a plain hex string (eth_getBlockByNumber sends
// ["0x...", true]) or a BlockNumberOrHash object (eth_getBlockReceipts sends
// [{"blockNumber":"0x..."}] or [{"blockHash":"0x..."}]).
func (s *replayRPCServer) paramBlockNumber(params json.RawMessage) (uint64, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) == 0 {
		return 0, fmt.Errorf("params %s: want [blockNumber, ...]", params)
	}

	var hexStr string
	if err := json.Unmarshal(arr[0], &hexStr); err == nil {
		return parseHexUint(hexStr)
	}

	var obj struct {
		BlockNumber string `json:"blockNumber"`
		BlockHash   string `json:"blockHash"`
	}
	if err := json.Unmarshal(arr[0], &obj); err != nil {
		return 0, fmt.Errorf("params %s: want a hex string or {blockNumber|blockHash}", params)
	}
	if obj.BlockNumber != "" {
		return parseHexUint(obj.BlockNumber)
	}
	// The production fetcher pins receipts calls to the block's content
	// hash, so a blockHash resolves through the fixture index like a node
	// would resolve it through its hash index. Unknown hashes read as a
	// dispatch-level not-found instead of a param error, keeping the
	// caller's retry semantics intact.
	if obj.BlockHash != "" {
		lookup := strings.ToLower(obj.BlockHash)
		for hash, num := range s.hashToNum {
			if strings.EqualFold(hash, lookup) {
				return num, nil
			}
		}
		return 0, pkgerrors.NewDocumentNotFound("replay rpc", "eth_getBlockReceipts", "block", obj.BlockHash)
	}
	return 0, fmt.Errorf("params %s: no blockNumber or blockHash", params)
}

// paramHash extracts the tx hash from a params array.
func (s *replayRPCServer) paramHash(params json.RawMessage) (string, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) == 0 {
		return "", fmt.Errorf("params %s: want [txHash]", params)
	}
	var hash string
	if err := json.Unmarshal(arr[0], &hash); err != nil {
		return "", fmt.Errorf("params %s: first element must be a hash string", params)
	}
	return hash, nil
}

// Close shuts the mock node down.
func (s *replayRPCServer) Close() { s.server.Close() }

// URL returns the mock node's base URL.
func (s *replayRPCServer) URL() string { return s.server.URL }

// rpcEnvelope is the mock node's response shape; Result carries the fixture's
// raw bytes verbatim.
type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

// mustJSON marshals v or panics; the mock's envelope shapes are literals.
func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal rpc envelope: %v", err))
	}
	return data
}

// hexNum formats a block number the way nodes expect it in params.
func hexNum(num uint64) string {
	return "0x" + strconv.FormatUint(num, 16)
}

// parseHexUint parses a hex-quantity string ("0x...") into a uint64.
func parseHexUint(s string) (uint64, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if trimmed == "" {
		return 0, fmt.Errorf("empty hex quantity")
	}
	n, err := strconv.ParseUint(trimmed, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hex quantity %q: %w", s, err)
	}
	return n, nil
}

// capSample applies the suite's REPLAY_MAX_BLOCKS cap for slow hosts,
// returning the capped blocks and their parsed numbers (kept aligned for the
// report).
func capSample(t *testing.T, fx *replayFixture, maxBlocksEnvName string) ([]fixtureBlock, []uint64) {
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
		num, err := parseHexUint(fb.Number)
		require.NoError(t, err)
		numbers[i] = num
	}
	return blocks, numbers
}

// resolveTargetBlockTime parses the suite's TARGET_BLOCK_TIME when set (for
// example 10s to gate a slow host harder) and falls back to the chain's
// current mainnet interval.
func resolveTargetBlockTime(t *testing.T, envName string, def time.Duration, durationHint string) time.Duration {
	t.Helper()
	if raw := os.Getenv(envName); raw != "" {
		d, err := time.ParseDuration(raw)
		require.NoError(t, err, "invalid %s %q: want a duration like %s", envName, raw, durationHint)
		return d
	}
	return def
}

// replaySpec carries everything that differs between the EVM chains' suites:
// the chain label used for fixture validation, log lines, and the verdict;
// the shipped config file the measurement boots from; the expected network
// identity inside that config; the env prefix composing the suite's
// variables (BSC_TARGET_BLOCK_TIME, BSC_REPLAY_MAX_BLOCKS, ...); the fixture
// glob; the target block interval with its parse error hint; and the make
// target the skip message points at for fixture capture.
type replaySpec struct {
	chainLabel         string
	configPath         string
	expectedNetwork    string
	envPrefix          string
	fixtureGlob        string
	defaultTarget      time.Duration
	targetDurationHint string
	skipHint           string
}

// runEVMReplay answers "can we index EVM blocks at the tip": it replays a
// captured fixture through a mock JSON-RPC node into the full production
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
// The spec's parameters make one implementation serve every EVM chain.
func runEVMReplay(t *testing.T, spec replaySpec) {
	fixtureEnv := spec.envPrefix + "_REPLAY_FIXTURE"
	fixturePath := replay.ResolveFixturePath(t, fixtureDir, spec.fixtureGlob, fixtureEnv)
	if fixturePath == "" {
		t.Skipf("no replay fixture found: run '%s' (needs GETH_RPC_URL) to capture one into %s, or point %s at an existing fixture", spec.skipHint, fixtureDir, fixtureEnv)
	}

	fx := loadReplayFixture(t, spec.chainLabel, fixturePath)
	if len(fx.Blocks) == 0 {
		t.Skipf("fixture %s contains no blocks - recapture it", fixturePath)
	}

	targetEnv := spec.envPrefix + "_TARGET_BLOCK_TIME"
	target := resolveTargetBlockTime(t, targetEnv, spec.defaultTarget, spec.targetDurationHint)
	blocks, numbers := capSample(t, fx, spec.envPrefix+"_REPLAY_MAX_BLOCKS")

	logger.Testf("%s replay acceptance: %d blocks from fixture %s, target %s per block", spec.chainLabel, len(blocks), fixturePath, target)

	mockNode := newReplayRPCServer(fx)
	defer mockNode.Close()

	storePath := t.TempDir()
	cfg := replay.NewReplayConfig(t, spec.configPath, spec.chainLabel, spec.expectedNetwork, mockNode.URL(), storePath)
	replay.ResolveReplayServices(t, cfg, spec.envPrefix)

	// Embedded DefraDB with the chain-prefixed schema. The ForChain variant
	// is required: LoadSchemaSDL returns the raw embedded Ethereum__Mainnet
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
	converter, err := evm.NewConverter(cfg)
	require.NoError(t, err)
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
