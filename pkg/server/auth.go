package server

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	authErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
)

// Authenticator verifies the authenticity of an HTTP request.
type Authenticator interface {
	Authenticate(r *http.Request) error
}

// NoOpAuthenticator is an Authenticator that always succeeds.
type NoOpAuthenticator struct{}

// Authenticate always returns nil.
func (NoOpAuthenticator) Authenticate(_ *http.Request) error { return nil }

// BearerAuthenticator validates requests using Bearer tokens or X-Api-Key headers.
type BearerAuthenticator struct {
	keys map[string]struct{}
}

// NewBearerAuthenticator creates a BearerAuthenticator from the given keys.
// Empty strings in the keys slice are ignored. An empty keys collection causes
// all Authenticate calls to return ErrNoKeysConfigured (fail-closed).
func NewBearerAuthenticator(keys []string) *BearerAuthenticator {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k != "" {
			m[k] = struct{}{}
		}
	}
	return &BearerAuthenticator{keys: m}
}

// Authenticate validates the request credentials against the configured keys.
func (b *BearerAuthenticator) Authenticate(r *http.Request) error {
	if len(b.keys) == 0 {
		return authErrors.ErrNoKeysConfigured
	}

	token := extractToken(r)
	if token == "" {
		return authErrors.ErrMissingCredentials
	}

	for k := range b.keys {
		if subtle.ConstantTimeCompare([]byte(token), []byte(k)) == 1 {
			return nil
		}
	}

	return authErrors.ErrInvalidCredentials
}

func authMiddleware(auth Authenticator, next http.HandlerFunc, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := auth.Authenticate(r); err != nil {
			reason := reasonFor(err)
			logAttrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("reason", reason),
				slog.String("remote_addr", r.RemoteAddr),
			}

			switch {
			case errors.Is(err, authErrors.ErrMissingCredentials):
				logger.Warn("authentication failed", logAttrs...)
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing or empty credentials")
			case errors.Is(err, authErrors.ErrInvalidCredentials):
				logger.Warn("authentication failed", logAttrs...)
				writeJSONError(w, http.StatusForbidden, "forbidden", "invalid credentials")
			case errors.Is(err, authErrors.ErrNoKeysConfigured):
				logger.Error("authentication failed", logAttrs...)
				writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "no API keys configured on server")
			default:
				logger.Error("authentication failed", logAttrs...)
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "unexpected authentication failure")
			}
			return
		}
		next(w, r)
	}
}

func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth != "" {
		lower := strings.ToLower(auth)
		if strings.HasPrefix(lower, "bearer ") {
			token := strings.TrimSpace(auth[len("bearer "):])
			if token != "" {
				return token
			}
		}
		return ""
	}

	key := strings.TrimSpace(r.Header.Get("X-Api-Key"))
	if key != "" {
		return key
	}

	return ""
}

func reasonFor(err error) string {
	var r authErrors.AuthError
	if errors.As(err, &r) {
		return r.Reason()
	}
	return "unknown"
}
