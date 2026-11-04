//go:build integration
// +build integration

package bitcoin

// The wire dialect layer of the scripted Bitcoin Core fake: an httptest
// server speaking Core's JSON-RPC 1.0 dialect (result/error/id envelopes,
// RPC failures served as HTTP 500 with an error object), with full request
// recording. Every request's method, params, and headers are captured so
// tests can audit the authentication coverage on the wire and assert that
// no height below a resume point was ever re-requested.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
)

// mockRPCError mirrors one JSON-RPC error object.
type mockRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mockResponse describes what the fake answers with. Exactly one style
// wins, in the order rawBody, rpcErr, httpStatus, result.
type mockResponse struct {
	// result rides inside the JSON-RPC 1.0 envelope Core serves for
	// successes.
	result json.RawMessage
	// rpcErr rides inside the error envelope with Core's RPC-failure
	// convention: HTTP 500 plus a JSON body.
	rpcErr *mockRPCError
	// rawBody is a prebuilt full response body served unchanged.
	rawBody string
	// httpStatus serves a non-JSON body, the proxy-shaped failure.
	httpStatus int
	httpBody   string
}

// recordedCall is one request the fake received.
type recordedCall struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	// Header carries the request's headers on the wire, including the
	// Authorization and api-key headers the auth audit asserts on.
	Header http.Header `json:"-"`
}

// coreRPCServer is an httptest server speaking Bitcoin Core's JSON-RPC 1.0
// dialect with request recording.
type coreRPCServer struct {
	*httptest.Server

	mu    sync.Mutex
	calls []recordedCall
}

// recordedCalls copies the recorded requests under the lock, so test-side
// reads cannot race the handler goroutine that appends them.
func (s *coreRPCServer) recordedCalls() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// callMarker returns the index of the next recorded call. A later
// callsSince(marker) view isolates the requests that arrived after the
// marker, which is how the restart/resume leg bounds its no-refetch
// assertion to the restarted run.
func (s *coreRPCServer) callMarker() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// callsSince returns the requests recorded after the given marker index.
func (s *coreRPCServer) callsSince(marker int) []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if marker > len(s.calls) {
		return nil
	}
	return slices.Clone(s.calls[marker:])
}

// newCoreRPCServer starts the wire-level fake whose answers come from the
// handler; requests are recorded before the handler runs.
func newCoreRPCServer(handler func(call recordedCall) mockResponse) *coreRPCServer {
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
	return server
}
