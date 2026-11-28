//go:build acceptance
// +build acceptance

package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	shinzoerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/replay"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// solanaFixtureDir holds per-machine capture fixtures produced by
// cmd/fetch_blocks --adapter solana; they are gitignored. Fixtures for every
// chain share one directory and the filename prefix identifies the chain
// (solana_blocks_<from>_<to>.json). From replay/solana the shared directory
// is one level up, like it is from replay/tron.
const solanaFixtureDir = "../testdata"

// solanaGenesisHash is Mainnet's genesis hash, duplicated from
// pkg/chains/solana/constants.go (unexported there) so the mock stays
// Connect-compatible without importing the adapter's identity table.
const solanaGenesisHash = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"

// Fixture types duplicated from cmd/fetch_blocks: test packages cannot
// import a main package, so the on-disk format is mirrored here. Keep both
// sides in sync when the capture format changes.
//
// The capture records only the slots the chain filled: each entry carries
// the verbatim getBlock result and receipt_mode "embedded" (the receipts-
// equivalent metadata lives inside the block body, and Solana has no
// separate receipts endpoint to call). Gaps between entries are chain-
// skipped slots, which the replay mock serves as the null getBlock result.

// solanaFixtureBlock holds one slot's verbatim node response.
type solanaFixtureBlock struct {
	Number      string          `json:"number"`       // decimal slot (Solana has no hex encoding)
	Block       json.RawMessage `json:"block"`        // getBlock result, verbatim
	Receipts    json.RawMessage `json:"receipts"`     // always null: the block body embeds the metadata
	ReceiptMode string          `json:"receipt_mode"` // "embedded"
}

// solanaFixtureMeta describes the capture; it carries no endpoint or key
// material.
type solanaFixtureMeta struct {
	Chain          string `json:"chain"`
	Network        string `json:"network"`
	From           uint64 `json:"from"`
	To             uint64 `json:"to"`
	BlockCount     int    `json:"block_count"`
	CapturedAt     string `json:"captured_at"`
	CapturePartial bool   `json:"capture_partial,omitempty"`
}

// solanaReplayFixture is the on-disk format produced by cmd/fetch_blocks
// --adapter solana.
type solanaReplayFixture struct {
	Meta   solanaFixtureMeta    `json:"meta"`
	Blocks []solanaFixtureBlock `json:"blocks"`
}

// TestMain initializes the console-only logger for the acceptance suite.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// loadSolanaFixture reads and validates a capture fixture for the Solana suite.
func loadSolanaFixture(t *testing.T, path string) *solanaReplayFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}

	var fx solanaReplayFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	// The chain label is free-form (it is typed at capture time), so match
	// case-insensitively; collection prefixes remain canonical downstream.
	if !strings.EqualFold(fx.Meta.Chain, "solana") {
		t.Fatalf("fixture %s is for chain %q, want solana", path, fx.Meta.Chain)
	}
	// Partial captures replay fine on purpose (the FIXTURE env), but
	// the verdict covers only what the file contains, so keep that visible:
	// an unexplained 20-slot run would otherwise pass unnoticed.
	if fx.Meta.CapturePartial {
		logger.Testf("WARNING: fixture %s is a partial capture (%d entries); the verdict covers only what it contains", path, len(fx.Blocks))
	}
	return &fx
}

// solanaRPCServer dispatches the production client's JSON-RPC surface
// (pkg/chains/solana/solana_client.go) from the fixture, serving the node's
// result bytes verbatim. getSlot answers one slot above the fixture's range
// end: strictly above every slot in the fixture, so an in-range gap slot
// (a chain skip the produced-only capture left out) classifies as
// ErrHeightSkipped (permanent) while a beyond-tip slot stays in the
// transient not-found loop, exactly the boundary the production fetcher
// draws in classifyMissingSlot. A slot the capped sample dropped serves the
// null result too and rides the same skip path. getGenesisHash answers the
// Mainnet hash so the mock also serves a Connect that pins the cluster.
type solanaRPCServer struct {
	server *httptest.Server
	blocks map[int64]solanaFixtureBlock // decimal slot -> fixture entry
	tip    int64                        // the getSlot answer
}

// newSolanaRPCServer indexes the fixture and starts the mock node.
func newSolanaRPCServer(fx *solanaReplayFixture) *solanaRPCServer {
	s := &solanaRPCServer{
		blocks: make(map[int64]solanaFixtureBlock, len(fx.Blocks)),
		tip:    int64(fx.Meta.To) + 1, //nolint:gosec // G115: a capture slot that overflows int64 would have failed capture
	}
	for _, fb := range fx.Blocks {
		num, err := strconv.ParseInt(fb.Number, 10, 64)
		if err != nil {
			continue // unreachable by slot; an unparsable slot would have failed capture
		}
		s.blocks[num] = fb
	}

	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// handle routes one JSON-RPC request against the fixture index. The id is
// echoed verbatim: the SDK's jsonrpc client matches responses by id.
func (s *solanaRPCServer) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	w.Header().Set("Content-Type", "application/json")
	respond := func(result any) {
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = w.Write(payload)
	}
	respondErr := func(code int, msg string) {
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error":   map[string]any{"code": code, "message": msg},
		})
		_, _ = w.Write(payload)
	}

	switch req.Method {
	case "getBlock":
		var params []json.RawMessage
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params) == 0 {
			respondErr(-32602, "mock: getBlock needs a slot parameter")
			return
		}
		var slot int64
		if err := json.Unmarshal(params[0], &slot); err != nil {
			respondErr(-32602, "mock: getBlock slot parameter is not a number")
			return
		}
		if fb, ok := s.blocks[slot]; ok {
			respond(fb.Block)
			return
		}
		// A slot absent from the fixture (a chain-skipped gap between
		// entries, or one the capped sample dropped) serves the null
		// result: the production client turns a null getBlock result into
		// its not-confirmed sentinel and the fetcher's tip comparison
		// classifies it, permanent below the tip and transient beyond it.
		respond(json.RawMessage("null"))
	case "getSlot":
		respond(s.tip)
	case "getGenesisHash":
		respond(solanaGenesisHash)
	default:
		respondErr(-32601, "mock: unexpected method "+req.Method)
	}
}

// Close shuts the mock node down.
func (s *solanaRPCServer) Close() { s.server.Close() }

// URL returns the mock node's base URL.
func (s *solanaRPCServer) URL() string { return s.server.URL }

// capSampleSolana applies the suite's REPLAY_MAX_BLOCKS cap for slow hosts,
// returning the capped entries' parsed slots, which are the run's measured
// block list. Slots parse as decimal: Solana has no hex encoding.
func capSampleSolana(t *testing.T, fx *solanaReplayFixture, maxBlocksEnvName string) []uint64 {
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
	return numbers
}

// solanaTimings is the suite's local per-slot timing collector, the Solana
// counterpart of the shared harness's ReplayTimings. The shared collector's
// readouts assume a contiguous block range, which EVM and Tron never leave,
// while a Solana fixture holds only the slots the chain filled and spans
// chain-skipped gaps, so the suite owns this collector and reads it over
// the entry list. The wrappers below feed it the same way the harness's
// wrappers feed the shared one.
type solanaTimings struct {
	mu      sync.Mutex
	samples map[int64]time.Duration
	phases  map[int64]replay.ReplayPhases
}

func newSolanaTimings() *solanaTimings {
	return &solanaTimings{
		samples: make(map[int64]time.Duration),
		phases:  make(map[int64]replay.ReplayPhases),
	}
}

func (rt *solanaTimings) recordFetch(blockNum int64, elapsed time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.phases[blockNum]
	p.Fetch = elapsed
	rt.phases[blockNum] = p
}

func (rt *solanaTimings) recordConvert(blockNum int64, elapsed time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.phases[blockNum]
	p.Convert = elapsed
	rt.phases[blockNum] = p
}

func (rt *solanaTimings) recordStore(blockNum int64, elapsed time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.phases[blockNum]
	p.Store += elapsed
	rt.phases[blockNum] = p
}

// endStore closes a slot's timing sample when its store lands, summing the
// phase durations recorded so far. A store the processor also failed leaves
// the sample unclosed, and the sample-count check then names the slot.
func (rt *solanaTimings) endStore(blockNum int64, stored bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !stored {
		return
	}
	p, ok := rt.phases[blockNum]
	if !ok {
		return
	}
	rt.samples[blockNum] = p.Fetch + p.Convert + p.Store
}

// solTimedFetcher wraps the chain fetcher, recording each slot's fetch
// duration into the suite's collector before passing through. Only a
// successful attempt records, so the stamp holds the duration of the
// attempt whose block actually flowed through (a chain-skipped gap slot
// fails its fetch and records nothing).
type solTimedFetcher struct {
	inner   chains.Fetcher
	timings *solanaTimings
}

func (f *solTimedFetcher) Connect(ctx context.Context) error { return f.inner.Connect(ctx) }

func (f *solTimedFetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	start := time.Now()
	res, err := f.inner.FetchBlock(ctx, height)
	if err == nil {
		f.timings.recordFetch(height, time.Since(start))
	}
	return res, err
}

func (f *solTimedFetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	return f.inner.FetchHighestBlockNumber(ctx)
}

// FetchBlockHeaderByHash and FetchFinalizedBlockNumber stay passthrough:
// the processor only calls them on the reorg path (walk-back fork-point
// reads), which is off the timed block pipeline.
func (f *solTimedFetcher) FetchBlockHeaderByHash(ctx context.Context, hash string) (chains.BlockHeader, error) {
	return f.inner.FetchBlockHeaderByHash(ctx, hash)
}

func (f *solTimedFetcher) FetchFinalizedBlockNumber(ctx context.Context) (int64, error) {
	return f.inner.FetchFinalizedBlockNumber(ctx)
}

func (f *solTimedFetcher) Close() error { return f.inner.Close() }

// solTimedConverter wraps the chain converter, recording each slot's convert
// duration into the suite's collector. Only Convert is measured; the
// replay loop's other converter traffic (pruner and snapshotter progress
// queries) passes through untouched.
type solTimedConverter struct {
	inner   chains.Converter
	timings *solanaTimings
}

func (c *solTimedConverter) Convert(ctx context.Context, rawBlock any) (chains.ConversionResult, error) {
	start := time.Now()
	res, err := c.inner.Convert(ctx, rawBlock)
	if err == nil {
		c.timings.recordConvert(extractSolanaBlockNum(res), time.Since(start))
	}
	return res, err
}

func (c *solTimedConverter) GetSchema() (string, error) { return c.inner.GetSchema() }

func (c *solTimedConverter) GetCollections() []string { return c.inner.GetCollections() }

func (c *solTimedConverter) Collections() chains.Collections { return c.inner.Collections() }

func (c *solTimedConverter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.inner.GetHighestStoredBlockNumber(ctx, n)
}

func (c *solTimedConverter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.inner.GetLowestStoredBlockNumber(ctx, n)
}

func (c *solTimedConverter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	return c.inner.GetDocIDsByBlockRange(ctx, n, from, to)
}

// solTimedStorer wraps the processor's BlockStorer, closing each slot's
// timing sample when its store lands. An already-exists outcome closes the
// sample too (the processor commits a block through that path), so the
// store phase spans every store attempt the slot took.
type solTimedStorer struct {
	inner   indexer.BlockStorer
	timings *solanaTimings
}

func (s *solTimedStorer) Store(ctx context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
	start := time.Now()
	res, err := s.inner.Store(ctx, result)
	s.timings.recordStore(extractSolanaBlockNum(result), time.Since(start))
	s.timings.endStore(extractSolanaBlockNum(result), err == nil || shinzoerrors.IsErrAlreadyExists(err))
	return res, err
}

func (s *solTimedStorer) SignExisting(ctx context.Context, result chains.ConversionResult, blockHash string, blockNumber int64) (string, error) {
	return s.inner.SignExisting(ctx, result, blockHash, blockNumber)
}

// extractSolanaBlockNum recovers a ConversionResult's slot the way the
// processor recovers the block hash: the block group is the one with a
// BlockHashField, and its first doc carries the number under BlockNumField.
func extractSolanaBlockNum(result chains.ConversionResult) int64 {
	for _, g := range result.Groups {
		if g.BlockHashField == "" || len(g.Docs) == 0 {
			continue
		}
		if num, ok := g.Docs[0][g.BlockNumField].(int64); ok {
			return num
		}
	}
	return 0
}

// orderedSolanaTimings returns the collected samples over the fixture's
// entry list in fixture order, requiring every entry to have produced one.
// The entry list is exactly the produced slots, so a missing sample names
// a block that never stored.
func orderedSolanaTimings(rt *solanaTimings, numbers []int64) (replay.BlockTimings, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, num := range numbers {
		if _, ok := rt.samples[num]; !ok {
			return nil, fmt.Errorf("block %d has no timing sample (never stored)", num)
		}
	}
	out := make(replay.BlockTimings, 0, len(numbers))
	for _, num := range numbers {
		out = append(out, rt.samples[num])
	}
	return out, nil
}

// solanaPhaseAverages returns the per-phase averages over the fixture's
// entry list, under the same completeness contract: a listed slot missing
// its phase records is named, never silently skipped. Sound because a
// total sample exists iff all three phase records do: a fetch success
// precedes its convert, and a convert precedes any store of that slot.
func solanaPhaseAverages(rt *solanaTimings, numbers []int64) (replay.ReplayPhases, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var total replay.ReplayPhases
	for _, num := range numbers {
		p, ok := rt.phases[num]
		if !ok {
			return replay.ReplayPhases{}, fmt.Errorf("block %d has no phase records (never fetched, converted, or stored)", num)
		}
		total.Fetch += p.Fetch
		total.Convert += p.Convert
		total.Store += p.Store
	}
	count := time.Duration(len(numbers))
	return replay.ReplayPhases{
		Fetch:   total.Fetch / count,
		Convert: total.Convert / count,
		Store:   total.Store / count,
	}, nil
}

// countSolanaInRange counts documents in a collection whose number field
// falls within [first, last]. Duplicate of the shared harness's count
// helper, which is unexported there and unreachable from this directory's
// package; the plain row query's shape is the one the production
// converter's progress queries rely on. limit must exceed the expected
// count, because a full page means the range holds more documents than
// expected, which callers treat as a mismatch.
func countSolanaInRange(ctx context.Context, n *node.Node, collection, numberField string, first, last int64, limit int) (int, error) {
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

// assertSolanaCorrectness is the "can index" half of the verdict, the
// suite's local counterpart of the shared harness's AssertCorrectness: the
// shared version asserts a gap-free contiguous range, which a Solana store
// never is, because the fixture spans chain-skipped slots the processor
// advances past without storing.
//
// The expected counts derive from the fixture's own entry list: every
// entry at or above the lowest surviving block must be stored (a fresh
// store holds only fixture blocks, so an absent entry inside the range
// would mean a silently lost block), and every stored block in the range
// must carry a BlockSignature (a signed block is a complete block). The
// pruner's informational disagreement line counts entries below lowest.
func assertSolanaCorrectness(t *testing.T, td *testutils.TestDefraDB, ctx context.Context, cols chains.Collections, converter chains.Converter, numbers []uint64, processed int, pruned int64) {
	t.Helper()

	// The produced-only capture plus the cap keeping a prefix of the
	// entries, and pruning only removing blocks from the low end, means
	// every entry at or above lowest must be in the store. lowest comes
	// from the same production query the pruner and snapshotter use, so
	// the verdict reads the store exactly the way the services do.
	first, last := int64(numbers[0]), int64(numbers[len(numbers)-1])
	lowest, err := converter.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err, "query the lowest stored block: the store holds no blocks, so nothing was indexed")
	require.GreaterOrEqual(t, lowest, first, "nothing can be stored below the fixture's first slot %d", first)

	expected := 0
	belowLowest := 0
	for _, num := range numbers {
		if int64(num) >= lowest {
			expected++
		} else {
			belowLowest++
		}
	}

	blockCount, err := countSolanaInRange(ctx, td.Node, cols.BlockCollection(),
		constants.NumberFieldName, lowest, last, processed+1)
	require.NoError(t, err)
	assert.Equal(t, expected, blockCount,
		"stored blocks must cover every fixture entry in %d-%d - a shortfall means a block silently failed or was pruned inside the range; slots between entries are chain skips and are absent by design",
		lowest, last)

	sigCount, err := countSolanaInRange(ctx, td.Node, cols.BlockSignatureCollection(),
		constants.BlockNumberFieldName, lowest, last, processed+1)
	require.NoError(t, err)
	assert.Equal(t, expected, sigCount,
		"every stored block in %d-%d must carry a BlockSignature - a shortfall means a block was committed with a partial store or its signature could not be created",
		lowest, last)
	logger.Testf("✓ correctness: blocks %d-%d stored with all %d fixture entries bridging the chain-skipped gaps, %d block signatures",
		lowest, last, expected, sigCount)

	// Informational, never failed: distinct pruned blocks are exactly the
	// entries below lowest (pruning only removes a low-end prefix), so the
	// pruner's counter should agree with the store history. A disagreement
	// reports the inflated tally instead of turning the indexer's verdict
	// into a service bookkeeping check.
	if pruned != int64(belowLowest) {
		logger.Testf("pruner counter disagreement: counted %d blocks, %d measured from the store (lowest %d)",
			pruned, belowLowest, lowest)
	}
}

// resolveSolanaTargetBlockTime parses the suite's TARGET_BLOCK_TIME when set
// (for example 400ms to gate an older fixture captured before the slot-time
// stages) and falls back to the default. Duplicated from the Tron suite's
// resolver: test packages in different directories cannot share unexported
// helpers, and promoting it to the shared harness would churn the EVM suites
// for ten lines.
func resolveSolanaTargetBlockTime(t *testing.T, envName string, def time.Duration, durationHint string) time.Duration {
	t.Helper()
	if raw := os.Getenv(envName); raw != "" {
		d, err := time.ParseDuration(raw)
		require.NoError(t, err, "invalid %s %q: want a duration like %s", envName, raw, durationHint)
		return d
	}
	return def
}

// solanaReplaySpec carries everything that differs between the chain suites,
// the Solana counterpart of the EVM suites' replaySpec and the Tron suite's
// tronReplaySpec: the chain label used for config validation and the
// verdict; the shipped config file the measurement boots from; the expected
// network identity inside that config; the env prefix composing the suite's
// variables (TEST_SOLANA_TARGET_BLOCK_TIME, TEST_SOLANA_REPLAY_MAX_BLOCKS,
// ...); the fixture glob; the target block interval with its parse error
// hint; and the make target the skip message points at for fixture capture.
type solanaReplaySpec struct {
	chainLabel         string
	configPath         string
	expectedNetwork    string
	envPrefix          string
	fixtureGlob        string
	defaultTarget      time.Duration
	targetDurationHint string
	skipHint           string
}

// runSolanaReplay answers "can we index Solana blocks at the tip": it
// replays a captured fixture through a mock JSON-RPC node into the full
// production pipeline and drives it with the real production orchestration,
// the indexer's ConcurrentBlockProcessor, with the worker count the shipped
// config carries. Output is Fetcher -> Converter -> BlockHandler.Store under
// the processor's worker pool, retry classification, and in-order commit.
//
// The verdict is a throughput gate: the effective per-block interval (wall
// clock from first dispatch to the last produced block's in-order commit,
// divided by the produced block count) must stay within the chain's slot
// interval, which is the same question as "do blocks pile up forever", a
// question concurrency answers differently than per-block latency does.
//
// blocks_per_minute pacing stays deliberately bypassed (0 disables the
// processor's rate limiter): this measures raw pipeline capacity against the
// chain's BPS, so the rate limiter must not soak up the measurement.
//
// Solana's chain-skipped slots ride the production paths unchanged: the
// fixture holds only produced slots, the mock serves the null getBlock
// result for the gap slots between entries, the fetcher classifies those
// heights as ErrHeightSkipped (the tip comparison decides), and the
// processor advances without storing, so a skip costs the pipeline nothing
// but the fetch, the way it does live. The fixture's entries carry the
// whole measurement: their count feeds the wall-clock division, the timing
// completeness check, and the correctness verdict's expected counts.
func runSolanaReplay(t *testing.T, spec solanaReplaySpec) {
	fixtureEnv := spec.envPrefix + "_REPLAY_FIXTURE"
	fixturePath := replay.ResolveFixturePath(t, solanaFixtureDir, spec.fixtureGlob, fixtureEnv)
	if fixturePath == "" {
		t.Skipf("no replay fixture found: run '%s' (needs SOLANA_RPC_URL) to capture one into %s, or point %s at an existing fixture", spec.skipHint, solanaFixtureDir, fixtureEnv)
	}

	fx := loadSolanaFixture(t, fixturePath)
	if len(fx.Blocks) == 0 {
		t.Skipf("fixture %s contains no entries - recapture it", fixturePath)
	}

	targetEnv := spec.envPrefix + "_TARGET_BLOCK_TIME"
	target := resolveSolanaTargetBlockTime(t, targetEnv, spec.defaultTarget, spec.targetDurationHint)
	// Every entry is a produced slot (the produced-only capture records
	// nothing else), so numbers is the full measured block list.
	numbers := capSampleSolana(t, fx, spec.envPrefix+"_REPLAY_MAX_BLOCKS")

	logger.Testf("%s replay acceptance: %d blocks from fixture %s, target %s per block",
		spec.chainLabel, len(numbers), fixturePath, target)

	mockNode := newSolanaRPCServer(fx)
	defer mockNode.Close()

	storePath := t.TempDir()
	cfg := replay.NewReplayConfig(t, spec.configPath, spec.chainLabel, spec.expectedNetwork, storePath,
		func(cfg *config.Config) {
			// The mock server replaces the endpoint; the WS hint gate, key
			// header, and archive route are blanked, so the replay fetches
			// blind (nil notifier is await-safe) with no credentials and no
			// endpoint or key literal leaks to a future consumer.
			cfg.Solana.NodeURL = mockNode.URL()
			cfg.Solana.WsURL = ""
			cfg.Solana.APIKey = ""
			cfg.Solana.APIKeyType = ""
			cfg.Solana.ArchiveRPCURL = ""
		})
	replay.ResolveReplayServices(t, cfg, spec.envPrefix)

	// Embedded DefraDB with the chain-prefixed schema. The ForChain variant
	// is required: LoadSchemaSDL returns the raw embedded default-prefix
	// content without the prefix swap, which would create the wrong
	// collections.
	cols, err := solana.NewCollectionsFromConfig(cfg)
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
	// endpoint is already bound and the genesis check would only re-read
	// the constant the mock serves. The client carries the shipped yaml's
	// commitment, version ceiling, and rewards flag, so the replay's
	// getBlock calls match the capture's calls everywhere but the endpoint.
	client, err := solana.NewClient(ctx, solana.ClientOptions{
		RPCURL:                         mockNode.URL(),
		Commitment:                     cfg.Solana.Commitment,
		MaxSupportedTransactionVersion: uint64(cfg.Solana.MaxSupportedTransactionVersion), //nolint:gosec // config validation normalizes this to a positive default
		Rewards:                        cfg.Solana.RewardsEnabled(),
	})
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	fetcher := solana.NewFetcher(client)
	converter := solana.NewConverter(cfg)
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

	// The suite's own timing collector (see solanaTimings): the shared
	// harness's readouts are range-based and its collector's maps are
	// unexported, so the gap-tolerant readouts live here with the wrappers
	// that feed them.
	timings := newSolanaTimings()
	processor := indexer.NewConcurrentBlockProcessor(
		&solTimedFetcher{inner: fetcher, timings: timings},
		&solTimedConverter{inner: converter, timings: timings},
		&solTimedStorer{inner: handler, timings: timings},
		nil, // no reorgs in a replay: the fixture range cannot fork, and nil makes any detected reorg fatal instead
		workers,
		0, // pacing off: blocks_per_minute's rate limiter must not soak up the measurement
	)

	// The dispatcher runs to infinity; the run ends when the last fixture
	// entry commits in order (chain-skipped gap slots advance without a
	// commit, so they never terminate the run). At that point every entry
	// is stored, so the ctx cancel is the expected termination, not a
	// failure. The wall clock stops at that same commit: ProcessBlocks'
	// return additionally waits out the in-flight beyond-tip fetch
	// retries, which are not part of the measurement.
	first, last := int64(numbers[0]), int64(numbers[len(numbers)-1])
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	watchdog := replay.StartStallWatchdog(len(numbers), cancelRun)
	defer watchdog.Stop()

	var wallEnd time.Time
	wallStart := time.Now()
	err = processor.ProcessBlocks(runCtx, first, func(blockNum int64) {
		done := watchdog.RecordCommit()
		if done%25 == 0 {
			logger.Testf("committed %d/%d blocks (last: %d)", done, len(numbers), blockNum)
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

	slotNumbers := make([]int64, len(numbers))
	for i, num := range numbers {
		slotNumbers[i] = int64(num) //nolint:gosec // G115: an unparsable or negative slot would have failed capture
	}

	timingsList, err := orderedSolanaTimings(timings, slotNumbers)
	require.NoError(t, err, "every fixture entry must have a timing sample - a missing sample means a block never stored (blocked by a failed fetch/convert/store or the stall watchdog)")
	require.Len(t, timingsList, len(numbers),
		"sample count must equal the entry count - a missing sample means a block failed inside the processor")

	phaseAvg, err := solanaPhaseAverages(timings, slotNumbers)
	require.NoError(t, err, "every fixture entry must have complete phase records - fetch success precedes convert, convert precedes any store, so a total sample implies all three")

	// Stop the services before the report and assertions: the pruner must
	// not race the final count queries, and the snapshotter's stats are
	// final once stopped. Idempotent: the Cleanup registered at startup
	// becomes a no-op after this.
	services.Stop()

	replay.ReportResults(t, len(numbers), numbers, timingsList, phaseAvg, workers, wall, target)
	replay.LogServiceStats(t, services)
	replay.AssertVerdict(t, spec.chainLabel, numbers, wall, workers, target)
	assertSolanaCorrectness(t, td, ctx, cols, converter, numbers, len(numbers), services.PrunedBlocks())
}
