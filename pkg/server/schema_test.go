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

func newHealthServerWithSchema() *HealthServer {
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NoOpAuthenticator{})
	return hs
}

// --- requireReadMethod unit tests ---

func TestRequireReadMethod_AllowsGet(t *testing.T) {
	t.Parallel()
	called := false
	handler := requireReadMethod(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler(rec, req)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireReadMethod_AllowsHead(t *testing.T) {
	t.Parallel()
	called := false
	handler := requireReadMethod(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/test", nil)
	handler(rec, req)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireReadMethod_RejectsPost(t *testing.T) {
	t.Parallel()
	handler := requireReadMethod(func(http.ResponseWriter, *http.Request) {})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "method_not_allowed", errResp.Code)
	assert.Equal(t, "only GET and HEAD are supported", errResp.Message)
}

func TestRequireReadMethod_AllowHeader(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			handler := requireReadMethod(func(http.ResponseWriter, *http.Request) {})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/test", nil)
			handler(rec, req)
			assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
		})
	}
}

// --- SchemaHandler integration tests ---

func TestSchemaHandler_ReturnsJSON(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, testNetwork, resp.Network)
	assert.Equal(t, testSDL, resp.Schema)
}

func TestSchemaHandler_EmptySDL_JSON(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint("", testNetwork, NoOpAuthenticator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "", resp.Schema)
	assert.Equal(t, testNetwork, resp.Network)
}

func TestSchemaHandler_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/api/v1/schema", nil)
			hs.mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

			var errResp errorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
			assert.Equal(t, "method_not_allowed", errResp.Code)
			assert.Equal(t, "only GET and HEAD are supported", errResp.Message)
		})
	}
}

func TestSchemaHandler_MethodNotAllowed_AllowHeader(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/api/v1/schema", nil)
			hs.mux.ServeHTTP(rec, req)
			assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
		})
	}
}

func TestSchemaHandler_JSONContentType(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_405ResponseContentType(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
}

// --- Cache-Control tests ---

func TestSchemaHandler_CacheControl_Set(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, constants.CacheControlSchema, rec.Header().Get("Cache-Control"))
}

func TestSchemaHandler_CacheControl_NotSetOn405(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Empty(t, rec.Header().Get("Cache-Control"))
}

// --- HEAD tests ---

func TestSchemaHandler_HEAD_JSON(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
	assert.Equal(t, constants.CacheControlSchema, rec.Header().Get("Cache-Control"))
	assert.Empty(t, rec.Body.String(), "HEAD response should have no body")
}

// --- EnableSchemaEndpoint integration tests ---

func TestEnableSchemaEndpoint_RegistersRoute(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NoOpAuthenticator{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnableSchemaEndpoint_WithNoOpAuth_AllowsThrough(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NoOpAuthenticator{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, testSDL, resp.Schema)
}

func TestEnableSchemaEndpoint_WithBearerAuth_MissingCreds_401(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NewBearerAuthenticator([]string{"secret"}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, strings.Contains(rec.Body.String(), testSDL))
}

func TestEnableSchemaEndpoint_WithBearerAuth_ValidCreds_200(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NewBearerAuthenticator([]string{"secret"}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Authorization", "Bearer secret")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, testSDL, resp.Schema)
}

// --- CollectionHandler tests ---

func TestCollectionHandler_ValidCollection_JSON(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema/block", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, testNetwork, resp.Network)
	assert.NotEmpty(t, resp.Schema)
}

func TestCollectionHandler_ValidCollection_HEAD(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/api/v1/schema/block", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))
	assert.Equal(t, constants.CacheControlSchema, rec.Header().Get("Cache-Control"))
	assert.Empty(t, rec.Body.String(), "HEAD response should have no body")
}

func TestCollectionHandler_UnknownCollection_404(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema/unknown", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "not_found", errResp.Code)
	assert.Contains(t, errResp.Message, "unknown")
}

func TestCollectionHandler_MethodNotAllowed_405(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schema/block", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))

	var errResp errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "method_not_allowed", errResp.Code)
}

// --- CollectionsListHandler tests ---

func TestCollectionsListHandler_JSON(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema/collections", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, constants.ContentTypeJSON, rec.Header().Get("Content-Type"))

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

func TestCollectionsListHandler_MethodNotAllowed_405(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/schema/collections", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}

// --- Integration: all EnableSchemaEndpoint routes ---

func TestEnableSchemaEndpoint_RegistersAllRoutes(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NoOpAuthenticator{})

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "schema endpoint", method: http.MethodGet, path: "/api/v1/schema", want: http.StatusOK},
		{name: "collection endpoint", method: http.MethodGet, path: "/api/v1/schema/block", want: http.StatusOK},
		{name: "collections list endpoint", method: http.MethodGet, path: "/api/v1/schema/collections", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			hs.mux.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestEnableSchemaEndpoint_CollectionEndpoint_WithBearerAuth(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NewBearerAuthenticator([]string{"secret"}))

	t.Run("missing creds returns 401", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/schema/block", nil)
		hs.mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("valid creds returns 200", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/schema/block", nil)
		req.Header.Set("Authorization", "Bearer secret")
		hs.mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
