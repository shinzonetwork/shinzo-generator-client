//go:build acceptance
// +build acceptance

package benchmarking

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/require"
)

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

// TestMain initializes the console-only logger for the acceptance suite.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// loadReplayFixture reads and validates a capture fixture.
func loadReplayFixture(t *testing.T, path string) *replayFixture {
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
	if !strings.EqualFold(fx.Meta.Chain, "BSC") {
		t.Fatalf("fixture %s is for chain %q, want bsc", path, fx.Meta.Chain)
	}
	return &fx
}

// replayRPCServer dispatches the production rpcClient method surface
// (pkg/chains/evm/fetcher.go) from the fixture, serving the node's result
// bytes verbatim. Batch-mode blocks answer eth_getBlockReceipts; per-tx-mode
// blocks answer eth_getTransactionReceipt and report the batch call as
// unsupported, mirroring the node the fixture was captured from.
type replayRPCServer struct {
	server      *httptest.Server
	blocks      map[uint64]fixtureBlock
	receipts    map[string]json.RawMessage // tx hash → verbatim receipt
	batchBlocks map[uint64]bool            // numbers whose receipts were captured via eth_getBlockReceipts
}

// newReplayRPCServer indexes the fixture and starts the mock node.
func newReplayRPCServer(fx *replayFixture) *replayRPCServer {
	srv := &replayRPCServer{
		blocks:      make(map[uint64]fixtureBlock, len(fx.Blocks)),
		receipts:    make(map[string]json.RawMessage),
		batchBlocks: make(map[uint64]bool),
	}
	for _, fb := range fx.Blocks {
		if num, err := parseHexUint(fb.Number); err == nil {
			srv.blocks[num] = fb
			srv.batchBlocks[num] = fb.ReceiptMode == "batch"
		}
		if fb.ReceiptMode == "per-tx" {
			indexReceipts(srv.receipts, fb.Receipts)
		}
	}

	srv.server = httptest.NewServer(http.HandlerFunc(srv.handle))
	return srv
}

// indexReceipts flattens a per-tx receipt array into the hash → receipt map.
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
	if obj.BlockHash != "" {
		return 0, fmt.Errorf("blockHash %s unsupported: the fixture index resolves blocks by number", obj.BlockHash)
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

// graphqlCountInRange counts documents in a collection whose number field
// falls within [first, last]. It deliberately avoids aggregate queries:
// DefraDB v1's aggregate response shape proved unstable across versions,
// while the plain row query's shape (data[collection] → []any) is the same
// one the production converter's progress queries rely on. limit must
// exceed the expected count — a full page means the range holds more
// documents than expected, which callers treat as a mismatch.
func graphqlCountInRange(ctx context.Context, n *node.Node, collection, numberField string, first, last int64, limit int) (int, error) {
	query := fmt.Sprintf(
		"query { %s(filter: {%s: {_geq: %d, _leq: %d}}, limit: %d) { %s } }",
		collection, numberField, first, last, limit, numberField)

	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, fmt.Errorf("count %s: %v", collection, result.GQL.Errors[0])
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("count %s: no data in response", collection)
	}
	// The data decodes as []any over JSON and as []map[string]any in-process;
	// a nil value means the collection is empty. Mirror converter's handling.
	switch arr := data[collection].(type) {
	case []any:
		return len(arr), nil
	case []map[string]any:
		return len(arr), nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("count %s: unexpected response type %T", collection, data[collection])
	}
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

// replayKeyringSecret backs the local file keyring for the harness's signing
// identity; throwaway by design because the store directory is a temp dir.
const replayKeyringSecret = "bsc-replay-keyring-secret"

// Forced-fast service defaults so prune and snapshot cycles actually fire
// during a replay run: retention below the fixture range produces real
// deletions, and a small blocks-per-file produces real snapshot writes.
// resolveReplayServices applies the BSC_REPLAY_* overrides on top.
const (
	replayPruneIntervalSeconds    = 10
	replayPruneMaxBlocks          = 100
	replayDocsPerBlock            = 1000 // production default
	replaySnapshotIntervalSeconds = 10
	replaySnapshotBlocksPerFile   = 50
)

// resolveReplayServices applies the pruner/snapshotter kill-switches and
// forced-fast overrides on top of newReplayConfig's defaults.
func resolveReplayServices(t *testing.T, cfg *config.Config) {
	t.Helper()

	if replayServiceOn(t, "BSC_REPLAY_PRUNER") {
		cfg.Pruner.Enabled = true
		cfg.Pruner.IntervalSeconds = replayServiceInt(t, "BSC_REPLAY_PRUNER_INTERVAL_SECONDS", replayPruneIntervalSeconds)
		cfg.Pruner.MaxBlocks = int64(replayServiceInt(t, "BSC_REPLAY_PRUNER_MAX_BLOCKS", replayPruneMaxBlocks))
	} else {
		logger.Test("pruner disabled via BSC_REPLAY_PRUNER=off")
		cfg.Pruner.Enabled = false
	}

	if replayServiceOn(t, "BSC_REPLAY_SNAPSHOT") {
		cfg.Snapshot.Enabled = true
		cfg.Snapshot.IntervalSeconds = replayServiceInt(t, "BSC_REPLAY_SNAPSHOT_INTERVAL_SECONDS", replaySnapshotIntervalSeconds)
		cfg.Snapshot.BlocksPerFile = int64(replayServiceInt(t, "BSC_REPLAY_SNAPSHOT_BLOCKS_PER_FILE", replaySnapshotBlocksPerFile))
	} else {
		logger.Test("snapshotter disabled via BSC_REPLAY_SNAPSHOT=off")
		cfg.Snapshot.Enabled = false
	}
}

// replayServiceOn parses a service kill-switch: "off" disables, empty or "on"
// keeps the service enabled, anything else fails fast so a typo cannot
// silently change the measured configuration.
func replayServiceOn(t *testing.T, env string) bool {
	t.Helper()
	switch raw := strings.ToLower(os.Getenv(env)); raw {
	case "", "on":
		return true
	case "off":
		return false
	default:
		t.Fatalf("invalid %s=%q: use on or off", env, raw)
		return false
	}
}

// replayServiceInt parses a positive-integer override, returning the
// forced-fast default when the env is unset.
func replayServiceInt(t *testing.T, env string, def int) int {
	t.Helper()
	raw := os.Getenv(env)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	require.NoError(t, err, "invalid %s %q: want a positive integer", env, raw)
	require.Greater(t, n, 0, "%s must be positive", env)
	return n
}

// newReplayConfig builds the config the pipeline runs under: chain identity
// BSC/Mainnet/evm (the prefix every collection name derives from), the mock
// node as endpoint, P2P off, no health server, and forced-fast pruner +
// snapshotter defaults whose delete and snapshot IO lands in the timing
// sample the way production background load does.
func newReplayConfig(nodeURL, storePath string) *config.Config {
	cfg := &config.Config{}
	cfg.Chain.Name = "BSC"
	cfg.Chain.Network = "Mainnet"
	cfg.Chain.Adapter = config.DefaultChainAdapter
	cfg.Chain.Hub = "testnet.shinzo.network"

	cfg.Geth.NodeURL = nodeURL
	cfg.Geth.DialTimeoutSeconds = 10

	cfg.DefraDB.Embedded = true
	cfg.DefraDB.Store.Path = storePath
	cfg.DefraDB.Store.ValueLogFileSizeMB = replayValueLogSizeMB
	cfg.DefraDB.KeyringSecret = replayKeyringSecret
	cfg.DefraDB.P2P.Enabled = false
	cfg.DefraDB.P2P.AcceptIncoming = false

	// max_docs_per_txn and the per-collection batch sizes stay moderate to
	// respect badger's ~9.7 MB per-transaction ceiling: a BSC mainnet block
	// can carry hundreds of transactions and thousands of logs.
	cfg.Indexer.StartHeight = 0
	cfg.Indexer.ConcurrentBlocks = 1
	cfg.Indexer.ReceiptWorkers = 8
	cfg.Indexer.MaxDocsPerTxn = 100
	cfg.Indexer.MaxTxDocsPerBatch = 100
	cfg.Indexer.MaxLogDocsPerBatch = 125
	cfg.Indexer.MaxALEDocsPerBatch = 500
	cfg.Indexer.HealthServerPort = -1
	cfg.Indexer.OpenBrowserOnStart = false
	cfg.Indexer.SchemaAuthMode = "none"

	// Forced-fast service pacing: retention below the fixture range produces
	// real deletions mid-run, and a small blocks-per-file produces real
	// snapshot writes. resolveReplayServices applies the BSC_REPLAY_*
	// kill-switches and overrides on top of these defaults.
	cfg.Pruner.Enabled = true
	cfg.Pruner.MaxBlocks = replayPruneMaxBlocks
	cfg.Pruner.DocsPerBlock = replayDocsPerBlock
	cfg.Pruner.IntervalSeconds = replayPruneIntervalSeconds

	cfg.Snapshot.Enabled = true
	cfg.Snapshot.Dir = filepath.Join(storePath, "snapshots")
	cfg.Snapshot.BlocksPerFile = replaySnapshotBlocksPerFile
	cfg.Snapshot.IntervalSeconds = replaySnapshotIntervalSeconds

	cfg.Logger.Development = false

	return cfg
}

// blockTimings collects per-block end-to-end processing durations.
type blockTimings []time.Duration

// sorted returns the timings ordered ascending.
func (bt blockTimings) sorted() []time.Duration {
	out := make([]time.Duration, len(bt))
	copy(out, bt)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// min returns the shortest duration.
func (bt blockTimings) min() time.Duration {
	sorted := bt.sorted()
	return sorted[0]
}

// max returns the longest duration.
func (bt blockTimings) max() time.Duration {
	sorted := bt.sorted()
	return sorted[len(sorted)-1]
}

// avg returns the mean duration.
func (bt blockTimings) avg() time.Duration {
	var total time.Duration
	for _, d := range bt {
		total += d
	}
	return total / time.Duration(len(bt))
}

// percentile returns the p-th percentile (0..100) of the timings.
func (bt blockTimings) percentile(p int) time.Duration {
	sorted := bt.sorted()
	idx := (len(sorted) - 1) * p / 100
	return sorted[idx]
}

// headroomPct returns how much faster the average is than the target, as a
// percentage of the target (negative when the target is missed).
func (bt blockTimings) headroomPct(target time.Duration) float64 {
	return float64(target-bt.avg()) / float64(target) * 100
}

// outlierLines lists blocks whose processing time exceeded twice the
// average, formatted as "block no - time - deviation from average" for the
// report's outlier list.
func (bt blockTimings) outlierLines(numbers []uint64, avg time.Duration) []string {
	var lines []string
	threshold := 2 * avg
	for i, d := range bt {
		if d > threshold {
			deviation := float64(d-avg) / float64(avg) * 100
			lines = append(lines, fmt.Sprintf("%d - %s - +%.1f%% vs avg", numbers[i], d, deviation))
		}
	}
	return lines
}

// replayQueueTracker adapts pruner's IndexerQueue to the BlockHandler's
// docID tracker interface — the same adapter pkg/indexer wires in production,
// duplicated here because the indexer's version is unexported.
type replayQueueTracker struct {
	queue *pruner.IndexerQueue
}

// TrackBlock records every document a stored block created, feeding the queue
// the pruner drains from. It runs inside BlockHandler.Store, so its cost is
// part of the measured per-block time, as in production.
func (t *replayQueueTracker) TrackBlock(_ context.Context, blockNumber int64, result *defra.BlockCreationResult) error {
	return t.queue.TrackBlockDocIDs(blockNumber, result.BlockID, result.OtherDocIDs, result.BlockSignatureID)
}

// replayServices groups the background services running beside the timing
// loop. Both are optional: nil fields mean the service was disabled. stopOnce
// makes stop idempotent: Cleanup and the happy path both call it.
type replayServices struct {
	pruneSvc *pruner.Pruner
	snapSvc  *snapshot.Snapshotter
	stopOnce sync.Once
}

// startReplayServices wires the pruner and snapshotter the way the indexer's
// initServices does: queue bound to its file, docID tracker on the handler,
// then Start on the identity context (the snapshotter signs with it). Their
// background cycles run while blocks are replayed, so prune and snapshot IO
// land in the measurements the way production background load does.
func startReplayServices(t *testing.T, cfg *config.Config, defraNode *node.Node, converter chains.Converter, handler *defra.BlockHandler, ctx context.Context) *replayServices {
	t.Helper()

	rs := &replayServices{}

	if cfg.Pruner.Enabled {
		pruneQueue := pruner.NewIndexerQueue()
		// Binding the queue to its file must happen before anything tracks
		// into it; saves are a no-op until then (the store is a fresh temp
		// dir, so the file does not exist yet and 0 entries load).
		if _, err := pruneQueue.LoadFromFile(filepath.Join(cfg.DefraDB.Store.Path, "prune_queue.gob")); err != nil {
			logger.Testf("prune queue file load failed (continuing): %v", err)
		}
		rs.pruneSvc = pruner.NewPruner(&cfg.Pruner, defraNode, converter)
		rs.pruneSvc.SetQueue(pruneQueue)
		handler.SetDocIDTracker(&replayQueueTracker{queue: pruneQueue})
		require.NoError(t, rs.pruneSvc.Start(ctx))
		logger.Testf("pruner started: interval %ds, retention %d blocks", cfg.Pruner.IntervalSeconds, cfg.Pruner.MaxBlocks)
	}

	if cfg.Snapshot.Enabled {
		rs.snapSvc = snapshot.New(&cfg.Snapshot, defraNode, converter)
		require.NoError(t, rs.snapSvc.Start(ctx))
		logger.Testf("snapshotter started: dir %s, interval %ds, blocks per file %d",
			cfg.Snapshot.Dir, cfg.Snapshot.IntervalSeconds, cfg.Snapshot.BlocksPerFile)
	}

	// Register teardown for every exit path: if a require in the replay loop
	// fails, the test goroutine stops there and Cleanup must stop the
	// services before the node is torn down, or the buried errors from a
	// pruner/snapshotter racing the dying node drown out the real failure.
	t.Cleanup(rs.stop)

	return rs
}

// stop tears the services down in the indexer's stop order: the snapshotter
// first (capture data before it is pruned), then the pruner so it cannot race
// the test's final count queries. Idempotent via stopOnce: the snapshotter's
// Stop closes its channel unguarded, so a second call would panic.
func (rs *replayServices) stop() {
	rs.stopOnce.Do(func() {
		if rs.snapSvc != nil {
			rs.snapSvc.Stop()
		}
		if rs.pruneSvc != nil {
			rs.pruneSvc.Stop()
		}
	})
}

// prunedBlocks returns the count of blocks the pruner deleted during the
// run; zero when the pruner is disabled.
func (rs *replayServices) prunedBlocks() int64 {
	if rs.pruneSvc == nil {
		return 0
	}
	return rs.pruneSvc.GetMetrics().TotalBlocksPruned
}

// logServiceStats prints each service's outcome below the report box so the
// fixed-width report format stays untouched.
func logServiceStats(t *testing.T, rs *replayServices) {
	t.Helper()
	if rs.pruneSvc != nil {
		m := rs.pruneSvc.GetMetrics()
		logger.Testf("Pruner: %d blocks / %d docs pruned", m.TotalBlocksPruned, m.TotalDocsPruned)
	}
	if rs.snapSvc != nil {
		m := rs.snapSvc.GetMetrics()
		logger.Testf("Snapshotter: %d snapshots (last block %d)", m.TotalSnapshots, m.LastSnapshotBlock)
	}
}
