//go:build acceptance
// +build acceptance

package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scrubReplayEnv unsets every env override applyEnvOverrides honors so the
// shipped config yaml is the only input the config loader sees. The Makefile
// exports the developer's .env into every target, and without this a stray
// INDEXER_MAX_DOCS_PER_TXN or badger cache value would silently change the
// measured configuration between machines. Originals are restored after the
// test so nothing outside LoadConfig's window observes the scrub.
func scrubReplayEnv(t *testing.T) {
	t.Helper()

	prefixes := [...]string{
		"CHAIN_", "GETH_", "DEFRADB_", "INDEXER_", "PRUNER_", "SNAPSHOT_",
		"SCHEMA_", "CONVERTER_", "LOGGER_", "SHINZOHUB_",
	}

	for _, key := range os.Environ() {
		key = key[:strings.IndexByte(key, '=')]
		matched := false
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		old, had := os.LookupEnv(key)
		require.NoError(t, os.Unsetenv(key))
		t.Cleanup(func() {
			if had {
				require.NoError(t, os.Setenv(key, old))
			} else {
				require.NoError(t, os.Unsetenv(key))
			}
		})
	}
}

// Forced-fast service deltas from the shipped config: retention well below
// the default capture range produces real deletions, and a small
// blocks-per-file produces real snapshot writes. ResolveReplayServices
// applies the chain's REPLAY_* overrides on top.
const (
	replayPruneIntervalSeconds    = 10
	replayPruneMaxBlocks          = 50
	replaySnapshotIntervalSeconds = 10
	replaySnapshotBlocksPerFile   = 50
)

// ResolveReplayServices applies the pruner/snapshotter kill-switches and
// forced-fast overrides on top of NewReplayConfig's defaults. envPrefix is
// the chain's env namespace ("ETHEREUM", "BSC"), which composes the kill
// switch and tuning variables the same way every suite names them.
func ResolveReplayServices(t *testing.T, cfg *config.Config, envPrefix string) {
	t.Helper()

	prunerSwitch := envPrefix + "_REPLAY_PRUNER"
	if replayServiceOn(t, prunerSwitch) {
		cfg.Pruner.Enabled = true
		cfg.Pruner.IntervalSeconds = replayServiceInt(t, prunerSwitch+"_INTERVAL_SECONDS", replayPruneIntervalSeconds)
		cfg.Pruner.MaxBlocks = int64(replayServiceInt(t, prunerSwitch+"_MAX_BLOCKS", replayPruneMaxBlocks))
	} else {
		logger.Testf("pruner disabled via %s=off", prunerSwitch)
		cfg.Pruner.Enabled = false
	}

	snapshotSwitch := envPrefix + "_REPLAY_SNAPSHOT"
	if replayServiceOn(t, snapshotSwitch) {
		cfg.Snapshot.Enabled = true
		cfg.Snapshot.IntervalSeconds = replayServiceInt(t, snapshotSwitch+"_INTERVAL_SECONDS", replaySnapshotIntervalSeconds)
		cfg.Snapshot.BlocksPerFile = int64(replayServiceInt(t, snapshotSwitch+"_BLOCKS_PER_FILE", replaySnapshotBlocksPerFile))
	} else {
		logger.Testf("snapshotter disabled via %s=off", snapshotSwitch)
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

// NewReplayConfig boots from the suite's shipped config yaml, the same file
// production block_poster runs with -config, and overrides only what the
// harness cannot take from it: the mock node as endpoint, the temp store,
// P2P off, no health server, open schema, the throwaway keyring, and the
// forced-fast pruner + snapshotter cadence. Everything else (chain identity,
// batch sizes, prune_history, the per-cycle cap, badger caches, the 128 MB
// value log) is exactly what production runs, and the env scrub keeps
// developer .env values out of the measurement.
//
// expectedChain and expectedNetwork fail the run when the shipped yaml does
// not define the chain the suite claims: loading the wrong family's config
// would create the wrong collections and poison every measurement downstream.
func NewReplayConfig(t *testing.T, configPath, expectedChain, expectedNetwork, nodeURL, storePath string) *config.Config {
	t.Helper()

	scrubReplayEnv(t)

	// config.yaml ships schema_auth_mode as a literal "${SCHEMA_AUTH_MODE}"
	// placeholder: yaml parsing never expands it, and the scrub above removes
	// the env var applyEnvOverrides would normally read to fill it in, so the
	// raw placeholder would fail schema-auth validation. Restoring a
	// known-good value after the scrub keeps the harness deterministic and
	// matches the open-schema mode applied to the loaded config below.
	t.Setenv("SCHEMA_AUTH_MODE", "none")

	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err, "load config yaml")
	require.Equal(t, expectedChain, cfg.Chain.Name, "config yaml must define the %s chain", expectedChain)
	require.Equal(t, expectedNetwork, cfg.Chain.Network, "config yaml must target %s", expectedNetwork)

	// The mock server replaces the endpoint; the geth section name is
	// historical (any JSON-RPC endpoint works), and the ${GETH_*} yaml
	// placeholders are zeroed so no literal leaks to a future consumer.
	cfg.Geth.NodeURL = nodeURL
	cfg.Geth.WsURL = ""
	cfg.Geth.APIKey = ""
	cfg.Geth.APIKeyType = ""

	// Harness-only DefraDB settings: temp store, no peers, throwaway signing
	// identity derived from the chain label so each run also fills its own
	// keyring slot. The store cache sizes and 128 MB value log ride from the
	// yaml, matching the shipped configuration.
	cfg.DefraDB.Store.Path = storePath
	cfg.DefraDB.P2P.Enabled = false
	cfg.DefraDB.KeyringSecret = strings.ToLower(expectedChain) + "-replay-keyring-secret"

	// The harness never starts the indexer, so the health server would
	// never bind, pinned off anyway, and schema auth stays open because
	// the harness applies its own schema without tokens.
	cfg.Indexer.HealthServerPort = -1
	cfg.Indexer.SchemaAuthMode = "none"

	// Forced-fast service pacing on top of the yaml's production values:
	// retention well below the default capture produces real deletions
	// mid-run, and a small blocks-per-file produces real snapshot writes.
	// ResolveReplayServices applies the kill-switches and overrides on top
	// of these.
	cfg.Pruner.MaxBlocks = replayPruneMaxBlocks
	cfg.Pruner.IntervalSeconds = replayPruneIntervalSeconds

	cfg.Snapshot.Dir = filepath.Join(storePath, "snapshots")
	cfg.Snapshot.BlocksPerFile = replaySnapshotBlocksPerFile
	cfg.Snapshot.IntervalSeconds = replaySnapshotIntervalSeconds

	cfg.Logger.Development = false

	return cfg
}

// ResolveFixturePath picks the fixture to replay: the suite's FIXTURE env wins
// when set, otherwise the newest capture matching glob inside dir is used.
// An empty result means none found, which the caller reports as a skip.
func ResolveFixturePath(t *testing.T, dir, glob, envVar string) string {
	t.Helper()
	if explicit := os.Getenv(envVar); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			t.Fatalf("%s %s not readable: %v", envVar, explicit, err)
		}
		return explicit
	}

	matches, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil || len(matches) == 0 {
		return ""
	}

	// Newest complete capture wins; the filename embeds the block range,
	// not the capture time, so modtime is the only reliable ordering.
	// _partial files are skipped: an interrupted capture must neither
	// preempt the newest complete capture (a newer modtime would win) nor,
	// when the interrupt hit before any block saved, starve the suite of
	// the complete fixture sitting in the same directory. Partial captures
	// still run on purpose via the FIXTURE env.
	newest := ""
	var newestMod time.Time
	for _, match := range matches {
		// Match the name's ending, not a substring anywhere in the path. The
		// _partial.json suffix mirrors the constant pair in cmd/fetch_blocks
		// (test packages cannot import a main package).
		if strings.HasSuffix(filepath.Base(match), "_partial.json") {
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

// ParsePositiveInt parses a positive integer or returns an error.
func ParsePositiveInt(raw string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", n)
	}
	return n, nil
}

// graphqlCountInRange counts documents in a collection whose number field
// falls within [first, last]. It deliberately avoids aggregate queries:
// DefraDB v1's aggregate response shape proved unstable across versions,
// while the plain row query's shape (data[collection] to []any) is the same
// one the production converter's progress queries rely on. limit must
// exceed the expected count, because a full page means the range holds more
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

// BlockTimings collects per-block end-to-end processing durations.
type BlockTimings []time.Duration

// sorted returns the timings ordered ascending.
func (bt BlockTimings) sorted() []time.Duration {
	out := make(BlockTimings, len(bt))
	copy(out, bt)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// min returns the shortest duration.
func (bt BlockTimings) min() time.Duration {
	sorted := bt.sorted()
	return sorted[0]
}

// max returns the longest duration.
func (bt BlockTimings) max() time.Duration {
	sorted := bt.sorted()
	return sorted[len(sorted)-1]
}

// avg returns the mean duration.
func (bt BlockTimings) avg() time.Duration {
	var total time.Duration
	for _, d := range bt {
		total += d
	}
	return total / time.Duration(len(bt))
}

// percentile returns the p-th percentile (0..100) of the timings.
func (bt BlockTimings) percentile(p int) time.Duration {
	sorted := bt.sorted()
	idx := (len(sorted) - 1) * p / 100
	return sorted[idx]
}

// outlierLines lists blocks whose processing time exceeded twice the
// average, formatted as "block no - time - deviation from average" for the
// report's outlier list.
func (bt BlockTimings) outlierLines(numbers []uint64, avg time.Duration) []string {
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

// ReplayPhases holds one block's per-phase durations: the fetch call, the
// convert call, and the total time spent in store attempts.
type ReplayPhases struct {
	Fetch   time.Duration
	Convert time.Duration
	Store   time.Duration
}

// ReplayTimings collects per-block processing durations and their
// fetch/convert/store phase breakdown in the concurrent replay.
// A block's timing sample is the sum of its three phase durations, not a
// fetch-to-store wall window: the processor runs fetch, convert, and store
// in separate worker pools, so the wall window between a fetch success and
// the store's completion is dominated by inter-stage queue wait, idleness
// that says nothing about pipeline capacity. The phase sum counts only the
// time the pipeline actually worked the block.
// The ConcurrentBlockProcessor exposes no per-block timing hook, so the
// wrappers around its Fetcher, Converter, and BlockStorer interfaces feed
// this collector: the fetcher records the fetch duration, the converter and
// storer record their phase durations, and the storer closes the sample.
// Completion order differs from block order under concurrency, so samples
// are keyed by block number and emit in ascending order. The fixture range
// is contiguous (capture invariant, and the sample cap keeps a prefix),
// which keeps the slice index-aligned with the parsed block numbers for the
// outlier list.
type ReplayTimings struct {
	mu      sync.Mutex
	samples map[int64]time.Duration
	phases  map[int64]ReplayPhases
}

// NewTimings creates an empty collector.
func NewTimings() *ReplayTimings {
	return &ReplayTimings{
		samples: make(map[int64]time.Duration),
		phases:  make(map[int64]ReplayPhases),
	}
}

// endStore closes a block's timing sample when its store lands, summing the
// phase durations recorded so far: fetch (the successful attempt), convert,
// and every store attempt. recordStore has already folded the just-finished
// attempt in by the time this runs. A store the processor also failed leaves
// the sample unclosed. Retried stores re-sum on the attempt that lands, and
// the sample-count check then names a block that never landed at all.
func (rt *ReplayTimings) endStore(blockNum int64, stored bool) {
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

// recordFetch stamps a block's fetch duration. Processor-level fetch retries
// re-enter FetchBlock, and only a successful attempt records, so the stamp
// holds the duration of the attempt whose block actually flowed through.
func (rt *ReplayTimings) recordFetch(blockNum int64, elapsed time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.phases[blockNum]
	p.Fetch = elapsed
	rt.phases[blockNum] = p
}

// recordConvert stamps a block's convert duration. Converts are single
// attempts, so the stamp is written once per block.
func (rt *ReplayTimings) recordConvert(blockNum int64, elapsed time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.phases[blockNum]
	p.Convert = elapsed
	rt.phases[blockNum] = p
}

// recordStore adds one store attempt's duration to a block's store phase.
// Every attempt accumulates, success, already-exists, or failure, so the
// store phase is the total time the block spent storing under contention
// (processor-level retries re-enter Store, and the batch-retry conflict
// sleeps live inside it).
func (rt *ReplayTimings) recordStore(blockNum int64, elapsed time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.phases[blockNum]
	p.Store += elapsed
	rt.phases[blockNum] = p
}

// SnapshotOrdered returns the collected samples as a BlockTimings slice in
// ascending block-number order, requiring every block in the inclusive range
// to have produced one.
func (rt *ReplayTimings) SnapshotOrdered(first, last int64) (BlockTimings, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for num := first; num <= last; num++ {
		if _, ok := rt.samples[num]; !ok {
			return nil, fmt.Errorf("block %d has no timing sample (never stored)", num)
		}
	}
	out := make(BlockTimings, 0, last-first+1)
	for num := first; num <= last; num++ {
		out = append(out, rt.samples[num])
	}
	return out, nil
}

// PhaseAverages returns the per-phase averages over the inclusive block
// range, under the same completeness contract as SnapshotOrdered: a block
// missing from the range is named, never silently skipped. Sound because a
// total sample exists iff all three phase records do: a fetch success
// precedes its convert, and a convert precedes any store of that block.
func (rt *ReplayTimings) PhaseAverages(first, last int64) (ReplayPhases, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var total ReplayPhases
	for num := first; num <= last; num++ {
		p, ok := rt.phases[num]
		if !ok {
			return ReplayPhases{}, fmt.Errorf("block %d has no phase records (never fetched, converted, or stored)", num)
		}
		total.Fetch += p.Fetch
		total.Convert += p.Convert
		total.Store += p.Store
	}
	count := time.Duration(last - first + 1)
	return ReplayPhases{
		Fetch:   total.Fetch / count,
		Convert: total.Convert / count,
		Store:   total.Store / count,
	}, nil
}

// TimedFetcher wraps a chains.Fetcher, recording each block's fetch duration
// into a ReplayTimings collector before passing through. It exists because
// the timing is not exposed by the orchestration layer, so the harness
// instruments the interfaces it owns.
type TimedFetcher struct {
	inner   chains.Fetcher
	timings *ReplayTimings
}

// NewTimedFetcher returns a timing wrapper around inner.
func NewTimedFetcher(inner chains.Fetcher, timings *ReplayTimings) *TimedFetcher {
	return &TimedFetcher{inner: inner, timings: timings}
}

func (f *TimedFetcher) Connect(ctx context.Context) error { return f.inner.Connect(ctx) }

func (f *TimedFetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	start := time.Now()
	res, err := f.inner.FetchBlock(ctx, height)
	if err == nil {
		f.timings.recordFetch(height, time.Since(start))
	}
	return res, err
}

func (f *TimedFetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	return f.inner.FetchHighestBlockNumber(ctx)
}

// FetchBlockHeaderByHash and FetchFinalizedBlockNumber stay passthrough:
// the processor only calls them on the reorg path (walk-back fork-point
// reads), which is off the timed block pipeline.
func (f *TimedFetcher) FetchBlockHeaderByHash(ctx context.Context, hash string) (chains.BlockHeader, error) {
	return f.inner.FetchBlockHeaderByHash(ctx, hash)
}

func (f *TimedFetcher) FetchFinalizedBlockNumber(ctx context.Context) (int64, error) {
	return f.inner.FetchFinalizedBlockNumber(ctx)
}

func (f *TimedFetcher) Close() error { return f.inner.Close() }

// TimedConverter wraps a chains.Converter, recording each block's convert
// duration into a ReplayTimings collector. Only Convert is measured: the
// replay loop's other converter traffic (pruner/snapshot progress queries)
// is either processor internals or background services, so everything else
// passes through untouched.
type TimedConverter struct {
	inner   chains.Converter
	timings *ReplayTimings
}

// NewTimedConverter returns a timing wrapper around inner.
func NewTimedConverter(inner chains.Converter, timings *ReplayTimings) *TimedConverter {
	return &TimedConverter{inner: inner, timings: timings}
}

func (c *TimedConverter) Convert(ctx context.Context, rawBlock any) (chains.ConversionResult, error) {
	start := time.Now()
	res, err := c.inner.Convert(ctx, rawBlock)
	if err == nil {
		c.timings.recordConvert(extractReplayBlockNum(res), time.Since(start))
	}
	return res, err
}

func (c *TimedConverter) GetSchema() (string, error) { return c.inner.GetSchema() }

func (c *TimedConverter) GetCollections() []string { return c.inner.GetCollections() }

func (c *TimedConverter) Collections() chains.Collections { return c.inner.Collections() }

func (c *TimedConverter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.inner.GetHighestStoredBlockNumber(ctx, n)
}

func (c *TimedConverter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.inner.GetLowestStoredBlockNumber(ctx, n)
}

func (c *TimedConverter) GetStoredBlockNumbers(ctx context.Context, n *node.Node, from int64) ([]int64, error) {
	return c.inner.GetStoredBlockNumbers(ctx, n, from)
}

func (c *TimedConverter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	return c.inner.GetDocIDsByBlockRange(ctx, n, from, to)
}

// TimedStorer wraps the processor's BlockStorer, closing each block's timing
// sample when its store lands. The processor's own extractBlockHash finds
// the block group by its BlockHashField; the number is recovered the same
// way via the group's BlockNumField ("number" on block groups per the
// converter). An already-exists outcome closes the sample too: the processor
// commits a block through that path (it fires the detached SignExisting and
// reports success), so the store phase spans every store attempt the block
// took. Only a store the processor also failed leaves the sample unclosed,
// and the sample-count check then names the block, so silence cannot hide a
// lost one. The store phase sums every attempt's duration (see recordStore),
// and SignExisting stays passthrough: it is processor-detached and tiny.
type TimedStorer struct {
	inner   indexer.BlockStorer
	timings *ReplayTimings
}

// NewTimedStorer returns a timing wrapper around inner.
func NewTimedStorer(inner indexer.BlockStorer, timings *ReplayTimings) *TimedStorer {
	return &TimedStorer{inner: inner, timings: timings}
}

func (s *TimedStorer) Store(ctx context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
	start := time.Now()
	res, err := s.inner.Store(ctx, result)
	s.timings.recordStore(extractReplayBlockNum(result), time.Since(start))
	s.timings.endStore(extractReplayBlockNum(result), err == nil || errors.IsErrAlreadyExists(err))
	return res, err
}

func (s *TimedStorer) SignExisting(ctx context.Context, result chains.ConversionResult, blockHash string, blockNumber int64) (string, error) {
	return s.inner.SignExisting(ctx, result, blockHash, blockNumber)
}

// extractReplayBlockNum recovers a ConversionResult's block number the way
// the processor recovers the block hash: the block group is the one with a
// BlockHashField, and its first doc carries the number under BlockNumField.
func extractReplayBlockNum(result chains.ConversionResult) int64 {
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

// Stall watchdog cadence: no commit for stallLimit while blocks remain cancels the run.
const (
	stallLimit      = 30 * time.Second
	stallCheckEvery = 5 * time.Second
)

// StallWatchdog guards the replay's only happy cancel path, which is the
// last block's commit. A block that fails inside the processor (fetch,
// convert, or store error) is logged as a warning instead of committed, so
// nextToCommit never advances and the run would hang to the go-test timeout.
// The watchdog keeps that honest: no commit for stallLimit while blocks
// remain cancels the run, and the missing timing sample then names the
// stuck block.
type StallWatchdog struct {
	totalBlocks int
	cancelRun   context.CancelFunc
	lastCommit  atomic.Int64
	commitCount atomic.Int64
	stop        chan struct{}
	stopOnce    sync.Once
}

// StartStallWatchdog starts the stall ticker that cancels the run when no
// block commits for stallLimit. Stop it on every exit path.
func StartStallWatchdog(totalBlocks int, cancelRun context.CancelFunc) *StallWatchdog {
	w := &StallWatchdog{
		totalBlocks: totalBlocks,
		cancelRun:   cancelRun,
		stop:        make(chan struct{}),
	}
	w.lastCommit.Store(time.Now().UnixNano())
	go w.watch()
	return w
}

func (w *StallWatchdog) watch() {
	tick := time.NewTicker(stallCheckEvery)
	defer tick.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-tick.C:
			if w.commitCount.Load() >= int64(w.totalBlocks) {
				continue // all committed; the last-block cancel is winding the run down
			}
			if time.Since(time.Unix(0, w.lastCommit.Load())) > stallLimit {
				logger.Testf("no commit for %s with %d/%d blocks committed - cancelling the stalled replay", stallLimit, w.commitCount.Load(), w.totalBlocks)
				w.cancelRun()
			}
		}
	}
}

// RecordCommit folds one committed block into the watchdog's liveness
// signals and returns the committed count so far, which the run loop uses
// for its periodic progress print.
func (w *StallWatchdog) RecordCommit() int64 {
	done := w.commitCount.Add(1)
	w.lastCommit.Store(time.Now().UnixNano())
	return done
}

// Stop ends the watchdog goroutine. Idempotent so the happy path and the
// deferred cleanup can both call it.
func (w *StallWatchdog) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

// ReportResults prints the timing report line by line in the replay
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
func ReportResults(t *testing.T, blockCount int, numbers []uint64, timings BlockTimings, phaseAvg ReplayPhases, workers int, wall time.Duration, target time.Duration) {
	t.Helper()
	avg := timings.avg()
	effective := wall / time.Duration(blockCount)

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
	// MAX_BLOCKS cap trims the prefix, so the fixture's To would name
	// blocks this run never touched.
	pad("Blocks Replayed: %d - %d   (%d blocks)", numbers[0], numbers[len(numbers)-1], blockCount)
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
	pad(" -  Average Phase Times:  fetch %s || convert %s || store %s", phaseAvg.Fetch, phaseAvg.Convert, phaseAvg.Store)
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

// AssertVerdict is the hard acceptance assertion: the effective per-block
// interval, wall clock from first dispatch to the last in-order commit
// divided by the block count, must stay within the chain's block interval,
// or the chain outruns the indexer and blocks pile up forever. Per-block
// latency is not gated: contention inflates individual latencies even when
// the concurrent pipeline comfortably keeps up. Its failure message names
// the replayed sample's range (numbers may be a capped prefix of the
// fixture), not the fixture's full range.
func AssertVerdict(t *testing.T, chainLabel string, numbers []uint64, wall time.Duration, workers int, target time.Duration) {
	t.Helper()
	blockCount := len(numbers)
	effective := wall / time.Duration(blockCount)
	first, last := numbers[0], numbers[len(numbers)-1]
	require.LessOrEqual(t, effective, target,
		"acceptance FAILED: effective per-block interval %s exceeds the %s block interval at %d workers (blocks %d..%d) - the concurrent pipeline cannot keep up with %s at the tip",
		effective, target, workers, first, last, chainLabel)
}

// AssertCorrectness is the "can index" half of the verdict, and it reads the
// store itself: the block collection must fill the whole range from the
// lowest stored block to the last with no gaps, and every block in that
// range must carry a BlockSignature (a signed block is a complete block).
// The verdict does not consume the pruner's block counter: the counter is a
// deletion-event tally that can count one block under both of the pruner's
// strategies, so a healthy store can legitimately disagree with
// `processed - pruned`. The counter still feeds an informational
// disagreement line below. Signature shortfalls name the real damage, not a
// counting error: a block committed through the already-exists path without
// a signature counts toward zero here. This is the assertion that catches
// lost blocks under concurrent write contention.
func AssertCorrectness(t *testing.T, td *testutils.TestDefraDB, ctx context.Context, cols chains.Collections, converter chains.Converter, numbers []uint64, processed int, pruned int64) {
	t.Helper()

	// The capture is a contiguous range and the cap keeps a prefix of it, and
	// pruning only removes blocks from the low end, so the surviving store is
	// a contiguous [lowest, last]. lowest comes from the same production
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

// replayQueueTracker adapts pruner's IndexerQueue to the BlockHandler's
// docID tracker interface, the same adapter pkg/indexer wires in production,
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

// ReplayServices groups the background services running beside the timing
// loop. Both are optional: nil fields mean the service was disabled. The
// stats structs aggregate the work-cycle durations their observers feed;
// stopOnce makes stop idempotent: Cleanup and the happy path both call it.
type ReplayServices struct {
	pruneSvc   *pruner.Pruner
	snapSvc    *snapshot.Snapshotter
	pruneStats replayCycleStats
	snapStats  replayCycleStats
	stopOnce   sync.Once
}

// StartReplayServices wires the pruner and snapshotter the way the indexer's
// initServices does: queue bound to its file, docID tracker on the handler,
// then Start on the identity context (the snapshotter signs with it). Their
// background cycles run while blocks are replayed, so prune and snapshot IO
// land in the measurements the way production background load does.
func StartReplayServices(t *testing.T, cfg *config.Config, defraNode *node.Node, converter chains.Converter, handler *defra.BlockHandler, ctx context.Context) *ReplayServices {
	t.Helper()

	rs := &ReplayServices{}

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
		// The observer must be registered before Start: the loop's startup
		// cleanup is itself a prune cycle and its facts would otherwise be
		// missed. The seam reports every cycle; deciding that only cycles
		// which deleted blocks count as work is harness policy, kept here
		// so the package stays free of bench semantics.
		rs.pruneSvc.SetCycleObserver(func(ev pruner.CycleEvent) {
			if ev.Blocks <= 0 {
				return
			}
			logger.Testf("prune cycle: +%d blocks / +%d docs in %v", ev.Blocks, ev.Docs, ev.Duration)
			rs.pruneStats.observe(ev.Duration)
		})
		handler.SetDocIDTracker(&replayQueueTracker{queue: pruneQueue})
		require.NoError(t, rs.pruneSvc.Start(ctx))
		logger.Testf("pruner started: interval %ds, retention %d blocks", cfg.Pruner.IntervalSeconds, cfg.Pruner.MaxBlocks)
	}

	if cfg.Snapshot.Enabled {
		rs.snapSvc = snapshot.New(&cfg.Snapshot, defraNode, converter)
		// Same policy split as the pruner's observer: every cycle is
		// reported, only snapshot-creating cycles count as work here.
		rs.snapSvc.SetCycleObserver(func(ev snapshot.CycleEvent) {
			if ev.Snapshots <= 0 {
				return
			}
			logger.Testf("snapshot cycle: +%d snapshot in %v", ev.Snapshots, ev.Duration)
			rs.snapStats.observe(ev.Duration)
		})
		require.NoError(t, rs.snapSvc.Start(ctx))
		logger.Testf("snapshotter started: dir %s, interval %ds, blocks per file %d",
			cfg.Snapshot.Dir, cfg.Snapshot.IntervalSeconds, cfg.Snapshot.BlocksPerFile)
	}

	// Register teardown for every exit path: if a require in the replay loop
	// fails, the test goroutine stops there and Cleanup must stop the
	// services before the node is torn down, or the buried errors from a
	// pruner/snapshotter racing the dying node drown out the real failure.
	t.Cleanup(rs.Stop)

	return rs
}

// Stop tears the services down in the indexer's stop order: the snapshotter
// first (capture data before it is pruned), then the pruner so it cannot race
// the test's final count queries. Idempotent via stopOnce: the snapshotter's
// Stop closes its channel unguarded, so a second call would panic.
func (rs *ReplayServices) Stop() {
	rs.stopOnce.Do(func() {
		if rs.snapSvc != nil {
			rs.snapSvc.Stop()
		}
		if rs.pruneSvc != nil {
			rs.pruneSvc.Stop()
		}
	})
}

// PrunedBlocks returns the count of blocks the pruner deleted during the
// run; zero when the pruner is disabled.
func (rs *ReplayServices) PrunedBlocks() int64 {
	if rs.pruneSvc == nil {
		return 0
	}
	return rs.pruneSvc.GetMetrics().TotalBlocksPruned
}

// LogServiceStats prints each service's outcome below the report box so the
// fixed-width report format stays untouched. Each service prints a summary
// line followed by a duration line; a service that ran no work cycles prints
// zeros rather than dividing by a zero cycle count. Cycle counts and
// durations come from the harness's own observer aggregation, not from the
// services' metrics.
func LogServiceStats(t *testing.T, rs *ReplayServices) {
	t.Helper()
	if rs.pruneSvc != nil {
		m := rs.pruneSvc.GetMetrics()
		logger.Testf("Pruner: %d blocks / %d docs across %d cycles", m.TotalBlocksPruned, m.TotalDocsPruned, rs.pruneStats.cycles())
		total, avg, peak := rs.pruneStats.durations()
		logger.Testf(" - Pruning Duration: %s total, %s average, %s max", total, avg, peak)
	}
	if rs.snapSvc != nil {
		m := rs.snapSvc.GetMetrics()
		logger.Testf("Snapshots: %d", m.TotalSnapshots)
		total, avg, peak := rs.snapStats.durations()
		logger.Testf(" - Snapshots Duration: %s total, %s average, %s max", total, avg, peak)
	}
}

// replayCycleStats aggregates work-cycle durations for one background
// service. The packages' observer seams report raw per-cycle facts; which
// cycles count as work and the total/average/max rollup are replay-harness
// policy, kept here so the packages stay free of bench semantics.
type replayCycleStats struct {
	mu    sync.Mutex
	count int64
	total time.Duration
	peak  time.Duration
}

// observe folds one work cycle's wall time into the rollup. Called from the
// service goroutines through the observers, hence the mutex.
func (cs *replayCycleStats) observe(d time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.count++
	cs.total += d
	if d > cs.peak {
		cs.peak = d
	}
}

// cycles reports how many work cycles were observed.
func (cs *replayCycleStats) cycles() int64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.count
}

// durations returns total / average / max over the observed work cycles. A
// zero cycle count means no cycle ever did work, so all three answer zero.
func (cs *replayCycleStats) durations() (time.Duration, time.Duration, time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.count <= 0 {
		return 0, 0, 0
	}
	return cs.total, cs.total / time.Duration(cs.count), cs.peak
}
