package errors

// AuthError is an error that carries a machine-readable reason string for logging.
type AuthError interface {
	error
	Reason() string
}

type missingCredentialsError struct{}

func (*missingCredentialsError) Error() string  { return "missing credentials" }
func (*missingCredentialsError) Reason() string { return "missing_credentials" }

// ErrMissingCredentials is returned when no authentication credentials are provided.
var ErrMissingCredentials = &missingCredentialsError{}

type invalidCredentialsError struct{}

func (*invalidCredentialsError) Error() string  { return "invalid credentials" }
func (*invalidCredentialsError) Reason() string { return "invalid_credentials" }

// ErrInvalidCredentials is returned when the provided credentials are invalid.
var ErrInvalidCredentials = &invalidCredentialsError{}

type noKeysConfiguredError struct{}

func (*noKeysConfiguredError) Error() string  { return "no API keys configured" }
func (*noKeysConfiguredError) Reason() string { return "no_keys_configured" }

// ErrNoKeysConfigured is returned when no API keys are configured on the server.
var ErrNoKeysConfigured = &noKeysConfiguredError{}

// ErrorResponse is the JSON body returned on authentication failure.
type ErrorResponse struct {
	Code    string `json:"error"`
	Message string `json:"message"`
}
