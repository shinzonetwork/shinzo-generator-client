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

	shinzoerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// A valid range is answered with the store's IDs for its first maxDocumentHeights heights at most;
// a malformed one is refused without reading the store, and one starting below the lowest stored
// height is answered 410 Gone with that height.
func TestDocumentsHandler(t *testing.T) {
	t.Parallel()
	stored := map[string][]string{
		"Ethereum__Mainnet__Header":         {"bae-1"},
		"Ethereum__Mainnet__BlockSignature": {"bae-2"},
	}
	cases := []struct {
		name       string
		query      string
		lowest     int64
		lowestErr  error
		storeErr   error
		wantStatus int
		wantRange  []int64
	}{
		{name: "one height", query: "from=5&to=5", wantStatus: http.StatusOK, wantRange: []int64{5, 5}},
		{name: "the widest range", query: fmt.Sprintf("from=1&to=%d", maxDocumentHeights), wantStatus: http.StatusOK, wantRange: []int64{1, maxDocumentHeights}},
		{name: "a wider range", query: fmt.Sprintf("from=1&to=%d", maxDocumentHeights+500), wantStatus: http.StatusOK, wantRange: []int64{1, maxDocumentHeights}},
		{name: "to below from", query: "from=6&to=5", wantStatus: http.StatusBadRequest},
		{name: "a negative height", query: "from=-1&to=5", wantStatus: http.StatusBadRequest},
		{name: "no from", query: "to=5", wantStatus: http.StatusBadRequest},
		{name: "no to", query: "from=0", wantStatus: http.StatusBadRequest},
		{name: "from the lowest stored height", query: "from=5&to=9", lowest: 5, wantStatus: http.StatusOK, wantRange: []int64{5, 9}},
		{name: "below the lowest stored height", query: "from=4&to=9", lowest: 5, wantStatus: http.StatusGone},
		{name: "nothing stored", query: "from=5&to=5", lowestErr: shinzoerrors.NewDocumentNotFound("defra", "lowest", "Header", "no blocks"), wantStatus: http.StatusOK, wantRange: []int64{5, 5}},
		{name: "the lowest height fails", query: "from=5&to=5", lowestErr: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError},
		{name: "the store fails", query: "from=5&to=5", storeErr: errors.New("store unavailable"), wantStatus: http.StatusInternalServerError, wantRange: []int64{5, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var asked []int64
			handler := documentsHandler(func(_ context.Context, from, to int64) (map[string][]string, error) {
				asked = []int64{from, to}
				return stored, tc.storeErr
			}, func(context.Context) (int64, error) {
				return tc.lowest, tc.lowestErr
			})
			rec := httptest.NewRecorder()

			handler(rec, httptest.NewRequest(http.MethodGet, "/api/v1/documents?"+tc.query, nil))

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantRange, asked)
			switch tc.wantStatus {
			case http.StatusOK:
				var got map[string][]string
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				assert.Equal(t, stored, got)
			case http.StatusGone:
				var got goneResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
				assert.Equal(t, tc.lowest, got.LowestHeight)
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
	}, func(context.Context) (int64, error) {
		return 0, nil
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
