package errors

import "errors"

// Reason strings used in structured logging for auth errors.
const (
	MissingCredentialsReason = "missing_credentials"
	InvalidCredentialsReason = "invalid_credentials" //nolint:gosec // G101 false positive: reason string, not a credential
	NoKeysConfiguredReason   = "no_keys_configured"
	UnknownReason            = "unknown"
)

var (
	// ErrMissingCredentials is returned when no authentication credentials are provided.
	ErrMissingCredentials = errors.New("missing credentials")
	// ErrInvalidCredentials is returned when the provided credentials are invalid.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrNoKeysConfigured is returned when no API keys are configured on the server.
	ErrNoKeysConfigured = errors.New("no API keys configured")
)

// ReasonFor returns a machine-readable reason string for known auth errors, or "unknown".
func ReasonFor(err error) string {
	switch {
	case errors.Is(err, ErrMissingCredentials):
		return MissingCredentialsReason
	case errors.Is(err, ErrInvalidCredentials):
		return InvalidCredentialsReason
	case errors.Is(err, ErrNoKeysConfigured):
		return NoKeysConfiguredReason
	default:
		return UnknownReason
	}
}
