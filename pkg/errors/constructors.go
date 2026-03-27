package errors

import (
	"time"
)

// Error code constants for monitoring and metrics
const (
	// Network error codes
	CodeRPCTimeout          = "RPC_TIMEOUT"
	CodeRPCConnectionFailed = "RPC_CONNECTION_FAILED"
	CodeHTTPError           = "HTTP_ERROR"
	CodeRateLimited         = "RATE_LIMITED"

	// Data error codes
	CodeInvalidHex           = "INVALID_HEX"
	CodeInvalidBlockFormat   = "INVALID_BLOCK_FORMAT"
	CodeInvalidInputFormat   = "INVALID_INPUT_FORMAT"
	CodeMissingRequiredField = "MISSING_REQUIRED_FIELD"
	CodeParsingFailed        = "PARSING_FAILED"

	// Storage error codes
	CodeDBConnectionFailed  = "DB_CONNECTION_FAILED"
	CodeQueryFailed         = "QUERY_FAILED"
	CodeConstraintViolation = "CONSTRAINT_VIOLATION"
	CodeDocumentNotFound    = "DOCUMENT_NOT_FOUND"

	// System error codes
	CodeInvalidConfig      = "INVALID_CONFIG"
	CodeResourceExhausted  = "RESOURCE_EXHAUSTED"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	CodeConfigurationError = "CONFIGURATION_ERROR"
)

// NetworkError constructors

func NewHTTPConnectionFailed(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeHTTPError, "Failed to connect to HTTP endpoint", Error, Retryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewRPCTimeout creates an error for RPC timeout scenarios
func NewRPCTimeout(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeRPCTimeout, "RPC request timed out", Error, Retryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewRPCConnectionFailed creates an error for RPC connection failures
func NewRPCConnectionFailed(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeRPCConnectionFailed, "Failed to connect to RPC endpoint", Error, Retryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewRateLimited creates an error for rate limiting scenarios
func NewRateLimited(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &NetworkError{
		baseError: newBaseError(CodeRateLimited, "Rate limited by external service", Warning, RetryableWithBackoff,
			component, operation, input_data, underlying, ctx...),
	}
}

// DataError constructors

// NewInvalidHex creates an error for invalid hexadecimal string inputs
func NewInvalidHex(component, operation, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Invalid hexadecimal format: " + input_data
	return &DataError{
		baseError: newBaseError(CodeInvalidHex, message, Error, NonRetryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewInvalidBlockFormat creates an error for malformed block data
func NewInvalidBlockFormat(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &DataError{
		baseError: newBaseError(CodeInvalidBlockFormat, "Block data format is invalid", Error, NonRetryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewInvalidInputFormat creates an error for malformed input data
func NewInvalidInputFormat(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &DataError{
		baseError: newBaseError(CodeInvalidInputFormat, "Input data format is invalid", Error, NonRetryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewParsingFailed creates an error for general parsing failures
func NewParsingFailed(component, operation, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Failed to parse " + input_data
	return &DataError{
		baseError: newBaseError(CodeParsingFailed, message, Error, NonRetryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// StorageError constructors

// NewDBConnectionFailed creates an error for database connection failures
func NewDBConnectionFailed(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &StorageError{
		baseError: newBaseError(CodeDBConnectionFailed, "Failed to connect to database", Critical, Retryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewQueryFailed creates an error for database query failures
func NewQueryFailed(component, operation string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	return &StorageError{
		baseError: newBaseError(CodeQueryFailed, "Database query execution failed", Error, Retryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewDocumentNotFound creates an error when a required document is not found
func NewDocumentNotFound(component, operation, documentType string, input_data string, ctx ...ContextOption) IndexerError {
	message := "Required " + documentType + " document not found"
	return &StorageError{
		baseError: newBaseError(CodeDocumentNotFound, message, Warning, NonRetryable,
			component, operation, input_data, nil, ctx...),
	}
}

// SystemError constructors

// NewConfigurationError creates an error for system configuration issues
func NewConfigurationError(component, operation, issue string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Configuration error: " + issue
	return &SystemError{
		baseError: newBaseError(CodeConfigurationError, message, Critical, NonRetryable,
			component, operation, input_data, underlying, ctx...),
	}
}

// NewServiceUnavailable creates an error when a critical service is unavailable
func NewServiceUnavailable(component, operation, service string, input_data string, underlying error, ctx ...ContextOption) IndexerError {
	message := "Service unavailable: " + service
	return &SystemError{
		baseError: newBaseError(CodeServiceUnavailable, message, Critical, RetryableWithBackoff,
			component, operation, input_data, underlying, ctx...),
	}
}

// Helper functions

// newBaseError creates a baseError with consistent context
func newBaseError(code, message string, severity Severity, retryable RetryBehavior,
	component, operation string, input_data string, underlying error, contextOptions ...ContextOption) *baseError {

	context := ErrorContext{
		Component: component,
		Operation: operation,
		Timestamp: time.Now(),
		Metadata:  make(map[string]any),
	}

	// Apply context options
	for _, opt := range contextOptions {
		opt(&context)
	}

	return &baseError{
		code:       code,
		message:    message,
		severity:   severity,
		retryable:  retryable,
		context:    context,
		input_data: input_data,
		underlying: underlying,
	}
}

// ContextOption allows flexible context configuration
type ContextOption func(*ErrorContext)

// WithBlockNumber adds block number to error context
func WithBlockNumber(blockNumber int64) ContextOption {
	return func(ctx *ErrorContext) {
		ctx.BlockNumber = &blockNumber
	}
}

// WithTxHash adds transaction hash to error context
func WithTxHash(txHash string) ContextOption {
	return func(ctx *ErrorContext) {
		ctx.TxHash = &txHash
	}
}

// WithMetadata adds arbitrary metadata to error context
func WithMetadata(key string, value any) ContextOption {
	return func(ctx *ErrorContext) {
		if ctx.Metadata == nil {
			ctx.Metadata = make(map[string]any)
		}
		ctx.Metadata[key] = value
	}
}
