package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tronCaptureServer is a minimal wallet API for the capture unit tests: one
// answer per endpoint, plus request capture for the payload assertions. The
// lag knobs make the first N answers the "node has not indexed the height
// yet" shapes, so the capture's lag retry can be exercised.
type tronCaptureServer struct {
	nowBlock     string
	block        string
	receipts     string
	lastNum      int64 // decoded num of the last getblockbynum call
	blockLag     int   // first N block answers are the empty object
	blockCalls   int
	receiptsLag  int // first N receipt answers are the empty object
	receiptCalls int
}

func (s *tronCaptureServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/wallet/getnowblock", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(s.nowBlock))
	})
	mux.HandleFunc("/wallet/getblockbynum", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Num int64 `json:"num"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.lastNum = req.Num
		s.blockCalls++
		if s.blockLag > 0 {
			s.blockLag--
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(s.block))
	})
	mux.HandleFunc("/wallet/gettransactioninfobyblocknum", func(w http.ResponseWriter, _ *http.Request) {
		s.receiptCalls++
		if s.receiptsLag > 0 {
			s.receiptsLag--
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(s.receipts))
	})
	return mux
}

// newTronTestBackend builds a tronBackend pointed at the test server.
func newTronTestBackend(server *httptest.Server) *tronBackend {
	return &tronBackend{env: &tronEnv{client: server.Client(), url: server.URL}}
}

func TestParseFlagsAdapter(t *testing.T) {
	t.Parallel()

	t.Run("defaults to evm", func(t *testing.T) {
		t.Parallel()
		opts, err := parseFlags([]string{"--chain", "ethereum", "--network", "Mainnet"})
		require.NoError(t, err)
		assert.Equal(t, adapterEVM, opts.adapter)
	})

	t.Run("tron accepted", func(t *testing.T) {
		t.Parallel()
		opts, err := parseFlags([]string{"--chain", "tron", "--network", "mainnet", "--adapter", "tron"})
		require.NoError(t, err)
		assert.Equal(t, adapterTron, opts.adapter)
	})

	t.Run("unknown adapter rejected", func(t *testing.T) {
		t.Parallel()
		_, err := parseFlags([]string{"--chain", "tron", "--network", "mainnet", "--adapter", "solana"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "supported adapters: evm, tron")
	})
}

func TestTronFetchTip(t *testing.T) {
	t.Parallel()

	t.Run("decodes the head block's decimal height", func(t *testing.T) {
		t.Parallel()
		s := tronCaptureServer{nowBlock: `{"blockID":"tip","block_header":{"raw_data":{"number":52700235}}}`}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		tip, err := newTronTestBackend(server).fetchTip(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(52700235), tip)
	})

	t.Run("empty object reads as an unavailable head block", func(t *testing.T) {
		t.Parallel()
		s := tronCaptureServer{nowBlock: `{}`}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		_, err := newTronTestBackend(server).fetchTip(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "head block unavailable")
	})
}

func TestTronCaptureBlock(t *testing.T) {
	t.Parallel()

	t.Run("captures the block and receipts verbatim with a decimal number", func(t *testing.T) {
		t.Parallel()
		s := &tronCaptureServer{
			block:    `{"blockID":"abc","block_header":{"raw_data":{"number":42,"timestamp":1000}},"transactions":[{"ret":[{"contractRet":"SUCCESS"}]}]}`,
			receipts: `[{"id":"tx1","blockNumber":42,"blockTimeStamp":1000}]`,
		}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		fb, err := newTronTestBackend(server).captureBlock(context.Background(), 42)
		require.NoError(t, err)
		assert.Equal(t, "42", fb.Number, "tron numbers are decimal heights")
		assert.Equal(t, receiptModeBlock, fb.ReceiptMode)
		assert.JSONEq(t, s.block, string(fb.Block))
		assert.JSONEq(t, s.receipts, string(fb.Receipts))
		assert.Equal(t, int64(42), s.lastNum, "the wallet API receives the decimal height in the body")
	})

	t.Run("empty receipts normalize into an array for an empty block", func(t *testing.T) {
		t.Parallel()
		s := &tronCaptureServer{
			block:    `{"blockID":"abc","block_header":{"raw_data":{"number":7}},"transactions":[]}`,
			receipts: `{}`,
		}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		fb, err := newTronTestBackend(server).captureBlock(context.Background(), 7)
		require.NoError(t, err)
		assert.Equal(t, "7", fb.Number)
		assert.JSONEq(t, `[]`, string(fb.Receipts))
	})

	t.Run("receipt count mismatch retries then fails", func(t *testing.T) {
		t.Parallel()
		s := &tronCaptureServer{
			block:    `{"blockID":"abc","block_header":{"raw_data":{"number":42}},"transactions":[{},{}]}`,
			receipts: `[{"id":"tx1"}]`,
		}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		_, err := newTronTestBackend(server).captureBlock(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "node returned 1 receipts for 2 transactions")
		assert.Contains(t, err.Error(), "still not indexed after 4 attempts")
		assert.Equal(t, tronCaptureAttempts, s.receiptCalls, "each lag retry re-asks the receipts endpoint")
	})

	t.Run("receipt lag resolves on retry", func(t *testing.T) {
		t.Parallel()
		s := &tronCaptureServer{
			block:       `{"blockID":"abc","block_header":{"raw_data":{"number":42}},"transactions":[{},{}]}`,
			receipts:    `[{"id":"tx1"},{"id":"tx2"}]`,
			receiptsLag: 2,
		}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		fb, err := newTronTestBackend(server).captureBlock(context.Background(), 42)
		require.NoError(t, err)
		assert.JSONEq(t, s.receipts, string(fb.Receipts))
		assert.Equal(t, 3, s.receiptCalls, "two not-yet-indexed answers plus the indexed one")
	})

	t.Run("lagging block node catches up on retry", func(t *testing.T) {
		t.Parallel()
		s := &tronCaptureServer{
			block:    `{"blockID":"abc","block_header":{"raw_data":{"number":42}},"transactions":[]}`,
			receipts: `[]`,
			blockLag: 1,
		}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		fb, err := newTronTestBackend(server).captureBlock(context.Background(), 42)
		require.NoError(t, err)
		assert.Equal(t, "42", fb.Number)
		assert.JSONEq(t, `[]`, string(fb.Receipts))
		assert.Equal(t, 2, s.blockCalls, "one not-yet-indexed answer plus the indexed one")
	})

	t.Run("empty object reads as an unavailable block", func(t *testing.T) {
		t.Parallel()
		s := &tronCaptureServer{block: `{}`, receipts: `[]`}
		server := httptest.NewServer(s.handler())
		defer server.Close()

		old := retryBackoffFn
		retryBackoffFn = fastBackoff()
		t.Cleanup(func() { retryBackoffFn = old })

		_, err := newTronTestBackend(server).captureBlock(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not available on the node")
		assert.Contains(t, err.Error(), "still not indexed after 4 attempts")
	})
}

func TestTronWalletCallBusinessErrorNotRetried(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"Error":"class java.lang.IllegalArgumentException"}`))
	}))
	defer server.Close()

	backend := newTronTestBackend(server)
	_, err := backend.walletCall(context.Background(), tronEndpointGetBlockByNum, map[string]int64{"num": 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IllegalArgumentException")
	assert.Equal(t, 1, requests, "a business failure is the node's answer and must not be retried")
}

func TestTronWalletCallRetries429ThenSucceeds(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"blockID":"b","block_header":{"raw_data":{"number":1}}}`))
	}))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	backend := newTronTestBackend(server)
	raw, err := backend.walletCall(context.Background(), tronEndpointGetNowBlock, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"blockID":"b","block_header":{"raw_data":{"number":1}}}`, string(raw))
	assert.Equal(t, 2, requests)
}

func TestNewTronBackendRequiresRPCURL(t *testing.T) {
	// Not parallel: the subtests write process-wide env vars.

	t.Run("missing endpoint env fails naming it", func(t *testing.T) {
		t.Setenv("TRON_RPC_URL", "")
		_, err := newTronBackend(&captureOptions{chain: "tron", network: "mainnet"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TRON_RPC_URL is required")
	})

	t.Run("default header keeps the TronGrid standard", func(t *testing.T) {
		t.Setenv("TRON_RPC_URL", "https://api.trongrid.io")
		t.Setenv("TRON_API_KEY", "key")
		t.Setenv("TRON_API_KEY_TYPE", "")
		backend, err := newTronBackend(&captureOptions{chain: "tron", network: "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, "key", backend.env.apiKey)
		assert.Equal(t, "Tron-Pro-Api-Key", backend.env.apiKeyHeader)
	})

	t.Run("configured key type is lowercased", func(t *testing.T) {
		t.Setenv("TRON_RPC_URL", "https://gateway.example")
		t.Setenv("TRON_API_KEY_TYPE", "X-Custom-Key")
		backend, err := newTronBackend(&captureOptions{chain: "tron", network: "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, "x-custom-key", backend.env.apiKeyHeader)
	})
}

func TestRunTronRequiresRPCEndpoint(t *testing.T) {
	t.Setenv("TRON_RPC_URL", "")
	err := run([]string{"--chain", "tron", "--network", "mainnet", "--adapter", "tron"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TRON_RPC_URL")
}
