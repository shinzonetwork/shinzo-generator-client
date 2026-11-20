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

	"github.com/gagliardetto/solana-go/rpc/jsonrpc"
	"github.com/shinzonetwork/shinzo-generator-client/config"
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

// requestBody returns the recorded body of the first request that used the
// given JSON-RPC method.
func (s *fakeRPCServer) requestBody(t *testing.T, method string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.requests {
		if req.method == method {
			return req.body
		}
	}
	return ""
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

// TestNewHTTPTransport_DialTimeoutDefaults pins the per-phase dial budgets:
// 0 or negative resolves to the config default, positive values are used
// as-is for TCP dial, TLS handshake, and the time to the first byte.
func TestNewHTTPTransport_DialTimeoutDefaults(t *testing.T) {
	t.Parallel()

	byDefault := newHTTPTransport(0).(*httpStatusTransport)
	transport, ok := byDefault.base.(*http.Transport)
	require.True(t, ok, "transport must wrap an *http.Transport")
	want := time.Duration(config.DefaultSolanaDialTimeoutSeconds) * time.Second
	assert.Equal(t, want, transport.TLSHandshakeTimeout)
	assert.Equal(t, want, transport.ResponseHeaderTimeout)

	explicit := newHTTPTransport(7).(*httpStatusTransport)
	transport, ok = explicit.base.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 7*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 7*time.Second, transport.ResponseHeaderTimeout)
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
	require.NotNil(t, block.BlockTime)
	assert.Equal(t, int64(1774267845), *block.BlockTime)

	require.Len(t, block.Transactions, 2)

	// Transaction 0: legacy, successful.
	tx0 := block.Transactions[0]
	assert.Equal(t, "legacy", tx0.Version)
	assert.Empty(t, tx0.Err, "successful transactions carry no err")
	assert.Equal(t, uint64(5000), tx0.Fee)
	assert.Nil(t, tx0.ComputeUnitsConsumed)
	assert.Equal(t,
		fixtureSig1, tx0.Signature)
	assert.Equal(t, []string{
		fixtureKeyA,
		fixtureKeyB,
	}, tx0.AccountKeys, "static account keys")
	assert.Empty(t, tx0.LoadedAddresses, "no ALT-loaded keys for this transaction")
	assert.Equal(t, fixtureRecent, tx0.RecentBlockhash)
	assert.Equal(t, []string{"Program log: entrypoint exited"}, tx0.LogMessages)
	assert.Equal(t, []uint64{100000, 200000}, tx0.PreBalances)
	assert.Equal(t, []uint64{95000, 200000}, tx0.PostBalances)

	require.Len(t, tx0.Instructions, 1)
	outer := tx0.Instructions[0]
	assert.Equal(t, fixtureKeyA, outer.ProgramID)
	assert.Equal(t, []string{fixtureKeyB}, outer.Accounts)
	assert.Equal(t, "111", outer.Data)
	assert.Nil(t, outer.StackHeight, "outer instructions carry no stack height")

	require.Len(t, tx0.InnerInstructions, 1)
	require.Len(t, tx0.InnerInstructions[0].Instructions, 1)
	inner := tx0.InnerInstructions[0].Instructions[0]
	assert.Equal(t, uint16(0), tx0.InnerInstructions[0].Index)
	assert.Equal(t, fixtureKeyA, inner.ProgramID)
	assert.Equal(t, []string{fixtureKeyB}, inner.Accounts)
	assert.Equal(t, "1111", inner.Data)
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
	assert.Contains(t, tx1.Err, "InstructionError")
	assert.Equal(t, uint64(9000), tx1.Fee)
	require.NotNil(t, tx1.ComputeUnitsConsumed)
	assert.Equal(t, uint64(4321), *tx1.ComputeUnitsConsumed)
	assert.Nil(t, tx1.LogMessages, "absent logMessages stay nil")
	assert.Nil(t, tx1.PreTokenBalances)
	assert.Nil(t, tx1.PostTokenBalances)
	assert.Equal(t, []string{fixtureKeyW, fixtureKeyR}, tx1.LoadedAddresses,
		"writable keys before readonly keys")

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

func TestGetFirstAvailableBlock_ReturnsArchiveFloor(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getFirstAvailableBlock" {
			return 0, nil, getSlotResultBody(250000000)
		}
		return 0, nil, ""
	})
	client := testClient(t, srv.URL)

	slot, err := client.GetFirstAvailableBlock(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(250000000), slot)
	assert.NotEmpty(t, srv.requestBody(t, "getFirstAvailableBlock"))
}

func TestGetFirstAvailableBlock_MethodMissingWrapped(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, ""
	})
	client := testClient(t, srv.URL)

	_, err := client.GetFirstAvailableBlock(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getFirstAvailableBlock")
}

// ---------------------------------------------------------------------------
// HTTP-layer status handling: non-2xx responses become typed errors, and the
// JSON-RPC 200 envelopes classify through the RPC error code path.
// ---------------------------------------------------------------------------

func TestGetSlot_Non2xxBecomesTypedStatusError(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return http.StatusServiceUnavailable, nil, "provider overloaded"
	})
	client := testClient(t, srv.URL)

	_, err := client.GetSlot(context.Background())
	require.Error(t, err)
	assert.NotErrorIs(t, err, errSlotSkipped)

	var statusErr *httpStatusError
	require.True(t, stderrors.As(err, &statusErr), "error must surface as httpStatusError: %v", err)
	assert.Equal(t, http.StatusServiceUnavailable, statusErr.status)
	assert.Contains(t, statusErr.msg, "provider overloaded")
	assert.Equal(t, "http_error", rpcErrorType(err))
}

func TestGetSlot_RateLimitedClassifiedRateLimited(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return http.StatusTooManyRequests, nil, "CU limit reached"
	})
	client := testClient(t, srv.URL)

	_, err := client.GetSlot(context.Background())
	require.Error(t, err)
	assert.Equal(t, "rate_limited", rpcErrorType(err), "429 must classify as rate_limited")
}

// TestRPCErrorType pins the dashboard vocabulary: the same buckets the EVM
// adapter reports (rate_limited, http_error, method_not_found, rpc_error,
// timeout, _OTHER).
func TestRPCErrorType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "RateLimited", err: &httpStatusError{status: 429, msg: "slow down"}, want: "rate_limited"},
		{name: "HTTPError", err: &httpStatusError{status: 500, msg: "broken"}, want: "http_error"},
		{name: "MethodNotFound", err: fmt.Errorf("wrapped: %w", &jsonrpc.RPCError{Code: -32601, Message: "not found"}), want: "method_not_found"},
		{name: "RPCError", err: fmt.Errorf("wrapped: %w", &jsonrpc.RPCError{Code: -32004, Message: "no block"}), want: "rpc_error"},
		{name: "Timeout", err: fmt.Errorf("outer: %w", context.DeadlineExceeded), want: "timeout"},
		{name: "Other", err: fmt.Errorf("mysterious"), want: "_OTHER"},
		{name: "SkippedIsStillRPCErrorAfterUnwrap", err: fmt.Errorf("%w: boom", errSlotSkipped), want: "_OTHER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, rpcErrorType(tc.err))
		})
	}
}

// TestCountRPCError_SkipsExpectedOutcomes pins that routine near-tip
// outcomes are not counted as RPC failures while transport and unknown
// errors are (the counter is nil-safe by construction).
func TestCountRPCError_SkipsExpectedOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		expect bool
	}{
		{name: "notConfirmed", err: errNotConfirmed, expect: false},
		{name: "notAvailable", err: errBlockNotAvailable, expect: false},
		{name: "skipped", err: errSlotSkipped, expect: false},
		{name: "archiveNotConfigured", err: errArchiveNotConfigured, expect: false},
		{name: "contextCancelled", err: fmt.Errorf("call: %w", context.Canceled), expect: false},
		{name: "cleanedUp", err: errBlockCleanedUp, expect: true},
		{name: "httpStatus", err: &httpStatusError{status: 403, msg: "forbidden"}, expect: true},
		{name: "unknown", err: fmt.Errorf("dom error"), expect: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.expect, !isContextError(tc.err) && !isExpectedOutcome(tc.err),
				"classification gate must match the expected-outcome policy")
		})
	}
}

// ---------------------------------------------------------------------------
// Request shapes.
// ---------------------------------------------------------------------------

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

func TestGetBlock_SendsDefaultAPIKeyHeaderType(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(string) (int, http.Header, string) {
		return 0, nil, getSlotResultBody(1)
	})
	client := testClient(t, srv.URL, func(opts *ClientOptions) {
		opts.APIKey = "sample-api-key"
	})

	_, err := client.GetSlot(context.Background())
	require.NoError(t, err)

	headers, _ := srv.requestHeaders(t, "getSlot")
	require.NotNil(t, headers)
	assert.Equal(t, "sample-api-key", headers.Get("X-Api-Key"), "empty api_key_type falls back to x-api-key")
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
// rewards flag - the getBlock contract the adapter depends on.
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

	body := srv.requestBody(t, "getBlock")
	require.NotEmpty(t, body)
	assert.Contains(t, body, `"transactionDetails":"full"`)
	assert.Contains(t, body, `"maxSupportedTransactionVersion":1`)
	assert.Contains(t, body, `"rewards":false`, "rewards flag must reflect configuration")
	assert.Contains(t, body, "42", "slot must be the first getBlock parameter")
}

// TestGetBlock_CarriesCommitment proves the block fetch is issued at the
// configured commitment (confirmed in tests, finalized configured).
func TestGetBlock_CarriesCommitment(t *testing.T) {
	t.Parallel()

	srv := newFakeRPCServer(t, func(method string) (int, http.Header, string) {
		if method == "getBlock" {
			return 0, nil, jsonRPCResult(t, fixtureResult(t, "getBlock_confirmed_nulls.json"))
		}
		return 0, nil, getSlotResultBody(1000)
	})
	client := testClient(t, srv.URL, func(opts *ClientOptions) {
		opts.Commitment = "finalized"
	})

	_, err := client.GetBlock(context.Background(), 42)
	require.NoError(t, err)

	body := srv.requestBody(t, "getBlock")
	assert.Contains(t, body, `"commitment":"finalized"`)
}
