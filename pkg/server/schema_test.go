package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
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

func TestSchemaHandler_PlainTextResponse(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, testSDL, rec.Body.String())
}

func TestSchemaHandler_PlainTextResponse_ExplicitAcceptHeader(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, testSDL, rec.Body.String())
}

func TestSchemaHandler_JSONResponse(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "application/json")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, testNetwork, resp.Network)
	assert.Equal(t, testSDL, resp.SDL)
}

func TestSchemaHandler_DefaultAcceptHeader(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "*/*")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_BothAcceptTypesPrefersText(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain, application/json")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_EmptySDL(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint("", testNetwork, NoOpAuthenticator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", rec.Body.String())
}

func TestSchemaHandler_EmptySDL_JSON(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint("", testNetwork, NoOpAuthenticator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "application/json")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "", resp.SDL)
	assert.Equal(t, testNetwork, resp.Network)
}

func TestSchemaHandler_MethodNotAllowed_Post(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/api/v1/schema", nil)
			hs.mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

			var errResp authErrors.ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
			assert.Equal(t, "method_not_allowed", errResp.Code)
			assert.Equal(t, "only GET is supported", errResp.Message)
		})
	}
}

func TestSchemaHandler_JSONContentTypeCharset(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "application/json")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_PlainTextContentTypeCharset(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
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
	assert.Equal(t, testSDL, rec.Body.String())
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
	assert.Equal(t, testSDL, rec.Body.String())
}
