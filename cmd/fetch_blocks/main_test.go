package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastBackoff makes retry tests instant: the RPC layer's real backoff grows
// to seconds, which no unit test needs to wait out.
func fastBackoff() func(int) time.Duration {
	return func(int) time.Duration { return time.Millisecond }
}

func TestResolveRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		from, to    uint64
		tip         uint64
		wantFrom    uint64
		wantTo      uint64
		shouldError bool
		errContains string
	}{
		{name: "defaults: 100 blocks ending at tip", tip: 5000, wantFrom: 4901, wantTo: 5000},
		{name: "defaults clamp at genesis", tip: 50, wantFrom: 0, wantTo: 50},
		{name: "to only: window ends at to", from: 0, to: 1000, tip: 5000, wantFrom: 901, wantTo: 1000},
		{name: "from only: to falls back to tip", from: 4990, to: 0, tip: 5000, wantFrom: 4990, wantTo: 5000},
		{name: "explicit range", from: 10, to: 20, tip: 5000, wantFrom: 10, wantTo: 20},
		{name: "from > to rejected", from: 20, to: 10, tip: 5000, shouldError: true, errContains: "greater than"},
		{name: "to beyond tip rejected", from: 10, to: 99999, tip: 5000, shouldError: true, errContains: "beyond the current tip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			from, to, err := resolveRange(tt.from, tt.to, tt.tip)
			if tt.shouldError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFrom, from)
			assert.Equal(t, tt.wantTo, to)
		})
	}
}

func TestRetryBackoffBounds(t *testing.T) {
	t.Parallel()
	previous := time.Duration(0)
	for attempt := range 10 {
		d := retryBackoff(attempt)
		assert.GreaterOrEqual(t, d, retryBaseDelay, "attempt %d must never be below the base delay", attempt)
		assert.LessOrEqual(t, d, retryMaxDelay, "attempt %d must never exceed the cap", attempt)
		if previous > 0 && d > retryBaseDelay && attempt < 5 {
			assert.Equal(t, previous*2, d, "attempt %d should double the previous wait", attempt)
		}
		previous = d
	}
	assert.Equal(t, retryMaxDelay, retryBackoff(100), "large attempts saturate at the cap")
}

func TestParseHexUint(t *testing.T) {
	t.Parallel()
	n, err := parseHexUint("0x1f")
	require.NoError(t, err)
	assert.Equal(t, uint64(31), n)

	n, err = parseHexUint("0x0")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), n)

	_, err = parseHexUint("0x")
	require.Error(t, err)

	_, err = parseHexUint("not-hex")
	require.Error(t, err)
}

func TestHexNum(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "0x0", hexNum(0))
	assert.Equal(t, "0x64", hexNum(100))
}

func TestParseFlags(t *testing.T) {
	t.Parallel()
	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		opts, err := parseFlags([]string{})
		require.NoError(t, err)
		assert.Equal(t, defaultChain, opts.chain)
		assert.Equal(t, defaultNetwork, opts.network)
		assert.Equal(t, uint64(0), opts.from)
		assert.Equal(t, uint64(0), opts.to)
		assert.Equal(t, defaultDelay, opts.delay)
	})

	t.Run("explicit chain and network", func(t *testing.T) {
		t.Parallel()
		opts, err := parseFlags([]string{"--chain", "arbitrum", "--network", "testnet"})
		require.NoError(t, err)
		assert.Equal(t, "arbitrum", opts.chain)
		assert.Equal(t, "testnet", opts.network)
	})

	t.Run("empty chain rejected", func(t *testing.T) {
		t.Parallel()
		_, err := parseFlags([]string{"--chain", " "})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--chain must not be empty")
	})

	t.Run("empty network rejected", func(t *testing.T) {
		t.Parallel()
		_, err := parseFlags([]string{"--network", ""})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--network must not be empty")
	})
}

func TestFixturePrefix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "bsc_blocks_", fixturePrefix("BSC"))
	assert.Equal(t, "arbitrum_blocks_", fixturePrefix("Arbitrum"))
}

func TestSanitizeLogLabel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "bsc", sanitizeLogLabel("bsc"))
	assert.Equal(t, "evillog", sanitizeLogLabel("evil\nlog"))
	assert.Equal(t, "evillog", sanitizeLogLabel("evil\rlog"))
	// The two-character sequence "\r" is not a control character; it must
	// pass through untouched.
	assert.Equal(t, "evil\\rlog", sanitizeLogLabel("evil\\rlog"))
	assert.Equal(t, "", sanitizeLogLabel("\n\r"))
}

func TestFixtureRoundTrip(t *testing.T) {
	t.Parallel()
	// The fixture must survive a marshal/unmarshal cycle with the raw node
	// bytes intact: the replay mock serves exactly what the node served.
	original := replayFixture{
		Meta: fixtureMeta{
			Chain:      defaultChain,
			Network:    defaultNetwork,
			From:       10,
			To:         11,
			BlockCount: 2,
			CapturedAt: "2026-01-01T00:00:00Z",
		},
		Blocks: []fixtureBlock{
			{
				Number:      "0xa",
				Block:       json.RawMessage(`{"number":"0xa","hash":"0xabc"}`),
				Receipts:    json.RawMessage(`[{"transactionHash":"0x1","status":"0x1"}]`),
				ReceiptMode: receiptModeBatch,
			},
			{
				Number:      "0xb",
				Block:       json.RawMessage(`{"number":"0xb","transactions":[]}`),
				Receipts:    json.RawMessage(`[{"transactionHash":"0x2"}]`),
				ReceiptMode: receiptModePerTx,
			},
		},
	}

	data, err := json.MarshalIndent(original, "", "  ")
	require.NoError(t, err)

	var decoded replayFixture
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, original.Meta, decoded.Meta)
	require.Len(t, decoded.Blocks, 2)
	assert.JSONEq(t, string(original.Blocks[0].Block), string(decoded.Blocks[0].Block))
	assert.JSONEq(t, string(original.Blocks[0].Receipts), string(decoded.Blocks[0].Receipts))
	assert.Equal(t, original.Blocks[0].ReceiptMode, decoded.Blocks[0].ReceiptMode)
}

func TestSaveFixtureAtomic(t *testing.T) {
	t.Parallel()
	out := t.TempDir() + "/nested/" + fixturePrefix(defaultChain) + "1_2" + fixtureExt
	fx := &replayFixture{
		Meta:   fixtureMeta{Chain: defaultChain, From: 1, To: 2, BlockCount: 1},
		Blocks: []fixtureBlock{{Number: "0x1", Block: json.RawMessage(`{}`)}},
	}

	require.NoError(t, saveFixture(fx, out))

	// The temp file must be gone (renamed, not copied).
	data, err := os.ReadFile(out)
	require.NoError(t, err)

	var saved replayFixture
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, fx.Meta, saved.Meta)
	assert.NoFileExists(t, out+".tmp")
}

func TestRPCCallSuccess(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"number":"0x1"}}`))
	}))
	defer server.Close()

	result, err := rpcCall(context.Background(), testEnv(server), "eth_getBlockByNumber", []any{"0x1", true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"number":"0x1"}`, string(result))
}

// testEnv builds an rpcEnv pointed at a test server.
func testEnv(server *httptest.Server) *rpcEnv {
	return &rpcEnv{client: server.Client(), url: server.URL}
}

func TestRPCCallEnvelopeErrorNotRetried(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	_, err := rpcCall(context.Background(), testEnv(server), "eth_getBlockReceipts", []any{"0x1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "method not found")
	assert.Equal(t, 1, requests, "envelope errors are the node's answer and must not be retried")
}

func TestRPCCallRetries429ThenSucceeds(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`))
	}))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	result, err := rpcCall(context.Background(), testEnv(server), "eth_blockNumber", []any{})
	require.NoError(t, err)
	assert.JSONEq(t, `"0x10"`, string(result))
	assert.Equal(t, 2, requests)
}

func TestRPCCallGivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	_, err := rpcCall(context.Background(), testEnv(server), "eth_blockNumber", []any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "giving up after")
	assert.Equal(t, maxRetries, requests)
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.Duration(0), parseRetryAfter(""))
	assert.Equal(t, time.Duration(0), parseRetryAfter("not-a-number"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("-3"))
	assert.Equal(t, 2*time.Second, parseRetryAfter("2"))
	assert.Equal(t, retryMaxDelay, parseRetryAfter("9999"), "absurd hints clamp to the retry cap")
}

func TestIsJSONNull(t *testing.T) {
	t.Parallel()
	assert.True(t, isJSONNull(json.RawMessage("null")))
	assert.True(t, isJSONNull(json.RawMessage("  null  ")))
	assert.False(t, isJSONNull(json.RawMessage(`{"a":1}`)))
	assert.False(t, isJSONNull(json.RawMessage("")))
}

func TestRedactEndpoint(t *testing.T) {
	t.Parallel()
	// Key-in-path style URLs (Infura/Alchemy) must lose everything after the
	// host, both in the capture log and in rpcCall error strings.
	assert.Equal(t, "https://mainnet.infura.io", redactEndpoint("https://user:pass@mainnet.infura.io/v3/s3cr3t"))
	assert.Equal(t, "https://eth-mainnet.g.alchemy.com", redactEndpoint("https://eth-mainnet.g.alchemy.com/v2/KEY"))
	assert.Equal(t, "http://127.0.0.1:8545", redactEndpoint("http://127.0.0.1:8545"))
	assert.Equal(t, "(endpoint hidden)", redactEndpoint("http://host\n.evil"))
	assert.Equal(t, "(endpoint hidden)", redactEndpoint("not a url"))
	assert.Equal(t, "(endpoint hidden)", redactEndpoint(""))
}
