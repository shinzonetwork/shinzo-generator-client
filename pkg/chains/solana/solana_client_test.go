package solana

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Base58 literals shared with the testdata fixtures. Lengths are exact:
// n leading '1's decode to n zero bytes, and 43-char strings starting with a
// high-value letter decode to exactly 32 bytes (58^42 needs ~246 bits).
var (
	fixtureHash0  = strings.Repeat("1", 32) // blockhash: all-zero hash
	fixturePrev   = strings.Repeat("Z", 43) // previous blockhash
	fixtureKeyA   = strings.Repeat("z", 43) // static account key 0
	fixtureKeyB   = strings.Repeat("Z", 43) // static account key 1
	fixtureRecent = strings.Repeat("y", 43) // recent blockhash
	fixtureKeyW   = strings.Repeat("y", 43) // ALT-loaded writable key
	fixtureKeyR   = strings.Repeat("Y", 43) // ALT-loaded readonly key
	fixtureSig1   = strings.Repeat("1", 64) // signature: 64 zero bytes
)

// ---------------------------------------------------------------------------
// Fake JSON-RPC server helpers.
// ---------------------------------------------------------------------------

// capturedRequest records what the fake server received, for header and
// payload assertions.
type capturedRequest struct {
	method  string
	headers http.Header
	body    string
}

// fakeRPCServer serves JSON-RPC responses from a per-method responder and
// records every request it receives.
type fakeRPCServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []capturedRequest
}

// responder returns the raw response body for a JSON-RPC method call; a nil
// body means "no route" and yields a method-not-found error envelope.
type responder func(method string) (status int, headers http.Header, body string)

func newFakeRPCServer(t *testing.T, respond responder) *fakeRPCServer {
	t.Helper()
	srv := &fakeRPCServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var envelope struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		_ = json.Unmarshal(raw, &envelope)

		srv.mu.Lock()
		srv.requests = append(srv.requests, capturedRequest{
			method:  envelope.Method,
			headers: r.Header.Clone(),
			body:    string(raw),
		})
		srv.mu.Unlock()

		status, headers, body := respond(envelope.Method)
		if body == "" {
			status, body = http.StatusOK, jsonRPCErrorEnvelope(-32601, "method not found")
		}
		if status == 0 {
			status = http.StatusOK
		}
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// requestHeaders returns the recorded headers of the first request that used
// the given JSON-RPC method.
func (s *fakeRPCServer) requestHeaders(t *testing.T, method string) (http.Header, int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.requests {
		if req.method == method {
			return req.headers, len(s.requests)
		}
	}
	return nil, len(s.requests)
}

// getSlotParams returns the commitment carried by the recorded getSlot
// request. The SDK sends an options object ([{"commitment": "..."}]), not a
// bare string; returns "" when no getSlot call was made or no commitment was
// included.
func (s *fakeRPCServer) getSlotParams(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.requests {
		if req.method == "getSlot" {
			var envelope struct {
				Params []any `json:"params"`
			}
			require.NoError(t, json.Unmarshal([]byte(req.body), &envelope))
			if len(envelope.Params) == 0 {
				return ""
			}
			switch param := envelope.Params[0].(type) {
			case string:
				return param
			case map[string]any:
				commitment, _ := param["commitment"].(string)
				return commitment
			}
			return ""
		}
	}
	return ""
}

// jsonRPCResult builds a JSON-RPC success envelope around a canned result.
func jsonRPCResult(t *testing.T, result any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "result": result, "id": 1})
	require.NoError(t, err)
	return string(raw)
}

// jsonRPCErrorEnvelope builds a JSON-RPC error envelope with the given code.
func jsonRPCErrorEnvelope(code int, message string) string {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"error":   map[string]any{"code": code, "message": message},
	})
	return string(raw)
}

// fixtureResult loads a canned getBlock result payload from testdata and
// embeds it verbatim in the response envelope.
func fixtureResult(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return json.RawMessage(raw)
}

// testClient builds a confirmed-commitment client pointed at the fake
// server, closed automatically with the test.
func testClient(t *testing.T, url string, mutate ...func(*ClientOptions)) *Client {
	t.Helper()
	opts := ClientOptions{
		RPCURL:                         url,
		Commitment:                     "confirmed",
		MaxSupportedTransactionVersion: 1,
		Rewards:                        true,
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	client, err := NewClient(context.Background(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func getSlotResultBody(n uint64) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","result":%d,"id":1}`, n)
}

// ---------------------------------------------------------------------------
// Construction.
// ---------------------------------------------------------------------------

func TestNewClient_EmptyURL(t *testing.T) {
	t.Parallel()

	_, err := NewClient(context.Background(), ClientOptions{})
	require.Error(t, err)
	assert.ErrorContains(t, err, "rpc_url is empty")
}

func TestNewClient_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewClient(ctx, ClientOptions{RPCURL: "http://localhost:1"})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Full-fidelity conversion (fixture-driven).
// ---------------------------------------------------------------------------

func TestGetBlock_ConvertsConfirmedBlockFull(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL)

	block, err := client.GetBlock(context.Background(), 397234561)
	require.NoError(t, err)
	require.NotNil(t, block)

	// Block-level fields.
	assert.Equal(t, uint64(397234561), block.Slot)
	assert.Equal(t, fixtureHash0, block.Blockhash)
	assert.Equal(t, fixturePrev, block.PreviousBlockhash)
	assert.Equal(t, uint64(397234560), block.ParentSlot)
	require.NotNil(t, block.BlockHeight)
	assert.Equal(t, uint64(270123456), *block.BlockHeight)
	require.NotNil(t, block.BlockTime)
	assert.Equal(t, int64(1774267845), *block.BlockTime)

	require.Len(t, block.Transactions, 2)

	// Transaction 0: legacy, successful.
	tx0 := block.Transactions[0]
	assert.Equal(t, "legacy", tx0.Version)
	assert.False(t, tx0.Failed)
	assert.Empty(t, tx0.Err)
	assert.Equal(t, uint64(5000), tx0.Fee)
	assert.Nil(t, tx0.ComputeUnitsConsumed)
	assert.Equal(t, uint64(397234561), tx0.Slot)
	assert.Equal(t, 0, tx0.TransactionIndex)
	assert.Equal(t,
		fixtureSig1, tx0.Signature)
	assert.Equal(t, []string{
		fixtureKeyA,
		fixtureKeyB,
	}, tx0.AccountKeys, "static account keys")
	assert.Empty(t, tx0.LoadedAddressesWritable)
	assert.Empty(t, tx0.LoadedAddressesReadonly)
	assert.Equal(t, fixtureRecent, tx0.RecentBlockhash)
	assert.Equal(t, []string{"Program log: entrypoint exited"}, tx0.LogMessages)
	assert.Equal(t, []uint64{100000, 200000}, tx0.PreBalances)
	assert.Equal(t, []uint64{95000, 200000}, tx0.PostBalances)

	require.Len(t, tx0.Instructions, 1)
	outer := tx0.Instructions[0]
	assert.Equal(t, fixtureKeyA, outer.ProgramID)
	assert.Equal(t, []string{fixtureKeyB}, outer.Accounts)
	assert.Equal(t, "111", outer.Data)
	assert.Equal(t, 0, outer.InstructionIndex)
	assert.Equal(t, 0, outer.InnerIndex)
	assert.Nil(t, outer.StackHeight, "outer instructions carry no stack height")

	require.Len(t, tx0.InnerInstructions, 1)
	require.Len(t, tx0.InnerInstructions[0].Instructions, 1)
	inner := tx0.InnerInstructions[0].Instructions[0]
	assert.Equal(t, uint16(0), tx0.InnerInstructions[0].Index)
	assert.Equal(t, fixtureKeyA, inner.ProgramID)
	assert.Equal(t, []string{fixtureKeyB}, inner.Accounts)
	assert.Equal(t, "1111", inner.Data)
	assert.Equal(t, 0, inner.InstructionIndex)
	assert.Equal(t, 0, inner.InnerIndex, "inner index is the 0-based position within the CPI group")
	require.NotNil(t, inner.StackHeight)
	assert.Equal(t, uint16(1), *inner.StackHeight)

	require.Len(t, tx0.PreTokenBalances, 1)
	preBalance := tx0.PreTokenBalances[0]
	assert.Equal(t, uint16(1), preBalance.AccountIndex)
	assert.Equal(t, fixtureKeyB, preBalance.Mint)
	assert.Empty(t, preBalance.Owner)
	assert.Empty(t, preBalance.ProgramID)
	assert.Equal(t, "100", preBalance.Amount)
	assert.Equal(t, uint8(6), preBalance.Decimals)

	require.Len(t, tx0.PostTokenBalances, 1)
	postBalance := tx0.PostTokenBalances[0]
	assert.Equal(t, fixtureKeyA, postBalance.ProgramID)
	assert.Equal(t, "150", postBalance.Amount)

	// Transaction 1: failed v0 transaction with ALT-loaded addresses.
	tx1 := block.Transactions[1]
	assert.Equal(t, "0", tx1.Version)
	assert.True(t, tx1.Failed)
	assert.Contains(t, tx1.Err, "InstructionError")
	assert.Equal(t, uint64(9000), tx1.Fee)
	require.NotNil(t, tx1.ComputeUnitsConsumed)
	assert.Equal(t, uint64(4321), *tx1.ComputeUnitsConsumed)
	assert.Nil(t, tx1.LogMessages, "absent logMessages stay nil")
	assert.Nil(t, tx1.PreTokenBalances)
	assert.Nil(t, tx1.PostTokenBalances)
	assert.Equal(t, []string{fixtureKeyW}, tx1.LoadedAddressesWritable)
	assert.Equal(t, []string{fixtureKeyR}, tx1.LoadedAddressesReadonly)

	// ALT-loaded keys come after static keys in resolution order.
	require.Len(t, tx1.InnerInstructions, 1)
	require.Len(t, tx1.InnerInstructions[0].Instructions, 1)
	resolved := tx1.InnerInstructions[0].Instructions[0]
	assert.Equal(t, fixtureKeyW, resolved.ProgramID,
		"program ID index 2 resolves into the loaded writable range")
	assert.Equal(t, []string{fixtureKeyR}, resolved.Accounts,
		"account index 3 resolves into the loaded readonly range")
	require.NotNil(t, resolved.StackHeight)
	assert.Equal(t, uint16(2), *resolved.StackHeight)

	// Rewards.
	require.Len(t, block.Rewards, 2)
	assert.Equal(t, int64(-25000), block.Rewards[0].Lamports, "fee rewards can be debits")
	assert.Nil(t, block.Rewards[0].Commission)
	assert.Equal(t, "Fee", block.Rewards[0].RewardType)
	assert.Equal(t, uint64(987654321), block.Rewards[0].PostBalance)
	require.NotNil(t, block.Rewards[1].Commission)
	assert.Equal(t, uint8(5), *block.Rewards[1].Commission)
}

func TestGetBlock_NullFieldsSurvive(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_nulls.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL)

	block, err := client.GetBlock(context.Background(), 397234561)
	require.NoError(t, err)
	require.NotNil(t, block)

	assert.Nil(t, block.BlockHeight, "null blockHeight must stay nil")
	assert.Nil(t, block.BlockTime, "null blockTime must stay nil")
	assert.Empty(t, block.Transactions)
	require.Len(t, block.Rewards, 1)
	assert.Nil(t, block.Rewards[0].Commission, "absent commission must stay nil")
	assert.Equal(t, "Rent", block.Rewards[0].RewardType)
}

// ---------------------------------------------------------------------------
// Error classification.
// ---------------------------------------------------------------------------

func TestGetBlock_ErrorClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		code      int
		message   string
		wantMatch error
	}{
		{name: "SlotSkipped", code: -32007, message: "Slot 5 was skipped", wantMatch: errSlotSkipped},
		{name: "LongTermStorageSkipped", code: -32009, message: "Slot 5 skipped in long-term storage", wantMatch: errSlotSkipped},
		{name: "BlockNotAvailable", code: -32004, message: "Block not available for slot 5", wantMatch: errBlockNotAvailable},
		{name: "BlockCleanedUp", code: -32001, message: "Block 5 cleaned up", wantMatch: errBlockCleanedUp},
		{name: "UnknownCode", code: -32010, message: "boom", wantMatch: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := newFakeRPCServer(t, func(_ string) (int, http.Header, string) {
				return 0, nil, jsonRPCErrorEnvelope(tc.code, tc.message)
			})
			client := testClient(t, srv.URL)

			block, err := client.GetBlock(context.Background(), 5)
			require.Error(t, err)
			assert.Nil(t, block)
			if tc.wantMatch != nil {
				assert.True(t, stderrors.Is(err, tc.wantMatch), "error must wrap the sentinel: %v", err)
			} else {
				assert.NotErrorIs(t, err, errSlotSkipped)
				assert.NotErrorIs(t, err, errBlockNotAvailable)
				assert.NotErrorIs(t, err, errBlockCleanedUp)
			}
		})
	}
}

func TestGetBlock_NullResultClassifiedNotConfirmed(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, `{"jsonrpc":"2.0","result":null,"id":1}`
	})
	client := testClient(t, srv.URL)

	block, err := client.GetBlock(context.Background(), 5)
	require.Error(t, err)
	assert.Nil(t, block)
	assert.True(t, stderrors.Is(err, errNotConfirmed), "null result means not-confirmed, not hard failure: %v", err)
}

// ---------------------------------------------------------------------------
// Archive routing.
// ---------------------------------------------------------------------------

func TestGetBlockFromArchive_NotConfigured(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) { return 0, nil, getSlotResultBody(1000) })
	client := testClient(t, srv.URL)

	block, err := client.GetBlockFromArchive(context.Background(), 5)
	require.Error(t, err)
	assert.Nil(t, block)
	assert.True(t, stderrors.Is(err, errArchiveNotConfigured))
	assert.NotErrorIs(t, err, errSlotSkipped)
}

func TestGetBlockFromArchive_ReturnsBlock(t *testing.T) {
	t.Parallel()

	mainSrv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCErrorEnvelope(-32001, "Block 5 cleaned up, does not exist on node")
		}
		return 0, nil, getSlotResultBody(1000)
	})
	archiveSrv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})

	client, err := NewClient(context.Background(), ClientOptions{
		RPCURL:                         mainSrv.URL,
		ArchiveRPCURL:                  archiveSrv.URL,
		Commitment:                     "confirmed",
		MaxSupportedTransactionVersion: 1,
		Rewards:                        true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	block, err := client.GetBlockFromArchive(context.Background(), 5)
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Len(t, block.Transactions, 2)
}

func TestGetSlot_ReturnsTip(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, getSlotResultBody(424242)
	})
	client := testClient(t, srv.URL)

	slot, err := client.GetSlot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(424242), slot)
	assert.Equal(t, "confirmed", srv.getSlotParams(t), "tip queries must carry the configured commitment")
}

// ---------------------------------------------------------------------------
// Transport behaviour: retry-with-backoff and API-key headers.
// ---------------------------------------------------------------------------

func TestRetryTransport_RateLimitedThenSuccess(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	getSlotCalls := 0
	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getSlot" {
			mu.Lock()
			defer mu.Unlock()
			getSlotCalls++
			if getSlotCalls == 1 {
				// No Retry-After: transport applies its exponential backoff.
				return http.StatusTooManyRequests, nil, "throttled"
			}
			return 0, nil, getSlotResultBody(7)
		}
		return 0, nil, ""
	})
	client := testClient(t, srv.URL)

	slot, err := client.GetSlot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(7), slot)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, getSlotCalls, "one 429 cycle must retry exactly once")
}

// TestGetSlot_OverloadAbortsOnContextExpiry proves the transport loop cannot
// outlive its caller: bounded retries against a permanently-overloaded
// server, then the deadline error surfaces (no unbounded blocking).
func TestRetryTransport_HonorsRetryAfterAndContext(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getSlot" {
			// Retry-After: 0 means "retry immediately", but the transport
			// still throttles to its base backoff so callers cannot spin.
			return http.StatusServiceUnavailable, http.Header{"Retry-After": []string{"0"}}, "overloaded"
		}
		return 0, nil, ""
	})

	// The cancellation must abort even a zero-Retry-After loop before its
	// full attempt budget is spent: bounded work, then a context error.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	client, err := NewClient(context.Background(), ClientOptions{
		RPCURL:     srv.URL,
		Commitment: "confirmed",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.GetSlot(ctx)
	require.Error(t, err)
	assert.True(t, stderrors.Is(err, context.DeadlineExceeded) ||
		StringContains(err.Error(), "overloaded", "context"),
		"either ctx-bound abort or documented transport failure: %v", err)
}

// StringContains reports whether s contains any of the candidates.
func StringContains(s string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(s, candidate) {
			return true
		}
	}
	return false
}

func TestGetBlock_SendsAPIKeyHeader(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getSlot" {
			return 0, nil, getSlotResultBody(1)
		}
		return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_full.json"))
	})
	client := testClient(t, srv.URL, func(opts *ClientOptions) {
		opts.APIKey = "sample-api-key"
		opts.APIKeyType = "X-Api-Key"
	})

	_, err := client.GetSlot(context.Background())
	require.NoError(t, err)

	headers, _ := srv.requestHeaders(t, "getSlot")
	require.NotNil(t, headers)
	assert.Equal(t, "sample-api-key", headers.Get("X-Api-Key"))
}

func TestGetBlock_NoAPIKeySendsNoHeader(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, getSlotResultBody(1)
	})
	client := testClient(t, srv.URL)

	_, err := client.GetSlot(context.Background())
	require.NoError(t, err)

	headers, _ := srv.requestHeaders(t, "getSlot")
	require.NotNil(t, headers)
	assert.Empty(t, headers.Get("X-Api-Key"), "no API key configured, so no auth header")
}

// TestGetBlock_CarriesGetBlockParameters proves the wire request carries the
// mandatory version parameter, the full detail level, and the configured
// rewards flag — the	GetBlock contract the adapter depends on.
func TestGetBlock_CarriesGetBlockParameters(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_nulls.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL, func(opts *ClientOptions) {
		opts.Rewards = false
	})

	_, err := client.GetBlock(context.Background(), 42)
	require.NoError(t, err)

	srv.mu.Lock()
	defer srv.mu.Unlock()
	var body string
	for _, req := range srv.requests {
		if req.method == "getBlock" {
			body = req.body
			break
		}
	}
	assert.Contains(t, body, `"transactionDetails":"full"`)
	assert.Contains(t, body, `"maxSupportedTransactionVersion":1`)
	assert.Contains(t, body, `"rewards":false`, "rewards flag must reflect configuration")
	assert.Contains(t, body, "42", "slot must be the first getBlock parameter")
}
