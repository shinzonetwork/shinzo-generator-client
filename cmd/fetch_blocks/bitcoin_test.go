package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// testCount and testHashPlain are a consistent getblockcount /
	// getblockhash answer pair, and testBlock echoes the same hash and
	// height the way a Core verbosity-3 body does.
	testCount     = "845213"
	testHashPlain = "0000000000000000000123456789abcdef0123456789abcdef0123456789abcdef"
	testHash      = `"` + testHashPlain + `"`
	testBlock     = `{"hash":"` + testHashPlain + `","height":845213,"tx":[],"nTx":0}`

	// Core's -8 lag answer, served as HTTP 500 with an error object: the
	// shape a load-balanced backend answers before it has indexed the
	// height.
	testLagBody = `{"result":null,"error":{"code":-8,"message":"Block height out of range"},"id":1}`
)

// bitcoinCaptureServer is a minimal Core JSON-RPC endpoint for the capture
// unit tests: one canned answer per method, request capture for the param and
// auth assertions, and a lag knob that makes the first N getblockhash calls
// answer the -8 shape, so the capture's lag retry can be exercised.
type bitcoinCaptureServer struct {
	count      string // bare JSON literal for getblockcount
	hash       string // quoted hash for getblockhash
	block      string // verbosity-3 object for getblock
	lagAnswers int    // first N getblockhash calls answer the lag body
	lagBody    string // raw HTTP 500 body carrying a JSON-RPC error object

	countCalls  int
	hashCalls   int
	blockCalls  int
	lastHeight  int64
	lastHashReq string
	lastVerb    int
	lastUser    string
	lastPass    string
	lastKey     string
	lastKeyHdr  string
}

func (s *bitcoinCaptureServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		s.lastUser, s.lastPass, _ = r.BasicAuth()
		s.lastKey = r.Header.Get("X-Api-Key")
		s.lastKeyHdr = r.Header.Get("X-Custom-Key")

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case bitcoinMethodGetBlockCount:
			s.countCalls++
			_, _ = fmt.Fprintf(w, `{"result":%s,"error":null,"id":1}`, s.count)
		case bitcoinMethodGetBlockHash:
			s.hashCalls++
			if s.lagAnswers > 0 {
				s.lagAnswers--
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(s.lagBody))
				return
			}
			_ = json.Unmarshal(req.Params[0], &s.lastHeight)
			_, _ = fmt.Fprintf(w, `{"result":%s,"error":null,"id":1}`, s.hash)
		case bitcoinMethodGetBlock:
			s.blockCalls++
			_ = json.Unmarshal(req.Params[0], &s.lastHashReq)
			_ = json.Unmarshal(req.Params[1], &s.lastVerb)
			_, _ = fmt.Fprintf(w, `{"result":%s,"error":null,"id":1}`, s.block)
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
			http.Error(w, "unexpected method", http.StatusNotFound)
		}
	})
}

// newBitcoinTestBackend builds a bitcoinBackend pointed at the test server
// with a full basic-auth pair, the capture's default shape.
func newBitcoinTestBackend(server *httptest.Server) *bitcoinBackend {
	return &bitcoinBackend{env: &bitcoinEnv{
		client:       server.Client(),
		url:          server.URL,
		user:         "capture-user",
		password:     "capture-password",
		apiKeyHeader: bitcoinDefaultAPIKeyHeader,
	}}
}

func TestParseFlagsAdapterBitcoin(t *testing.T) {
	t.Parallel()

	t.Run("bitcoin accepted", func(t *testing.T) {
		t.Parallel()
		opts, err := parseFlags([]string{"--chain", "bitcoin", "--network", "mainnet", "--adapter", "bitcoin"})
		require.NoError(t, err)
		assert.Equal(t, adapterBitcoin, opts.adapter)
	})

	t.Run("default stays evm", func(t *testing.T) {
		t.Parallel()
		opts, err := parseFlags([]string{"--chain", "ethereum", "--network", "mainnet"})
		require.NoError(t, err)
		assert.Equal(t, adapterEVM, opts.adapter)
	})

	t.Run("unknown adapter still rejected", func(t *testing.T) {
		t.Parallel()
		_, err := parseFlags([]string{"--chain", "x", "--network", "mainnet", "--adapter", "solana"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), adapterBitcoin, "the rejection must list every supported adapter")
	})
}

func TestNewBitcoinBackendEnv(t *testing.T) {
	// Not parallel: the subtests write process-wide env vars.
	//
	// Every subtest neutralizes the BITCOIN_* family first: mise loads the
	// developer's .env into every run, so a real endpoint and auth pair sit
	// in the process environment and an unset-looking case would silently
	// read them.

	t.Run("missing url", func(t *testing.T) {
		t.Setenv("BITCOIN_RPC_URL", "")
		t.Setenv("BITCOIN_RPC_USER", "")
		t.Setenv("BITCOIN_RPC_PASSWORD", "")
		t.Setenv("BITCOIN_API_KEY", "")
		_, err := newBitcoinBackend(&captureOptions{chain: "Bitcoin", network: "Mainnet"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BITCOIN_RPC_URL is required")
	})

	t.Run("missing auth names both mechanisms", func(t *testing.T) {
		t.Setenv("BITCOIN_RPC_URL", "http://node:8332")
		t.Setenv("BITCOIN_RPC_USER", "")
		t.Setenv("BITCOIN_RPC_PASSWORD", "")
		t.Setenv("BITCOIN_API_KEY", "")
		_, err := newBitcoinBackend(&captureOptions{chain: "Bitcoin", network: "Mainnet"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BITCOIN_RPC_USER")
		assert.Contains(t, err.Error(), "BITCOIN_API_KEY")
	})

	t.Run("half basic-auth pair without a key is rejected", func(t *testing.T) {
		t.Setenv("BITCOIN_RPC_URL", "http://node:8332")
		t.Setenv("BITCOIN_RPC_USER", "user")
		t.Setenv("BITCOIN_RPC_PASSWORD", "")
		t.Setenv("BITCOIN_API_KEY", "")
		_, err := newBitcoinBackend(&captureOptions{chain: "Bitcoin", network: "Mainnet"})
		require.Error(t, err)
	})

	t.Run("basic auth configures cleanly", func(t *testing.T) {
		t.Setenv("BITCOIN_RPC_URL", "http://node:8332")
		t.Setenv("BITCOIN_RPC_USER", "user")
		t.Setenv("BITCOIN_RPC_PASSWORD", "pass")
		t.Setenv("BITCOIN_API_KEY", "")
		backend, err := newBitcoinBackend(&captureOptions{chain: "Bitcoin", network: "Mainnet"})
		require.NoError(t, err)
		assert.Equal(t, bitcoinDefaultAPIKeyHeader, backend.env.apiKeyHeader)
	})

	t.Run("api key with custom header type", func(t *testing.T) {
		t.Setenv("BITCOIN_RPC_URL", "http://node:8332")
		t.Setenv("BITCOIN_RPC_USER", "")
		t.Setenv("BITCOIN_RPC_PASSWORD", "")
		t.Setenv("BITCOIN_API_KEY", "secret")
		t.Setenv("BITCOIN_API_KEY_TYPE", "X-Custom-Key")
		backend, err := newBitcoinBackend(&captureOptions{chain: "Bitcoin", network: "Mainnet"})
		require.NoError(t, err)
		assert.Equal(t, "x-custom-key", backend.env.apiKeyHeader)
	})
}

func TestBitcoinFetchTip(t *testing.T) {
	t.Parallel()
	srv := &bitcoinCaptureServer{count: testCount}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	tip, err := newBitcoinTestBackend(server).fetchTip(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(845213), tip)
	assert.Equal(t, 1, srv.countCalls)
}

func TestBitcoinFetchTipRejectsNegative(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer((&bitcoinCaptureServer{count: "-1"}).handler(t))
	defer server.Close()

	_, err := newBitcoinTestBackend(server).fetchTip(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getblockcount")
}

func TestBitcoinCaptureBlock(t *testing.T) {
	t.Parallel()
	srv := &bitcoinCaptureServer{count: testCount, hash: testHash, block: testBlock}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	fb, err := newBitcoinTestBackend(server).captureBlock(context.Background(), 845213)
	require.NoError(t, err)

	// Decimal height, empty receipts, and the none mode keep the on-disk
	// fixture envelope uniform across adapters.
	assert.Equal(t, "845213", fb.Number)
	assert.JSONEq(t, `[]`, string(fb.Receipts))
	assert.Equal(t, receiptModeNone, fb.ReceiptMode)
	// The body is stored verbatim: the replay mock serves exactly what the
	// node served.
	assert.JSONEq(t, testBlock, string(fb.Block))

	// The two-call pair matches the production fetcher: the height lookup
	// first, the by-hash body fetch with verbosity 3 second.
	assert.Equal(t, int64(845213), srv.lastHeight)
	assert.Equal(t, testHashPlain, srv.lastHashReq)
	assert.Equal(t, bitcoinCaptureVerbosity, srv.lastVerb)
}

func TestBitcoinCaptureBlockHashMismatchFails(t *testing.T) {
	t.Parallel()
	otherHashBlock := `{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","height":845213}`
	server := httptest.NewServer((&bitcoinCaptureServer{count: testCount, hash: testHash, block: otherHashBlock}).handler(t))
	defer server.Close()

	_, err := newBitcoinTestBackend(server).captureBlock(context.Background(), 845213)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "answered hash")
}

func TestBitcoinCaptureBlockHeightMismatchFails(t *testing.T) {
	t.Parallel()
	otherHeightBlock := `{"hash":"` + testHashPlain + `","height":845212}`
	server := httptest.NewServer((&bitcoinCaptureServer{count: testCount, hash: testHash, block: otherHeightBlock}).handler(t))
	defer server.Close()

	_, err := newBitcoinTestBackend(server).captureBlock(context.Background(), 845213)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "answered height")
}

func TestBitcoinCaptureRetriesHeightLag(t *testing.T) {
	t.Parallel()
	srv := &bitcoinCaptureServer{count: testCount, hash: testHash, block: testBlock, lagAnswers: 1, lagBody: testLagBody}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	fb, err := newBitcoinTestBackend(server).captureBlock(context.Background(), 845213)
	require.NoError(t, err)
	assert.Equal(t, 2, srv.hashCalls, "the -8 lag answer must be retried")
	assert.Equal(t, receiptModeNone, fb.ReceiptMode)
}

func TestBitcoinCaptureDoesNotRetryEnvelopeError(t *testing.T) {
	t.Parallel()
	notFoundBody := `{"result":null,"error":{"code":-5,"message":"Block not found"},"id":1}`
	srv := &bitcoinCaptureServer{count: testCount, hash: testHash, block: testBlock, lagAnswers: 999, lagBody: notFoundBody}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	_, err := newBitcoinTestBackend(server).captureBlock(context.Background(), 845213)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Block not found")
	assert.Equal(t, 1, srv.hashCalls, "a permanent RPC answer is the node's verdict and must not be retried")
}

func TestBitcoinCallRetries429ThenSucceeds(t *testing.T) {
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
		_, _ = fmt.Fprintf(w, `{"result":%s,"error":null,"id":1}`, testCount)
	}))
	defer server.Close()

	old := retryBackoffFn
	retryBackoffFn = fastBackoff()
	t.Cleanup(func() { retryBackoffFn = old })

	tip, err := newBitcoinTestBackend(server).fetchTip(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(845213), tip)
	assert.Equal(t, 2, requests)
}

func TestBitcoinBackendAuthOnTheWire(t *testing.T) {
	t.Parallel()

	t.Run("basic auth pair rides the request", func(t *testing.T) {
		t.Parallel()
		srv := &bitcoinCaptureServer{count: testCount}
		server := httptest.NewServer(srv.handler(t))
		defer server.Close()

		backend := &bitcoinBackend{env: &bitcoinEnv{
			client:   server.Client(),
			url:      server.URL,
			user:     "core-user",
			password: "core-pass",
		}}
		_, err := backend.fetchTip(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "core-user", srv.lastUser)
		assert.Equal(t, "core-pass", srv.lastPass)
	})

	t.Run("hosted key rides the configured header", func(t *testing.T) {
		t.Parallel()
		srv := &bitcoinCaptureServer{count: testCount}
		server := httptest.NewServer(srv.handler(t))
		defer server.Close()

		backend := &bitcoinBackend{env: &bitcoinEnv{
			client:       server.Client(),
			url:          server.URL,
			apiKey:       "hosted-key",
			apiKeyHeader: "x-custom-key",
		}}
		_, err := backend.fetchTip(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "hosted-key", srv.lastKeyHdr)
	})
}
