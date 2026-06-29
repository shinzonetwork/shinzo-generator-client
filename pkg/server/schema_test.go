package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSDL     = "type Query { hello: String }"
	testNetwork = "Ethereum__Mainnet"
)

func newHealthServerWithSchema(t *testing.T, sdl string) *HealthServer {
	t.Helper()
	hs := NewHealthServer(0, nil, "")
	require.NoError(t, hs.EnableSchemaEndpoint(sdl, testNetwork, NoOpAuthenticator{}))
	return hs
}

// --- SchemaHandler integration tests ---

func TestSchemaHandler(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema(t, testSDL)

	t.Run("read methods", func(t *testing.T) {
		t.Parallel()
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method, func(t *testing.T) {
				t.Parallel()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/api/v1/schema", nil)
				hs.mux.ServeHTTP(rec, req)

				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
				assert.Equal(t, constants.CacheControlSchema, rec.Header().Get("Cache-Control"))

				if method == http.MethodHead {
					assert.Empty(t, rec.Body.String(), "HEAD response should have no body")
				} else {
					var resp schemaResponse
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
					assert.Equal(t, testNetwork, resp.Network)
					assert.Equal(t, testSDL, resp.Schema)
				}
			})
		}
	})

	t.Run("non-read methods return 405", func(t *testing.T) {
		t.Parallel()
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			t.Run(method, func(t *testing.T) {
				t.Parallel()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/api/v1/schema", nil)
				hs.mux.ServeHTTP(rec, req)

				assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
				assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
			})
		}
	})
}

func TestSchemaHandler_EmptySDL(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "", resp.Schema)
	assert.Equal(t, testNetwork, resp.Network)
}

// --- CollectionHandler tests ---

func TestCollectionHandler(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema(t, testSDL)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/api/v1/schema/block", nil)
			hs.mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
			assert.Equal(t, constants.CacheControlSchema, rec.Header().Get("Cache-Control"))

			if method == http.MethodHead {
				assert.Empty(t, rec.Body.String(), "HEAD response should have no body")
			} else {
				var resp schemaResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				assert.Equal(t, testNetwork, resp.Network)
				assert.NotEmpty(t, resp.Schema)
			}
		})
	}
}

func TestCollectionHandler_UnknownCollection(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema(t, testSDL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema/unknown", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "not_found", errResp.Code)
	assert.Contains(t, errResp.Message, "unknown")
}

// --- CollectionsListHandler tests ---

func TestCollectionsListHandler(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema(t, testSDL)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/api/v1/schema/collections", nil)
			hs.mux.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
			assert.Equal(t, constants.CacheControlSchema, rec.Header().Get("Cache-Control"))

			if method == http.MethodHead {
				assert.Empty(t, rec.Body.String(), "HEAD response should have no body")
			} else {
				var resp collectionsResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				assert.Equal(t, testNetwork, resp.Network)
				assert.NotEmpty(t, resp.Collections)

				expectedEntries := schema.ListCollections(testNetwork)
				assert.Equal(t, len(expectedEntries), len(resp.Collections))
				for i, entry := range resp.Collections {
					assert.Equal(t, expectedEntries[i].Name, entry.Name)
					assert.Equal(t, expectedEntries[i].TypeName, entry.TypeName)
				}
			}
		})
	}
}

// --- Auth wiring tests ---

func TestSchemaEndpoint_AuthWiring(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	require.NoError(t, hs.EnableSchemaEndpoint(testSDL, testNetwork, NewBearerAuthenticator([]string{"secret"})))

	routes := []struct {
		name string
		path string
	}{
		{"schema", "/api/v1/schema"},
		{"collection", "/api/v1/schema/block"},
		{"collections", "/api/v1/schema/collections"},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			t.Run("missing_creds_401", func(t *testing.T) {
				t.Parallel()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, route.path, nil)
				hs.mux.ServeHTTP(rec, req)
				assert.Equal(t, http.StatusUnauthorized, rec.Code)
				assert.False(t, strings.Contains(rec.Body.String(), testSDL))
			})

			t.Run("valid_creds_200", func(t *testing.T) {
				t.Parallel()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, route.path, nil)
				req.Header.Set("Authorization", "Bearer secret")
				hs.mux.ServeHTTP(rec, req)
				assert.Equal(t, http.StatusOK, rec.Code)
			})
		})
	}
}

// --- Integration: all EnableSchemaEndpoint routes ---

func TestEnableSchemaEndpoint_RegistersAllRoutes(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema(t, testSDL)

	for _, tc := range []struct {
		name   string
		path   string
		isList bool
	}{
		{name: "schema endpoint", path: "/api/v1/schema"},
		{name: "collection endpoint", path: "/api/v1/schema/block"},
		{name: "collections list endpoint", path: "/api/v1/schema/collections", isList: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			hs.mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))

			if tc.isList {
				var resp collectionsResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				assert.Equal(t, testNetwork, resp.Network)
				assert.NotEmpty(t, resp.Collections)
			} else {
				var resp schemaResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				assert.Equal(t, testNetwork, resp.Network)
				assert.NotEmpty(t, resp.Schema)
			}
		})
	}
}
