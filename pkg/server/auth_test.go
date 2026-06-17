package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingAuthenticator struct {
	err error
}

func (f failingAuthenticator) Authenticate(_ *http.Request) error { return f.err }

type capturedRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]string
}

type captureHandler struct {
	records []capturedRecord
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{Level: r.Level, Message: r.Message, Attrs: make(map[string]string)}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.records = append(h.records, rec)
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func newCaptureLogger() (*slog.Logger, *captureHandler) {
	h := &captureHandler{}
	return slog.New(h), h
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// --- NoOpAuthenticator ---

func TestNoOpAuthenticator_ReturnsNilOnAnyRequest(t *testing.T) {
	t.Parallel()
	auth := NoOpAuthenticator{}
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	assert.Nil(t, auth.Authenticate(req))
}

// --- BearerAuthenticator unit tests ---

func TestBearerAuthenticator_NoKeysConfigured(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator(nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrNoKeysConfigured)
}

func TestBearerAuthenticator_NoKeysConfigured_EmptySlice(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrNoKeysConfigured)
}

func TestBearerAuthenticator_MissingCredentials(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrMissingCredentials)
}

func TestBearerAuthenticator_EmptyBearerToken(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer ")
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrMissingCredentials)
}

func TestBearerAuthenticator_EmptyXAPIKey(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Api-Key", "   ")
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrMissingCredentials)
}

func TestBearerAuthenticator_InvalidBearerToken(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrInvalidCredentials)
}

func TestBearerAuthenticator_InvalidXAPIKey(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Api-Key", "wrong-key")
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrInvalidCredentials)
}

func TestBearerAuthenticator_ValidBearerToken(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	assert.Nil(t, auth.Authenticate(req))
}

func TestBearerAuthenticator_ValidXAPIKey(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Api-Key", "secret")
	assert.Nil(t, auth.Authenticate(req))
}

func TestBearerAuthenticator_BearerTakesPrecedenceOverXAPIKey(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"bearer-key", "xapi-key"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bearer-key")
	req.Header.Set("X-Api-Key", "xapi-key")
	assert.Nil(t, auth.Authenticate(req))
}

func TestBearerAuthenticator_BearerInvalidDoesNotFallBackToXAPIKey(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"xapi-key"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("X-Api-Key", "xapi-key")
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrInvalidCredentials)
}

func TestBearerAuthenticator_IgnoresEmptyKeys(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"", "valid-key"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer valid-key")
	assert.Nil(t, auth.Authenticate(req))
}

func TestBearerAuthenticator_IgnoresEmptyKeys_EmptyTokenFails(t *testing.T) {
	t.Parallel()
	auth := NewBearerAuthenticator([]string{"", "valid-key"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer ")
	assert.ErrorIs(t, auth.Authenticate(req), authErrors.ErrMissingCredentials)
}

// --- authMiddleware integration tests ---

func TestAuthMiddleware_NoOp_AllowsThrough(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	handler := authMiddleware(NoOpAuthenticator{}, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestAuthMiddleware_Bearer_MissingCredentials_401(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	handler := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	var resp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "unauthorized", resp.Code)
	assert.Equal(t, "missing or empty credentials", resp.Message)
}

func TestAuthMiddleware_Bearer_InvalidCredentials_403(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	handler := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	handler(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	var resp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "forbidden", resp.Code)
	assert.Equal(t, "invalid credentials", resp.Message)
}

func TestAuthMiddleware_Bearer_NoKeys_503(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	auth := NewBearerAuthenticator(nil)
	handler := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var resp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "service_unavailable", resp.Code)
	assert.Equal(t, "no API keys configured on server", resp.Message)
}

func TestAuthMiddleware_ValidBearer_200(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	handler := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer secret")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_ValidXAPIKey_200(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	handler := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Api-Key", "secret")
	handler(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_UnknownError_500(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()
	auth := failingAuthenticator{err: errors.New("something unexpected")} //nolint:err113
	handler := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	handler(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var resp authErrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "internal_error", resp.Code)
	assert.Equal(t, "unexpected authentication failure", resp.Message)
}

// --- Audit logging tests ---

func TestAuthMiddleware_LogsWarnOnMissingCredentials(t *testing.T) {
	t.Parallel()
	logger, handler := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	mw := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	mw(rec, req)
	require.NotEmpty(t, handler.records)
	assert.Equal(t, slog.LevelWarn, handler.records[0].Level)
	assert.Equal(t, "missing_credentials", handler.records[0].Attrs["reason"])
}

func TestAuthMiddleware_LogsWarnOnInvalidCredentials(t *testing.T) {
	t.Parallel()
	logger, handler := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	mw := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	mw(rec, req)
	require.NotEmpty(t, handler.records)
	assert.Equal(t, slog.LevelWarn, handler.records[0].Level)
	assert.Equal(t, "invalid_credentials", handler.records[0].Attrs["reason"])
}

func TestAuthMiddleware_LogsErrorOnNoKeys(t *testing.T) {
	t.Parallel()
	logger, handler := newCaptureLogger()
	auth := NewBearerAuthenticator(nil)
	mw := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	mw(rec, req)
	require.NotEmpty(t, handler.records)
	assert.Equal(t, slog.LevelError, handler.records[0].Level)
	assert.Equal(t, "no_keys_configured", handler.records[0].Attrs["reason"])
}

func TestAuthMiddleware_LogFields(t *testing.T) {
	t.Parallel()
	logger, handler := newCaptureLogger()
	auth := NewBearerAuthenticator([]string{"secret"})
	mw := authMiddleware(auth, okHandler, logger)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	mw(rec, req)
	require.NotEmpty(t, handler.records)
	assert.Equal(t, "POST", handler.records[0].Attrs["method"])
	assert.Equal(t, "/graphql", handler.records[0].Attrs["path"])
}

// --- extractToken edge cases ---

func TestExtractToken_CaseInsensitiveBearer(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "bearer my-token")
	assert.Equal(t, "my-token", extractToken(req))
}

func TestExtractToken_BearerUpper(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "BEARER my-token")
	assert.Equal(t, "my-token", extractToken(req))
}

func TestExtractToken_BearerWithExtraSpaces(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer   my-token  ")
	assert.Equal(t, "my-token", extractToken(req))
}

func TestExtractToken_NoAuthHeader_FallsBackToXAPIKey(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Api-Key", "api-key-123")
	assert.Equal(t, "api-key-123", extractToken(req))
}

func TestExtractToken_NoHeaders(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, "", extractToken(req))
}

// --- reasonFor ---

func TestReasonFor_MissingCredentials(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "missing_credentials", reasonFor(authErrors.ErrMissingCredentials))
}

func TestReasonFor_InvalidCredentials(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "invalid_credentials", reasonFor(authErrors.ErrInvalidCredentials))
}

func TestReasonFor_NoKeysConfigured(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "no_keys_configured", reasonFor(authErrors.ErrNoKeysConfigured))
}

func TestReasonFor_Unknown(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "unknown", reasonFor(errors.New("boom"))) //nolint:err113
}

// --- Error response content type verification ---

func TestAuthMiddleware_AllErrorsHaveJSONContentType(t *testing.T) {
	t.Parallel()
	logger, _ := newCaptureLogger()

	cases := []struct {
		name    string
		auth    Authenticator
		setup   func(r *http.Request)
		wantErr string
	}{
		{
			name:    "missing credentials",
			auth:    NewBearerAuthenticator([]string{"k"}),
			setup:   func(_ *http.Request) {},
			wantErr: "unauthorized",
		},
		{
			name:    "invalid credentials",
			auth:    NewBearerAuthenticator([]string{"k"}),
			setup:   func(r *http.Request) { r.Header.Set("Authorization", "Bearer bad") },
			wantErr: "forbidden",
		},
		{
			name:    "no keys configured",
			auth:    NewBearerAuthenticator(nil),
			setup:   func(_ *http.Request) {},
			wantErr: "service_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := authMiddleware(tc.auth, okHandler, logger)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			tc.setup(req)
			handler(rec, req)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var resp authErrors.ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tc.wantErr, resp.Code)
			assert.NotEmpty(t, resp.Message)
			assert.NotContains(t, rec.Body.String(), "SDL")
			assert.False(t, strings.Contains(rec.Body.String(), "secret"))
		})
	}
}
