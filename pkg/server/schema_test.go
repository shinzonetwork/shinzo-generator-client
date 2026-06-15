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

const testSDL = "type Query { hello: String }"
const testNetwork = "Ethereum__Mainnet"

func TestSchemaHandler_PlainTextResponse(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, testSDL, rec.Body.String())
}

func TestSchemaHandler_PlainTextResponse_ExplicitAcceptHeader(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, testSDL, rec.Body.String())
}

func TestSchemaHandler_JSONResponse(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "application/json")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, testNetwork, resp.Network)
	assert.Equal(t, testSDL, resp.SDL)
}

func TestSchemaHandler_DefaultAcceptHeader(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "*/*")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_BothAcceptTypesPrefersText(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "text/plain, application/json")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_EmptySDL(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler("", testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", rec.Body.String())
}

func TestSchemaHandler_EmptySDL_JSON(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler("", testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "application/json")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp schemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "", resp.SDL)
	assert.Equal(t, testNetwork, resp.Network)
}

func TestSchemaHandler_MethodNotAllowed_Post(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/api/v1/schema", nil)
			handler(rec, req)
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
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Accept", "application/json")
	handler(rec, req)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestSchemaHandler_PlainTextContentTypeCharset(t *testing.T) {
	t.Parallel()
	handler := SchemaHandler(testSDL, testNetwork)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	handler(rec, req)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
}

// --- SetSchemaHandler integration tests ---

func TestSetSchemaHandler_RegistersRoute(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	logger, _ := newCaptureLogger()
	hs.SetSchemaHandler(NoOpAuthenticator{}, testSDL, testNetwork, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSetSchemaHandler_WithNoOpAuth_AllowsThrough(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	logger, _ := newCaptureLogger()
	hs.SetSchemaHandler(NoOpAuthenticator{}, testSDL, testNetwork, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testSDL, rec.Body.String())
}

func TestSetSchemaHandler_WithBearerAuth_MissingCreds_401(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	logger, _ := newCaptureLogger()
	hs.SetSchemaHandler(NewBearerAuthenticator([]string{"secret"}), testSDL, testNetwork, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, strings.Contains(rec.Body.String(), testSDL))
}

func TestSetSchemaHandler_WithBearerAuth_ValidCreds_200(t *testing.T) {
	t.Parallel()
	hs := NewHealthServer(0, nil, "")
	logger, _ := newCaptureLogger()
	hs.SetSchemaHandler(NewBearerAuthenticator([]string{"secret"}), testSDL, testNetwork, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schema", nil)
	req.Header.Set("Authorization", "Bearer secret")
	hs.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testSDL, rec.Body.String())
}
