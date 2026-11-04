package bitcoin

// The mock Bitcoin Core JSON-RPC server and the fixture loader shared by the
// package's client tests. The harness speaks Core's JSON-RPC 1.0 dialect
// (result/error/id envelopes, RPC failures served as HTTP 500 with an error
// object), records every request's method, params and auth headers, and can
// serve prebuilt raw bodies for byte-identity pass-through tests.

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// fixtureFile holds real mainnet verbosity-3 data for height 184498 (327
// transactions, a coinbase paying 50.2207 BTC, and an 857.05 BTC prevout).
const fixtureFile = "testdata/block_184498.json"

// mockRPCError mirrors one JSON-RPC error object.
type mockRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mockResponse describes what the mock server answers with. Exactly one
// style wins, in the order rawBody, rpcErr, httpStatus, result.
type mockResponse struct {
	// result rides inside the standard JSON-RPC 1.0 envelope Core serves
	// for successes.
	result json.RawMessage
	// rpcErr rides inside the error envelope with Core's RPC-failure
	// convention: HTTP 500 plus a JSON body.
	rpcErr *mockRPCError
	// rawBody is a prebuilt full response body served unchanged; the
	// fixture pass-through tests use it to pin byte identity.
	rawBody string
	// httpStatus serves a non-JSON body, the proxy-shaped failure rpcclient
	// cannot parse into a JSON-RPC envelope.
	httpStatus int
	httpBody   string
}

// recordedCall is one request the mock server received.
type recordedCall struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	// Header carries the request's headers on the wire, including the
	// Authorization and api-key headers the auth tests assert on.
	Header http.Header `json:"-"`
}

// coreRPCServer is an httptest server speaking Bitcoin Core's JSON-RPC 1.0
// dialect with request recording.
type coreRPCServer struct {
	*httptest.Server

	mu    sync.Mutex
	calls []recordedCall
}

// callCount returns how many requests hit the given method.
func (s *coreRPCServer) callCount(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.calls {
		if call.Method == method {
			count++
		}
	}
	return count
}

// recordedCalls copies the recorded requests under the lock, so test-side
// reads cannot race the handler goroutine that appends them.
func (s *coreRPCServer) recordedCalls() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// newCoreRPCServer starts a mock Bitcoin Core whose answers come from the
// handler; requests are recorded before the handler runs, so cleanup-free
// uniform assertions stay possible from the tests.
func newCoreRPCServer(t *testing.T, handler func(call recordedCall) mockResponse) *coreRPCServer {
	t.Helper()
	server := &coreRPCServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		clone := r.Header.Clone()
		res := handler(recordedCall{Method: req.Method, Params: req.Params, Header: clone})
		server.mu.Lock()
		server.calls = append(server.calls, recordedCall{Method: req.Method, Params: req.Params, Header: clone})
		server.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case res.rawBody != "":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(res.rawBody))
		case res.rpcErr != nil:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": nil,
				"error":  res.rpcErr,
				"id":     req.ID,
			})
		case res.httpStatus != 0:
			w.WriteHeader(res.httpStatus)
			_, _ = w.Write([]byte(res.httpBody))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": res.result,
				"error":  nil,
				"id":     req.ID,
			})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// testConfig is a config fixture with basic auth filled in; tests override
// individual fields as needed.
func testConfig() config.BitcoinConfig {
	return config.BitcoinConfig{
		RPCUser:     "testrpcuser",
		RPCPassword: "testrpcpass",
	}
}

// newTestClient constructs a client against the mock endpoint and registers
// its shutdown as test cleanup.
func newTestClient(t *testing.T, cfg config.BitcoinConfig) *Client {
	t.Helper()
	client, err := NewClient(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// mustJSONMarshal turns typed payloads into the wire bytes a mock serves.
func mustJSONMarshal(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return raw
}

// mockBlockAt builds a successful getblock verbosity-3 answer for a canned
// payload.
func mockBlockAt(t *testing.T, payload any) mockResponse {
	return mockResponse{result: mustJSONMarshal(t, payload)}
}

// loadFixture returns the fixture's verbosity-3 JSON with trailing
// whitespace trimmed. The suite skips when the fixture has not been fetched
// yet: the file holds real mainnet data and must be taken from an archive
// node (pruned false), Bitcoin Core 23.0 or newer, with a call shaped like
//
//	bitcoin-cli getblock "$(bitcoin-cli getblockhash 184498)" 3 \
//	  > pkg/chains/bitcoin/testdata/block_184498.json
func loadFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixtureFile)
	if err != nil {
		require.ErrorIs(t, err, fs.ErrNotExist, "unexpected failure loading %s", fixtureFile)
		t.Skipf("fixture %s is absent: fetch verbosity-3 output of mainnet height 184498 from an archive Bitcoin Core node (prune=0, Core 23.0+)", fixtureFile)
		return nil
	}
	return bytes.TrimRight(raw, " \n\r\t")
}

// unmarshalParams decodes one recorded request's params array, with numbers
// as float64 and booleans as bool, ready for shape assertions.
func unmarshalParams(t *testing.T, call recordedCall) []any {
	t.Helper()
	var params []any
	require.NoError(t, json.Unmarshal(call.Params, &params), "params %s", call.Params)
	return params
}

// serveFixtureEnvelope wraps the fixture bytes into a full response body, so
// the served result stays byte-identical to the bytes the tests loaded.
func serveFixtureEnvelope(fixture []byte) string {
	return `{"result":` + string(fixture) + `,"error":null,"id":1}`
}
