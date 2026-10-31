package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A valid range is answered with the store's IDs for exactly that range; a malformed or too wide
// one is refused without reading the store.
func TestDocumentsHandler(t *testing.T) {
	t.Parallel()
	stored := map[string][]string{
		"Ethereum__Mainnet__Header":         {"bae-1"},
		"Ethereum__Mainnet__BlockSignature": {"bae-2"},
	}
	cases := []struct {
		name       string
		query      string
		storeErr   error
		wantStatus int
		wantRange  []int64
	}{
		{name: "one height", query: "from=5&to=5", wantStatus: http.StatusOK, wantRange: []int64{5, 5}},
		{name: "the widest range", query: fmt.Sprintf("from=1&to=%d", maxDocumentHeights), wantStatus: http.StatusOK, wantRange: []int64{1, maxDocumentHeights}},
		{name: "a range too wide", query: fmt.Sprintf("from=1&to=%d", maxDocumentHeights+1), wantStatus: http.StatusBadRequest},
		{name: "to below from", query: "from=6&to=5", wantStatus: http.StatusBadRequest},
		{name: "a negative height", query: "from=-1&to=5", wantStatus: http.StatusBadRequest},
		{name: "no from", query: "to=5", wantStatus: http.StatusBadRequest},
		{name: "no to", query: "from=0", wantStatus: http.StatusBadRequest},
		{name: "the store fails", query: "from=5&to=5", storeErr: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError, wantRange: []int64{5, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var asked []int64
			handler := documentsHandler(func(_ context.Context, from, to int64) (map[string][]string, error) {
				asked = []int64{from, to}
				return stored, tc.storeErr
			})
			rec := httptest.NewRecorder()

			handler(rec, httptest.NewRequest(http.MethodGet, "/api/v1/documents?"+tc.query, nil))

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantRange, asked)
			if tc.wantStatus == http.StatusOK {
				var got map[string][]string
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				assert.Equal(t, stored, got)
			}
		})
	}
}

// The documents endpoint sits behind the same authentication as the schema endpoints.
func TestDocumentsEndpoint_AuthWiring(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableDocumentsEndpoint(func(context.Context, int64, int64) (map[string][]string, error) {
		return map[string][]string{}, nil
	}, NewBearerAuthenticator([]string{"secret"}))

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
			req := httptest.NewRequest(http.MethodGet, "/api/v1/documents?from=1&to=1", nil)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}

			hs.mux.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
