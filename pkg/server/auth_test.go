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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthError_ErrorStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"MissingCredentials", ErrMissingCredentials, "missing credentials"},
		{"InvalidCredentials", ErrInvalidCredentials, "invalid credentials"},
		{"NoKeysConfigured", ErrNoKeysConfigured, "no API keys configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.expected {
				t.Errorf("Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

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

func TestBearerAuthenticator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		keys         []string
		authHeader   string
		apiKeyHeader string
		wantErr      error
	}{
		{"no keys configured", nil, "", "", ErrNoKeysConfigured},
		{"no keys configured empty slice", []string{}, "", "", ErrNoKeysConfigured},
		{"missing credentials", []string{"secret"}, "", "", ErrMissingCredentials},
		{"empty bearer token", []string{"secret"}, "Bearer ", "", ErrMissingCredentials},
		{"empty x-api-key", []string{"secret"}, "", "   ", ErrMissingCredentials},
		{"invalid bearer token", []string{"secret"}, "Bearer wrong-token", "", ErrInvalidCredentials},
		{"invalid x-api-key", []string{"secret"}, "", "wrong-key", ErrInvalidCredentials},
		{"valid bearer token", []string{"secret"}, "Bearer secret", "", nil},
		{"valid x-api-key", []string{"secret"}, "", "secret", nil},
		{"bearer takes precedence over x-api-key", []string{"bearer-key", "xapi-key"}, "Bearer bearer-key", "xapi-key", nil},
		{"bearer invalid does not fall back to x-api-key", []string{"xapi-key"}, "Bearer wrong-token", "xapi-key", ErrInvalidCredentials},
		{"ignores empty keys", []string{"", "valid-key"}, "Bearer valid-key", "", nil},
		{"ignores empty keys empty token fails", []string{"", "valid-key"}, "Bearer ", "", ErrMissingCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auth := NewBearerAuthenticator(tt.keys)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.apiKeyHeader != "" {
				req.Header.Set("X-Api-Key", tt.apiKeyHeader)
			}
			err := auth.Authenticate(req)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.Nil(t, err)
			}
		})
	}
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

func TestAuthMiddleware_ErrorResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		auth       Authenticator
		setup      func(r *http.Request)
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "missing credentials 401",
			auth:       NewBearerAuthenticator([]string{"secret"}),
			setup:      func(_ *http.Request) {},
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
			wantMsg:    "missing or empty credentials",
		},
		{
			name:       "invalid credentials 403",
			auth:       NewBearerAuthenticator([]string{"secret"}),
			setup:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
			wantStatus: http.StatusForbidden,
			wantCode:   "forbidden",
			wantMsg:    "invalid credentials",
		},
		{
			name:       "no keys 503",
			auth:       NewBearerAuthenticator(nil),
			setup:      func(_ *http.Request) {},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "service_unavailable",
			wantMsg:    "no API keys configured on server",
		},
		{
			name:       "unknown error 500",
			auth:       failingAuthenticator{err: errors.New("something unexpected")}, //nolint:err113
			setup:      func(_ *http.Request) {},
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
			wantMsg:    "unexpected authentication failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, _ := newCaptureLogger()
			handler := authMiddleware(tt.auth, okHandler, logger)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			tt.setup(req)
			handler(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
			var resp errorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantCode, resp.Code)
			assert.Equal(t, tt.wantMsg, resp.Message)
		})
	}
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

// --- Audit logging tests ---

func TestAuthMiddleware_AuditLogging(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		auth       Authenticator
		setup      func(r *http.Request)
		wantLevel  slog.Level
		wantReason string
	}{
		{
			name:       "warn on missing credentials",
			auth:       NewBearerAuthenticator([]string{"secret"}),
			setup:      func(_ *http.Request) {},
			wantLevel:  slog.LevelWarn,
			wantReason: "missing credentials",
		},
		{
			name:       "warn on invalid credentials",
			auth:       NewBearerAuthenticator([]string{"secret"}),
			setup:      func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
			wantLevel:  slog.LevelWarn,
			wantReason: "invalid credentials",
		},
		{
			name:       "error on no keys",
			auth:       NewBearerAuthenticator(nil),
			setup:      func(_ *http.Request) {},
			wantLevel:  slog.LevelError,
			wantReason: "no API keys configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, handler := newCaptureLogger()
			mw := authMiddleware(tt.auth, okHandler, logger)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			tt.setup(req)
			mw(rec, req)
			require.NotEmpty(t, handler.records)
			assert.Equal(t, tt.wantLevel, handler.records[0].Level)
			assert.Equal(t, tt.wantReason, handler.records[0].Attrs["reason"])
		})
	}
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

func TestExtractToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		authHeader   string
		apiKeyHeader string
		want         string
	}{
		{"case insensitive bearer", "bearer my-token", "", "my-token"},
		{"bearer upper", "BEARER my-token", "", "my-token"},
		{"bearer with extra spaces", "Bearer   my-token  ", "", "my-token"},
		{"no auth header falls back to x-api-key", "", "api-key-123", "api-key-123"},
		{"no headers", "", "", ""},
		{"non-bearer auth blocks fallback", "Basic dXNlcjpwYXNz", "api-key-123", ""},
		{"bearer empty token blocks fallback", "Bearer ", "api-key-123", ""},
		{"non-bearer auth no api key", "Digest username=test", "", ""},
		{"non-bearer auth disregards valid api key", "Negotiate token", "valid-key", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.apiKeyHeader != "" {
				req.Header.Set("X-Api-Key", tt.apiKeyHeader)
			}
			assert.Equal(t, tt.want, extractToken(req))
		})
	}
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

			var resp errorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tc.wantErr, resp.Code)
			assert.NotEmpty(t, resp.Message)
			assert.NotContains(t, rec.Body.String(), "SDL")
			assert.False(t, strings.Contains(rec.Body.String(), "secret"))
		})
	}
}
