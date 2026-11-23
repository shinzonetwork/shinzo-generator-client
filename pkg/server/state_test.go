package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stateBody is a state request body naming slots 0x1 to 0xn.
func stateBody(blockHash, address string, n int) string {
	slots := make([]string, n)
	for i := range slots {
		slots[i] = fmt.Sprintf("0x%x", i+1)
	}
	body, _ := json.Marshal(stateRequest{BlockHash: blockHash, Address: address, Slots: slots})
	return string(body)
}

// A well-formed request, including one with no slots that reads the account alone, is answered with
// the block's number and hash and the proof in hex, the node given stateTimeout to answer. A malformed
// one, one naming more than maxStateSlots slots, or a body over maxStateRequestBytes is refused
// without asking the node. A node error is answered with 502 and the node's message.
func TestStateHandler(t *testing.T) {
	t.Parallel()
	hash := "0x" + strings.Repeat("ab", 32)
	address := "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d"
	widest := make([]string, maxStateSlots)
	for i := range widest {
		widest[i] = "0x" + strings.Repeat("ef", 32)
	}
	largest, err := json.MarshalIndent(stateRequest{BlockHash: hash, Address: address, Slots: widest}, "", "  ")
	require.NoError(t, err)
	cases := []struct {
		name       string
		body       string
		nodeErr    error
		wantStatus int
		wantAsked  bool
	}{
		{name: "one slot", body: stateBody(hash, address, 1), wantStatus: http.StatusOK, wantAsked: true},
		{name: "the most slots", body: stateBody(hash, address, maxStateSlots), wantStatus: http.StatusOK, wantAsked: true},
		{name: "too many slots", body: stateBody(hash, address, maxStateSlots+1), wantStatus: http.StatusBadRequest},
		{name: "no slots", body: stateBody(hash, address, 0), wantStatus: http.StatusOK, wantAsked: true},
		{name: "the largest request, indented", body: string(largest), wantStatus: http.StatusOK, wantAsked: true},
		{name: "a body over the cap", body: strings.Repeat(" ", maxStateRequestBytes) + stateBody(hash, address, 1), wantStatus: http.StatusBadRequest},
		{name: "a short block hash", body: stateBody("0xab", address, 1), wantStatus: http.StatusBadRequest},
		{name: "a short address", body: stateBody(hash, "0x8d0d", 1), wantStatus: http.StatusBadRequest},
		{name: "a slot without 0x", body: `{"blockHash":"` + hash + `","address":"` + address + `","slots":["35"]}`, wantStatus: http.StatusBadRequest},
		{name: "not JSON", body: "{", wantStatus: http.StatusBadRequest},
		{name: "the node fails", body: stateBody(hash, address, 1), nodeErr: errors.New("missing trie node"), wantStatus: http.StatusBadGateway, wantAsked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			asked := false
			handler := stateHandler(func(ctx context.Context, blockHash, gotAddress string, _ []string) (int64, []byte, error) {
				asked = true
				deadline, ok := ctx.Deadline()
				assert.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), stateTimeout)
				assert.Equal(t, hash, blockHash)
				assert.Equal(t, address, gotAddress)
				return 26378186, []byte{1, 2}, tc.nodeErr
			})
			rec := httptest.NewRecorder()

			handler(rec, httptest.NewRequest(http.MethodPost, "/api/v1/state", strings.NewReader(tc.body)))

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantAsked, asked)
			switch tc.wantStatus {
			case http.StatusOK:
				var got stateResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				assert.Equal(t, stateResponse{BlockNumber: 26378186, BlockHash: hash, Proof: "0x0102"}, got)
			case http.StatusBadGateway:
				var got errorResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				assert.Equal(t, "missing trie node", got.Message)
			}
		})
	}
}

// At most maxConcurrentStateRequests requests are answered at once. One more is refused with 503
// without asking the node, and is answered again once a request finishes.
func TestStateHandler_ConcurrencyLimit(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var asked atomic.Int32
		handler := stateHandler(func(context.Context, string, string, []string) (int64, []byte, error) {
			asked.Add(1)
			<-release
			return 1, []byte{1}, nil
		})
		body := stateBody("0x"+strings.Repeat("ab", 32), "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d", 1)
		serve := func() int {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodPost, "/api/v1/state", strings.NewReader(body)))
			return rec.Code
		}

		codes := make(chan int, maxConcurrentStateRequests)
		for range maxConcurrentStateRequests {
			go func() { codes <- serve() }()
		}
		synctest.Wait()
		require.Equal(t, int32(maxConcurrentStateRequests), asked.Load())
		assert.Equal(t, http.StatusServiceUnavailable, serve())
		assert.Equal(t, int32(maxConcurrentStateRequests), asked.Load())

		close(release)
		for range maxConcurrentStateRequests {
			assert.Equal(t, http.StatusOK, <-codes)
		}
		assert.Equal(t, http.StatusOK, serve())
	})
}

// The state endpoint sits behind the same authentication as the schema endpoints.
func TestStateEndpoint_AuthWiring(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableStateEndpoint(func(context.Context, string, string, []string) (int64, []byte, error) {
		return 1, []byte{1}, nil
	}, NewBearerAuthenticator([]string{"secret"}))
	body := stateBody("0x"+strings.Repeat("ab", 32), "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d", 1)

	for _, tc := range []struct {
		name          string
		authorization string
		want          int
	}{
		{name: "no credentials", want: http.StatusUnauthorized},
		{name: "valid credentials", authorization: "Bearer secret", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/state", strings.NewReader(body))
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}

			hs.mux.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
