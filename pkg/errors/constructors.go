package errors

import (
	"time"
)

// Error code constants for monitoring and metrics.
const (
	// CodeRPCTimeout is for errors related to RPC request timeouts, such as when a request to an RPC endpoint exceeds the configured timeout duration.
	CodeRPCTimeout = "RPC_TIMEOUT"
	// CodeRPCConnectionFailed is for errors related to failures when connecting to an RPC endpoint, such as connection refused or network unreachable.
	CodeRPCConnectionFailed = "RPC_CONNECTION_FAILED"
	// CodeHTTPError is for errors related to HTTP connection failures, such as when connecting to a third-party API or service over HTTP.
	CodeHTTPError = "HTTP_ERROR"
	// CodeRateLimited is for errors indicating that the system has been rate limited by an external service, such as a third-party API or database.
	CodeRateLimited = "RATE_LIMITED"

	// CodeInvalidHex is for errors indicating that a hexadecimal string input was invalid or malformed.
	CodeInvalidHex = "INVALID_HEX"
	// CodeInvalidBlockFormat is for errors indicating that block data is malformed or does not conform to expected format.
	CodeInvalidBlockFormat = "INVALID_BLOCK_FORMAT"
	// CodeInvalidInputFormat is for errors indicating that input data is malformed or does not conform to expected format.
	CodeInvalidInputFormat = "INVALID_INPUT_FORMAT"
	// CodeMissingRequiredField is for errors indicating that a required field was missing from input data.
	CodeMissingRequiredField = "MISSING_REQUIRED_FIELD"
	// CodeParsingFailed is for general parsing failures that don't fit more specific categories.
	CodeParsingFailed = "PARSING_FAILED"

	// CodeDBConnectionFailed is for errors related to failures when connecting to the database.
	CodeDBConnectionFailed = "DB_CONNECTION_FAILED"
	// CodeQueryFailed is for errors that occur during database query execution, such as syntax errors or constraint violations.
	CodeQueryFailed = "QUERY_FAILED"
	// CodeConstraintViolation is for errors indicating a database constraint violation, such as unique key or foreign key violations.
	CodeConstraintViolation = "CONSTRAINT_VIOLATION"
	// CodeDocumentNotFound is for errors indicating that a required document was not found in the database.
	CodeDocumentNotFound = "DOCUMENT_NOT_FOUND"

	// CodeInvalidConfig is for errors related to invalid system configuration that prevents startup or operation.
	CodeInvalidConfig = "INVALID_CONFIG"
	// CodeResourceExhausted is for errors indicating that a critical resource (e.g. memory, disk space) has been exhausted.
	CodeResourceExhausted = "RESOURCE_EXHAUSTED"
	// CodeServiceUnavailable is for errors indicating that a critical external service is unavailable, such as a database or third-party API.
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	// CodeConfigurationError is for errors related to misconfiguration of the system that prevents it from functioning correctly (e.g. invalid environment variables, missing config files).
	CodeConfigurationError = "CONFIGURATION_ERROR"
)

// NetworkError constructors.

// NewHTTPConnectionFailed creates an error for HTTP connection failures.
func NewHTTPConnectionFailed(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeHTTPError, "Failed to connect to HTTP endpoint", Error, Retryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewRPCTimeout creates an error for RPC timeout scenarios.
func NewRPCTimeout(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeRPCTimeout, "RPC request timed out", Error, Retryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewRPCConnectionFailed creates an error for RPC connection failures.
func NewRPCConnectionFailed(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeRPCConnectionFailed, "Failed to connect to RPC endpoint", Error, Retryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewRateLimited creates an error for rate limiting scenarios.
func NewRateLimited(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeRateLimited, "Rate limited by external service", Warning, RetryableWithBackoff,
			component, operation, inputData, underlying, ctx...),
	}
}

// DataError constructors.

// NewInvalidHex creates an error for invalid hexadecimal string inputs.
func NewInvalidHex(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Invalid hexadecimal format: " + inputData
	return &DataError{
		baseError: newBaseError(CodeInvalidHex, message, Error, NonRetryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewInvalidBlockFormat creates an error for malformed block data.
func NewInvalidBlockFormat(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &DataError{
		baseError: newBaseError(CodeInvalidBlockFormat, "Block data format is invalid", Error, NonRetryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewInvalidInputFormat creates an error for malformed input data.
func NewInvalidInputFormat(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &DataError{
		baseError: newBaseError(CodeInvalidInputFormat, "Input data format is invalid", Error, NonRetryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewParsingFailed creates an error for general parsing failures.
func NewParsingFailed(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Failed to parse " + inputData
	return &DataError{
		baseError: newBaseError(CodeParsingFailed, message, Error, NonRetryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// StorageError constructors.

// NewDBConnectionFailed creates an error for database connection failures.
func NewDBConnectionFailed(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &StorageError{
		baseError: newBaseError(CodeDBConnectionFailed, "Failed to connect to database", Critical, Retryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewQueryFailed creates an error for database query failures.
func NewQueryFailed(component, operation, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	return &StorageError{
		baseError: newBaseError(CodeQueryFailed, "Database query execution failed", Error, Retryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewDocumentNotFound creates an error when a required document is not found.
func NewDocumentNotFound(component, operation, documentType, inputData string, ctx ...ContextOption) IndexerError {
	message := "Required " + documentType + " document not found"
	return &StorageError{
		baseError: newBaseError(CodeDocumentNotFound, message, Warning, NonRetryable,
			component, operation, inputData, nil, ctx...),
	}
}

// SystemError constructors.

// NewConfigurationError creates an error for system configuration issues.
func NewConfigurationError(component, operation, issue, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Configuration error: " + issue
	return &SystemError{
		baseError: newBaseError(CodeConfigurationError, message, Critical, NonRetryable,
			component, operation, inputData, underlying, ctx...),
	}
}

// NewServiceUnavailable creates an error when a critical service is unavailable.
func NewServiceUnavailable(component, operation, service, inputData string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Service unavailable: " + service
	return &SystemError{
		baseError: newBaseError(CodeServiceUnavailable, message, Critical, RetryableWithBackoff,
			component, operation, inputData, underlying, ctx...),
	}
}

// Helper functions.

// newBaseError creates a baseError with consistent context.
func newBaseError(code, message string, severity Severity, retryable RetryBehavior,
	component, operation, inputData string, underlying error, contextOptions ...ContextOption,
) *baseError {
	context := ErrorContext{
		Component: component,
		Operation: operation,
		Timestamp: time.Now(),
		Metadata:  make(map[string]any),
	}

	// Apply context options.
	for _, opt := range contextOptions {
		opt(&context)
	}

	return &baseError{
		code:       code,
		message:    message,
		severity:   severity,
		retryable:  retryable,
		context:    context,
		inputData:  inputData,
		underlying: underlying,
	}
}

// ContextOption allows flexible context configuration.
type ContextOption func(*ErrorContext)

// WithBlockNumber adds block number to error context.
func WithBlockNumber(blockNumber int64) ContextOption {
	return func(ctx *ErrorContext) {
		ctx.BlockNumber = &blockNumber
	}
}

// WithTxHash adds transaction hash to error context.
func WithTxHash(txHash string) ContextOption {
	return func(ctx *ErrorContext) {
		ctx.TxHash = &txHash
	}
}

// WithMetadata adds arbitrary metadata to error context.
func WithMetadata(key string, value any) ContextOption {
	return func(ctx *ErrorContext) {
		if ctx.Metadata == nil {
			ctx.Metadata = make(map[string]any)
		}
		ctx.Metadata[key] = value
	}
}
