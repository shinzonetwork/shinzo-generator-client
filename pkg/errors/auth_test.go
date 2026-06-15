package errors

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestAuthErrorTypes_ImplementAuthError(t *testing.T) {
	t.Parallel()
	var _ AuthError = (*missingCredentialsError)(nil)
	var _ AuthError = (*invalidCredentialsError)(nil)
	var _ AuthError = (*noKeysConfiguredError)(nil)
}

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

func TestAuthError_ReasonStrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      AuthError
		expected string
	}{
		{"MissingCredentials", ErrMissingCredentials, "missing_credentials"},
		{"InvalidCredentials", ErrInvalidCredentials, "invalid_credentials"},
		{"NoKeysConfigured", ErrNoKeysConfigured, "no_keys_configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Reason(); got != tt.expected {
				t.Errorf("Reason() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAuthError_ErrorsIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		sentinel error
	}{
		{"MissingCredentials", ErrMissingCredentials, ErrMissingCredentials},
		{"InvalidCredentials", ErrInvalidCredentials, ErrInvalidCredentials},
		{"NoKeysConfigured", ErrNoKeysConfigured, ErrNoKeysConfigured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !errors.Is(tt.err, tt.sentinel) {
				t.Errorf("errors.Is(%v, %v) = false, want true", tt.err, tt.sentinel)
			}
		})
	}
}

func TestAuthError_ErrorsIs_CrossCheck(t *testing.T) {
	t.Parallel()
	if errors.Is(ErrMissingCredentials, ErrInvalidCredentials) {
		t.Error("ErrMissingCredentials should not match ErrInvalidCredentials")
	}
	if errors.Is(ErrMissingCredentials, ErrNoKeysConfigured) {
		t.Error("ErrMissingCredentials should not match ErrNoKeysConfigured")
	}
	if errors.Is(ErrInvalidCredentials, ErrNoKeysConfigured) {
		t.Error("ErrInvalidCredentials should not match ErrNoKeysConfigured")
	}
}

func TestAuthError_ErrorsAsAuthError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"MissingCredentials", ErrMissingCredentials},
		{"InvalidCredentials", ErrInvalidCredentials},
		{"NoKeysConfigured", ErrNoKeysConfigured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var authErr AuthError
			if !errors.As(tt.err, &authErr) {
				t.Fatalf("errors.As(%v, &AuthError) = false, want true", tt.err)
			}
			if authErr.Reason() == "" {
				t.Error("Reason() should not be empty")
			}
		})
	}
}

func TestAuthError_ErrorsAs_NonAuthError(t *testing.T) {
	t.Parallel()
	plainErr := errors.New("plain error") //nolint:err113
	var authErr AuthError
	if errors.As(plainErr, &authErr) {
		t.Error("plain error should not match AuthError interface")
	}
}

func TestErrorResponse_JSONSerialization(t *testing.T) {
	t.Parallel()
	resp := ErrorResponse{Code: "unauthorized", Message: "missing or empty credentials"}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if decoded["error"] != "unauthorized" {
		t.Errorf("json[error] = %q, want %q", decoded["error"], "unauthorized")
	}
	if decoded["message"] != "missing or empty credentials" {
		t.Errorf("json[message] = %q, want %q", decoded["message"], "missing or empty credentials")
	}
}
