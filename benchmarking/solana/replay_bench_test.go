//go:build bench

package solanabench

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	solana "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
)

// ---------------------------------------------------------------------------
// Real-mainnet replay benchmark.
//
// Perf target: the indexer must process a mainnet block in <= 100ms on
// average. This harness replays captured mainnet getBlock payloads through
// the full production pipeline — Client decode, Converter, BlockHandler
// (embedded DefraDB + signing) — with the RPC transport local on loopback,
// so the measurement is client-side processing, not network latency.
//
// The fixture is captured once by cmd/bench_fetch (make solana-bench-fetch):
// it is hundreds of MB of raw node responses, gitignored, and must exist
// before the test runs. Skipped slots (null result / skip error codes) are
// replayed verbatim and excluded from the average, as in production.
// Report-only: a slow average prints an EXCEEDED verdict but never fails
// the test; only genuine pipeline errors do.
// ---------------------------------------------------------------------------

const (
	// replayFixturePath is the default capture produced by bench_fetch for
	// the benchmark slot range, relative to this package's directory.
	// Overridable via SOLANA_REPLAY_FIXTURE.
	replayFixturePath = "testdata/bench_replay_449791000_449791099.json"
	// replayTargetAvg is the per-block processing budget under evaluation.
	replayTargetAvg = 100 * time.Millisecond
	// replayWarmupSlot is the empty-but-complete block that primes schema,
	// DefraDB, and identity before any timed block. Its slot must stay far
	// from the fixture range: docIDs are content hashes, so re-storing a
	// warm-up block later would collide.
	replayWarmupSlot = 1

	// replayCodesMethodNotFound / InvalidParams mirror the JSON-RPC error
	// codes the built-in server convention answers for unmatched calls.
	replayCodeMethodNotFound = -32601
	replayCodeInvalidParams  = -32602
)

type replaySlotEntry struct {
	Slot   uint64              `json:"slot"`
	Result json.RawMessage     `json:"result"`
	Error  *replayRPCErrorInfo `json:"error"`
}

type replayRPCErrorInfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type replayFixture struct {
	StartSlot uint64            `json:"startSlot"`
	EndSlot   uint64            `json:"endSlot"`
	Blocks    []replaySlotEntry `json:"blocks"`
}

// loadReplayFixture reads the capture, skipping (never failing) the whole
// suite when it is absent — the fixture must not be committed.
func loadReplayFixture(t *testing.T) *replayFixture {
	t.Helper()

	path := os.Getenv("SOLANA_REPLAY_FIXTURE")
	if path == "" {
		path = replayFixturePath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("replay fixture %s not found: %v — generate it with: make solana-bench-fetch", path, err)
	}
	var fx replayFixture
	require.NoError(t, json.Unmarshal(raw, &fx), "fixture %s is not a valid capture", path)
	require.NotEmpty(t, fx.Blocks, "fixture %s holds no slot captures", path)
	requireFiniteRange(t, &fx)
	return &fx
}

// requireFiniteRange guards a corrupt capture: inverted or zeroed boundaries
// are not meaningful slot ranges.
func requireFiniteRange(t *testing.T, fx *replayFixture) {
	t.Helper()

	require.GreaterOrEqual(t, fx.EndSlot, fx.StartSlot, "fixture slot range inverted")
	require.Positive(t, fx.StartSlot, "fixture slot range missing")
}

// ---------------------------------------------------------------------------
// Replay server: a JSON-RPC endpoint that serves the captured payloads.
// ---------------------------------------------------------------------------

type replayServer struct {
	*httptest.Server

	blocks  map[uint64]replaySlotEntry
	nextTip uint64
}

// newReplayServer builds the endpoint. getBlock replays the stored payload
// (or the stored error envelope, or null); getSlot answers just past the
// captured range so every null outcome is strictly below the confirmed tip
// and the real fetcher classifies it as a permanent skip rather than
// retrying forever.
func newReplayServer(t *testing.T, fx *replayFixture) *replayServer {
	t.Helper()

	srv := &replayServer{
		blocks:  make(map[uint64]replaySlotEntry, len(fx.Blocks)),
		nextTip: fx.EndSlot + 1,
	}
	for _, entry := range fx.Blocks {
		srv.blocks[entry.Slot] = entry
	}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var envelope struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params []json.RawMessage
		}
		_ = json.Unmarshal(raw, &envelope)
		id := envelope.ID

		switch envelope.Method {
		case "getBlock":
			if len(envelope.Params) == 0 {
				writeReplayError(w, id, "getBlock without slot parameter")
				return
			}
			var slot uint64
			if err := json.Unmarshal(envelope.Params[0], &slot); err != nil {
				writeReplayError(w, id, "unparseable getBlock slot parameter")
				return
			}
			if entry, ok := srv.blocks[slot]; ok {
				if entry.Error != nil {
					writeReplayErrorObject(w, id, entry.Error)
					return
				}
				// Payload bytes go out verbatim; a null-result entry
				// (Result nil) serves the null the node originally sent.
				writeReplayResult(w, id, entry.Result)
				return
			}
			writeReplayResult(w, id, nil)
		case "getSlot":
			tip, err := json.Marshal(srv.nextTip)
			if err != nil {
				writeReplayError(w, id, "replay tip marshal error")
				return
			}
			writeReplayResult(w, id, tip)
		default:
			writeReplayErrorObject(w, id, &replayRPCErrorInfo{Code: replayCodeMethodNotFound, Message: "method not found"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeReplayResult wraps one raw result payload (or null) with the
// incoming request id — echoing the id matters because the SDK correlates
// concurrent responses by it.
func writeReplayResult(w http.ResponseWriter, id json.RawMessage, payload json.RawMessage) {
	if payload == nil {
		payload = json.RawMessage("null")
	}
	writeReplayEnvelope(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  payload,
	})
}

func writeReplayErrorObject(w http.ResponseWriter, id json.RawMessage, errInfo *replayRPCErrorInfo) {
	writeReplayEnvelope(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   errInfo,
	})
}

func writeReplayError(w http.ResponseWriter, id json.RawMessage, message string) {
	writeReplayErrorObject(w, id, &replayRPCErrorInfo{Code: replayCodeInvalidParams, Message: message})
}

func writeReplayEnvelope(w http.ResponseWriter, envelope map[string]any) {
	raw, err := json.Marshal(envelope)
	if err != nil {
		http.Error(w, "replay envelope marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// ---------------------------------------------------------------------------
// Benchmark runner.
// ---------------------------------------------------------------------------

// replayMaxBlocks caps how many processable blocks the benchmark indexes.
// 0 (default) indexes the whole captured range. Skipped slots do not count
// toward the cap. Set via SOLANA_REPLAY_MAX_BLOCKS.
func replayMaxBlocks(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("SOLANA_REPLAY_MAX_BLOCKS")
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		t.Fatalf("invalid SOLANA_REPLAY_MAX_BLOCKS=%q: use a non-negative integer", raw)
	}
	return n
}

// runReplayBenchmark drives every captured slot through the production
// pipeline sequentially and returns the per-block wall times plus the
// skipped-slot count. Skipped slots stay out of the timing sample: the
// production processor's lag budget is consumed by real blocks.
// SOLANA_REPLAY_MAX_BLOCKS caps the sample to the first N processable
// blocks; when set, remaining slots are left unindexed.
func runReplayBenchmark(
	t *testing.T,
	fx *replayFixture,
) (durations []time.Duration, skipped int) {
	t.Helper()

	srv := newReplayServer(t, fx)
	store := newBenchStore(t, 100)

	client, err := solana.NewClient(context.Background(), solana.ClientOptions{
		RPCURL:                         srv.URL,
		Commitment:                     config.DefaultSolanaCommitment,
		MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		Rewards:                        true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	fetcher := solana.NewFetcher(client)

	// Warm-up: one small synthetic block exercises the full Convert →
	// Store → sign path so cold-start costs (schema, store, identity) do
	// not land on the first real block, which would skew the average.
	warm, err := store.conv.Convert(store.ctx, fakeBlockWithTxs(replayWarmupSlot, fakeTransaction(replayWarmupSlot, 0, "replay-warmup")))
	require.NoError(t, err)
	_, err = store.handler.Store(store.ctx, warm)
	require.NoError(t, err)

	maxBlocks := replayMaxBlocks(t)

	durations = make([]time.Duration, 0, fx.EndSlot-fx.StartSlot+1)
	for slot := fx.StartSlot; slot <= fx.EndSlot; slot++ {
		if maxBlocks > 0 && len(durations) == maxBlocks {
			t.Logf("processing capped: %d processable blocks indexed, remaining slots left unindexed", maxBlocks)
			break
		}
		blockStart := time.Now()

		fetched, err := fetcher.FetchBlock(store.ctx, int64(slot))
		if err != nil {
			if stderrors.Is(err, chains.ErrHeightSkipped) {
				t.Logf("block %d: skipped (%v)", slot, err)
				skipped++
				continue
			}
			t.Fatalf("fetch slot %d: %v", slot, err)
		}
		block, ok := fetched.(*solana.Block)
		require.True(t, ok, "slot %d: fetcher returned %T, want *Block", slot, fetched)

		result, err := store.conv.Convert(store.ctx, block)
		require.NoError(t, err, "convert slot %d", slot)

		creation, err := store.handler.Store(store.ctx, result)
		require.NoError(t, err, "store slot %d", slot)
		require.NotEmpty(t, creation.BlockSignatureID, "slot %d must sign like production stores", slot)

		durations = append(durations, time.Since(blockStart))
		t.Logf("block %d: processed in %s", slot, durations[len(durations)-1])
	}
	return durations, skipped
}

// percentileIndex returns the nearest-rank percentile of a sorted slice.
func percentileIndex(n int, pct float64) int {
	idx := max(int(pct*float64(n)+0.9999), 1)
	if idx > n {
		idx = n
	}
	return idx - 1
}

// durMs renders one duration rounded to whole milliseconds for the report.
func durMs(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}

// ---------------------------------------------------------------------------
// The benchmark test.
// ---------------------------------------------------------------------------

// TestSolanaReplayProcessingBenchmark replays the captured mainnet slot
// range end-to-end and reports the average per-block processing time
// against the 100ms target. The verdict is informational: an exceeded
// target does not fail the test, so slow hosts still produce full reports;
// only fixture corruption or pipeline failures fail it.
func TestSolanaReplayProcessingBenchmark(t *testing.T) {
	fx := loadReplayFixture(t)

	durations, skipped := runReplayBenchmark(t, fx)

	require.NotEmpty(t, durations,
		"no slot was processed (all %d skipped) — the capture holds no processable blocks", skipped)

	sorted := append([]time.Duration(nil), durations...)
	slices.Sort(sorted)

	var total int64
	for _, d := range durations {
		total += int64(d)
	}
	avg := time.Duration(total / int64(len(durations)))
	minDur, maxDur := sorted[0], sorted[len(sorted)-1]
	p50 := sorted[percentileIndex(len(sorted), 0.50)]
	p95 := sorted[percentileIndex(len(sorted), 0.95)]

	maxBlocks := replayMaxBlocks(t)

	t.Logf("=== Solana replay benchmark: slots %d-%d ===", fx.StartSlot, fx.EndSlot)
	t.Logf("backend: %s", benchBackendName(benchDefraInMemory(t)))
	t.Logf("blocks processed: %d   skipped: %d", len(durations), skipped)
	if maxBlocks > 0 {
		t.Logf("sample: first %d processable blocks (SOLANA_REPLAY_MAX_BLOCKS)", maxBlocks)
	}
	t.Logf("avg: %s   min: %s   p50: %s   p95: %s   max: %s",
		durMs(avg), durMs(minDur), durMs(p50), durMs(p95), durMs(maxDur))
	t.Logf("total: %s for %d blocks", durMs(time.Duration(total)), len(durations))
	switch {
	case avg <= replayTargetAvg:
		headroom := 100 * float64(replayTargetAvg-avg) / float64(replayTargetAvg)
		t.Logf("TARGET %s avg: PASS (%.1f%% headroom)", durMs(replayTargetAvg), headroom)
	default:
		over := 100 * float64(avg) / float64(replayTargetAvg)
		t.Logf("TARGET %s avg: EXCEEDED (avg %s = %.0f%% of budget)",
			durMs(replayTargetAvg), durMs(avg), over)
	}
}
