package errors

import (
	"errors"
	"fmt"
	"testing"
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

func TestAuthError_ErrorsIs_Wrapped(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("context: %w", ErrInvalidCredentials)
	if !errors.Is(wrapped, ErrInvalidCredentials) {
		t.Error("wrapped ErrInvalidCredentials should match sentinel via errors.Is")
	}
}

func TestReasonFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"MissingCredentials", ErrMissingCredentials, MissingCredentialsReason},
		{"InvalidCredentials", ErrInvalidCredentials, InvalidCredentialsReason},
		{"NoKeysConfigured", ErrNoKeysConfigured, NoKeysConfiguredReason},
		{"Unknown", errors.New("boom"), UnknownReason},
		{"WrappedInvalid", fmt.Errorf("context: %w", ErrInvalidCredentials), InvalidCredentialsReason},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ReasonFor(tt.err); got != tt.expected {
				t.Errorf("ReasonFor() = %q, want %q", got, tt.expected)
			}
		})
	}
}
