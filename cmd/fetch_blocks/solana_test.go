package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// solanaCaptureServer is a minimal Solana JSON-RPC node for the capture unit
// tests: one answer per method, plus request capture for the parameter
// assertions. The lag and code knobs make the getBlock answers cover every
// missing-slot spelling the backend classifies: null result (lag or skip),
// -32004 (ambiguous not-available), -32007 (affirmative skip), -32001
// (pruned below the ledger floor), -32015 (version ceiling rejected). The
// per-slot override lets walk-level tests give individual slots their own
// spellings while the global knobs stay untouched.
type solanaCaptureServer struct {
	tip string // raw getSlot result
	// getBlock answer knobs: the per-slot override wins over the code, the
	// code wins over the lag, the lag wins over the block body.
	block     string
	blockLag  int // first N getBlock answers are the null result
	blockCode int // when non-zero, getBlock answers with this error code

	slotAnswers map[uint64]slotAnswer

	slotCalls       int
	blockCalls      int
	slotParams      json.RawMessage // raw getSlot params of the last call
	lastSlot        uint64          // decoded slot of the last getBlock call
	lastBlockConfig map[string]any  // decoded getBlock config object
}

// slotAnswer is one slot's per-slot getBlock override: an error code when
// non-zero, otherwise the raw block body to serve verbatim.
type slotAnswer struct {
	code  int
	block string
}

func (s *solanaCaptureServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

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
		case "getSlot":
			s.slotCalls++
			s.slotParams = req.Params
			respond(json.RawMessage(s.tip))
		case "getBlock":
			s.blockCalls++
			var params []json.RawMessage
			_ = json.Unmarshal(req.Params, &params)
			_ = json.Unmarshal(params[0], &s.lastSlot)
			var slot uint64
			_ = json.Unmarshal(params[0], &slot)
			s.lastBlockConfig = map[string]any{}
			_ = json.Unmarshal(params[1], &s.lastBlockConfig)
			if sa, ok := s.slotAnswers[slot]; ok {
				if sa.code != 0 {
					respondErr(sa.code, "mock rpc error")
					return
				}
				respond(json.RawMessage(sa.block))
				return
			}
			if s.blockCode != 0 {
				respondErr(s.blockCode, "mock rpc error")
				return
			}
			if s.blockLag > 0 {
				s.blockLag--
				respond(json.RawMessage("null"))
				return
			}
			respond(json.RawMessage(s.block))
		default:
			respondErr(-32601, "unexpected method "+req.Method)
		}
	})
}

// newSolanaTestBackend builds a solanaBackend pointed at the test server
// with the default capture commitment.
func newSolanaTestBackend(server *httptest.Server) *solanaBackend {
	return &solanaBackend{
		env:        &rpcEnv{client: server.Client(), url: server.URL},
		commitment: solanaDefaultCommitment,
	}
}

func TestParseFlagsSolanaAdapter(t *testing.T) {
	t.Parallel()
	opts, err := parseFlags([]string{"--chain", "solana", "--network", "mainnet", "--adapter", "solana"})
	require.NoError(t, err)
	assert.Equal(t, adapterSolana, opts.adapter)
}

func TestSolanaFetchTip(t *testing.T) {
	t.Parallel()

	t.Run("decodes the decimal slot and sends the commitment", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `5000`}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		tip, err := newSolanaTestBackend(server).fetchTip(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(5000), tip)
		assert.Contains(t, string(s.slotParams), `"commitment":"confirmed"`)
	})
}

func TestSolanaCaptureBlock(t *testing.T) {
	t.Parallel()

	t.Run("captures the block verbatim with a decimal slot and embedded receipts", func(t *testing.T) {
		t.Parallel()
		block := `{"blockhash":"BH","previousBlockhash":"PB","parentSlot":41,"blockTime":1700000000,"transactions":[],"rewards":[]}`
		s := &solanaCaptureServer{tip: `42`, block: block}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		fb, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.NoError(t, err)
		require.NotNil(t, fb)
		assert.Equal(t, "42", fb.Number, "solana numbers are decimal slots")
		assert.Equal(t, receiptModeEmbedded, fb.ReceiptMode)
		assert.JSONEq(t, block, string(fb.Block))
		assert.Equal(t, "null", string(fb.Receipts), "solana embeds the receipt metadata in the block body")
		assert.Equal(t, uint64(42), s.lastSlot)

		// The captured call must be the production call shape, so the
		// fixture bytes decode identically through the SDK in replay.
		require.NotNil(t, s.lastBlockConfig)
		assert.Equal(t, solanaCaptureEncoding, s.lastBlockConfig["encoding"])
		assert.Equal(t, solanaCaptureTxDetails, s.lastBlockConfig["transactionDetails"])
		assert.Equal(t, solanaDefaultCommitment, s.lastBlockConfig["commitment"])
		assert.Equal(t, true, s.lastBlockConfig["rewards"])
		assert.InDelta(t, float64(solanaCaptureMaxTxVersion), s.lastBlockConfig["maxSupportedTransactionVersion"], 0)
	})

	t.Run("present but empty block object is a hard failure", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `42`, block: `{}`}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		_, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing blockhash")
		assert.Equal(t, 1, s.blockCalls, "a degenerate answer is not an indexing lag and must not be retried")
	})
}

func TestSolanaCaptureSkippedSlot(t *testing.T) {
	t.Parallel()

	t.Run("affirmative skip code ends the slot with the skip sentinel in one call", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `42`, block: `{}`, blockCode: solanaCodeSlotSkipped}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		fb, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.ErrorIs(t, err, errBlockSkipped, "the fixture holds only produced slots, so a skip records no entry")
		assert.Nil(t, fb)
		assert.Equal(t, 1, s.blockCalls, "an affirmative skip answer is permanent and must not be retried")
	})

	t.Run("long-term storage skip code ends the slot with the skip sentinel too", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `42`, block: `{}`, blockCode: solanaCodeLTStorageSlotSkipped}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		fb, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.ErrorIs(t, err, errBlockSkipped)
		assert.Nil(t, fb)
		assert.Equal(t, 1, s.blockCalls)
	})
}

func TestSolanaCaptureLagRetry(t *testing.T) {
	t.Parallel()

	t.Run("null result resolves into the block on retry", func(t *testing.T) {
		t.Parallel()
		block := `{"blockhash":"BH","previousBlockhash":"PB","parentSlot":41,"blockTime":1700000000,"transactions":[],"rewards":[]}`
		s := &solanaCaptureServer{tip: `42`, block: block, blockLag: 2}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		fb, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.NoError(t, err)
		require.NotNil(t, fb)
		assert.Equal(t, "42", fb.Number)
		assert.JSONEq(t, block, string(fb.Block))
		assert.Equal(t, 3, s.blockCalls, "two not-yet-visible answers plus the indexed one")
	})

	t.Run("null result past the lag window ends the slot with no entry", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `42`, block: `{}`, blockLag: 99}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		fb, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.ErrorIs(t, err, errBlockSkipped, "an ambiguous spelling that never clears is a skip, not an entry")
		assert.Nil(t, fb)
		assert.Equal(t, solanaCaptureAttempts, s.blockCalls, "each lag retry re-asks the endpoint")
	})

	t.Run("not-available code is an ambiguous spelling and retries", func(t *testing.T) {
		t.Parallel()
		block := `{"blockhash":"BH","previousBlockhash":"PB","parentSlot":41,"blockTime":1700000000,"transactions":[],"rewards":[]}`
		s := &solanaCaptureServer{tip: `42`, block: block, blockCode: solanaCodeBlockNotAvailable}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		// The not-available spelling never clears in this test, so the
		// bounded retries exhaust and the slot ends with the skip sentinel.
		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		fb, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.ErrorIs(t, err, errBlockSkipped)
		assert.Nil(t, fb)
		assert.Equal(t, solanaCaptureAttempts, s.blockCalls)
	})
}

func TestSolanaCaptureHardErrors(t *testing.T) {
	t.Parallel()

	t.Run("pruned below the ledger floor fails naming it", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `42`, block: `{}`, blockCode: solanaCodeBlockCleanedUp}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		_, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ledger floor")
		assert.Equal(t, 1, s.blockCalls, "a pruned slot is permanently gone from this endpoint")
	})

	t.Run("version ceiling rejection fails naming it", func(t *testing.T) {
		t.Parallel()
		s := &solanaCaptureServer{tip: `42`, block: `{}`, blockCode: solanaCodeUnsupportedTxVersion}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		_, err := newSolanaTestBackend(server).captureBlock(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "version ceiling")
		assert.Equal(t, 1, s.blockCalls)
	})
}

func TestSolanaCaptureRangeRecordsOnlyProducedSlots(t *testing.T) {
	// Not parallel: t.Chdir redirects the whole process's working directory,
	// so the default output directory resolves inside this test's temp dir.
	t.Chdir(t.TempDir())

	// The walk covers slots 10..12 with slot 11 affirmatively skipped: the
	// capture finishes clean, the fixture holds exactly the two produced
	// slots, and nothing in the file marks the empty one.
	block10 := `{"blockhash":"BH10","previousBlockhash":"PB","parentSlot":9,"blockTime":1700000000,"transactions":[],"rewards":[]}`
	block12 := `{"blockhash":"BH12","previousBlockhash":"PB","parentSlot":11,"blockTime":1700000000,"transactions":[],"rewards":[]}`
	s := &solanaCaptureServer{
		tip: `12`,
		slotAnswers: map[uint64]slotAnswer{
			10: {block: block10},
			11: {code: solanaCodeSlotSkipped},
			12: {block: block12},
		},
	}
	server := httptest.NewServer(s.handler())
	defer server.Close()

	opts := &captureOptions{chain: "solana", network: "mainnet", delay: time.Millisecond}
	require.NoError(t, captureRange(context.Background(), newSolanaTestBackend(server), opts, 10, 12))

	data, err := os.ReadFile(filepath.Join(defaultOutDir, "solana_blocks_10_12"+fixtureExt))
	require.NoError(t, err)
	var saved replayFixture
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.False(t, saved.Meta.CapturePartial)
	assert.Equal(t, 2, saved.Meta.BlockCount)
	require.Len(t, saved.Blocks, 2)
	assert.Equal(t, "10", saved.Blocks[0].Number)
	assert.Equal(t, receiptModeEmbedded, saved.Blocks[0].ReceiptMode)
	assert.JSONEq(t, block10, string(saved.Blocks[0].Block))
	assert.Equal(t, "12", saved.Blocks[1].Number)
	assert.JSONEq(t, block12, string(saved.Blocks[1].Block))
}

func TestNewSolanaBackend(t *testing.T) {
	// Not parallel: the subtests write process-wide env vars.

	t.Run("missing endpoint env fails naming it", func(t *testing.T) {
		t.Setenv("SOLANA_RPC_URL", "")
		_, err := newSolanaBackend(&captureOptions{chain: "solana", network: "mainnet"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SOLANA_RPC_URL is required")
	})

	t.Run("key without a header type keeps the x-api-key fallback", func(t *testing.T) {
		t.Setenv("SOLANA_RPC_URL", "https://gateway.example")
		t.Setenv("SOLANA_API_KEY", "key")
		t.Setenv("SOLANA_API_KEY_TYPE", "")
		backend, err := newSolanaBackend(&captureOptions{chain: "solana", network: "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, "key", backend.env.apiKey)
		assert.Equal(t, "x-api-key", backend.env.apiKeyType)
	})

	t.Run("default commitment is confirmed", func(t *testing.T) {
		t.Setenv("SOLANA_RPC_URL", "https://gateway.example")
		t.Setenv("SOLANA_COMMITMENT", "")
		backend, err := newSolanaBackend(&captureOptions{chain: "solana", network: "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, solanaDefaultCommitment, backend.commitment)
	})

	t.Run("stray whitespace on env values is normalized", func(t *testing.T) {
		// An .env line can carry a trailing space (SOLANA_API_KEY_TYPE=x-api-key ):
		// sent verbatim, the transport rejects it as an invalid header field
		// name only after every retry attempt burned. The constructor
		// normalizes like the production client's authHeader instead.
		t.Setenv("SOLANA_RPC_URL", " https://gateway.example ")
		t.Setenv("SOLANA_API_KEY", " key ")
		t.Setenv("SOLANA_API_KEY_TYPE", "X-Api-Key ")
		backend, err := newSolanaBackend(&captureOptions{chain: "solana", network: "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, "https://gateway.example", backend.env.url)
		assert.Equal(t, "key", backend.env.apiKey)
		assert.Equal(t, "x-api-key", backend.env.apiKeyType, "trimmed and lower-cased, so the transport accepts the header")
	})

	t.Run("SOLANA_COMMITMENT overrides the default", func(t *testing.T) {
		t.Setenv("SOLANA_RPC_URL", "https://gateway.example")
		t.Setenv("SOLANA_COMMITMENT", "finalized")
		backend, err := newSolanaBackend(&captureOptions{chain: "solana", network: "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, "finalized", backend.commitment)
	})
}

func TestRunSolanaRequiresRPCEndpoint(t *testing.T) {
	t.Setenv("SOLANA_RPC_URL", "")
	err := run([]string{"--chain", "solana", "--network", "mainnet", "--adapter", "solana"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SOLANA_RPC_URL")
}
