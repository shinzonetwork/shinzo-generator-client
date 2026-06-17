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
	assert.Equal(t, testSDL, resp.Schema)
}

func TestSchemaHandler_MissingAcceptHeader_406(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotAcceptable, rec.Code)

	var errResp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "not_acceptable", errResp.Code)
	assert.Equal(t, "supported content types: application/json, text/plain", errResp.Message)
}

func TestSchemaHandler_WildcardAcceptHeader_406(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "*/*")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotAcceptable, rec.Code)

	var errResp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "not_acceptable", errResp.Code)
	assert.Equal(t, "supported content types: application/json, text/plain", errResp.Message)
}

func TestSchemaHandler_UnsupportedAcceptHeader_406(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	for _, accept := range []string{"text/html", "application/xml", "image/png"} {
		t.Run(accept, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
			req.Header.Set("Accept", accept)
			hs.mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusNotAcceptable, rec.Code)

			var errResp authErrors.ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
			assert.Equal(t, "not_acceptable", errResp.Code)
		})
	}
}

func TestSchemaHandler_BothAcceptTypes_406(t *testing.T) {
	t.Parallel()
	hs := newHealthServerWithSchema()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain, application/json")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotAcceptable, rec.Code)

	var errResp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	assert.Equal(t, "not_acceptable", errResp.Code)
	assert.Equal(t, "supported content types: application/json, text/plain", errResp.Message)
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
	assert.Equal(t, "", resp.Schema)
	assert.Equal(t, testNetwork, resp.Network)
}

func TestSchemaHandler_EmptySDL_PlainText(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint("", testNetwork, NoOpAuthenticator{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", rec.Body.String())
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
	req.Header.Set("Accept", "text/plain")
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
	req.Header.Set("Accept", "text/plain")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnableSchemaEndpoint_WithNoOpAuth_AllowsThrough(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NoOpAuthenticator{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain")
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
	req.Header.Set("Accept", "text/plain")
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
	req.Header.Set("Accept", "text/plain")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testSDL, rec.Body.String())
}

func TestEnableSchemaEndpoint_WithBearerAuth_ValidCreds_406(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	hs.EnableSchemaEndpoint(testSDL, testNetwork, NewBearerAuthenticator([]string{"secret"}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Authorization", "Bearer secret")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotAcceptable, rec.Code)
}
