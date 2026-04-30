package errors

import (
	"fmt"
	"time"
)

// Severity levels for error classification
type Severity int

const (
	// Info - Notable events for monitoring/debugging (non-blocking)
	Info Severity = iota
	// Warning - Issue detected but operation succeeded with degraded quality
	Warning
	// Error - Operation failed but system can continue with next item
	Error
	// Critical - System cannot continue, requires immediate attention
	Critical
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "INFO"
	case Warning:
		return "WARNING"
	case Error:
		return "ERROR"
	case Critical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// RetryBehavior indicates how the system should handle retry logic
type RetryBehavior int

const (
	// NonRetryable - Permanent errors (invalid data format, auth failures)
	NonRetryable RetryBehavior = iota
	// Retryable - Can be retried immediately (network timeouts, temp issues)
	Retryable
	// RetryableWithBackoff - Requires exponential backoff (rate limiting, resource exhaustion)
	RetryableWithBackoff
)

func (r RetryBehavior) String() string {
	switch r {
	case NonRetryable:
		return "NON_RETRYABLE"
	case Retryable:
		return "RETRYABLE"
	case RetryableWithBackoff:
		return "RETRYABLE_WITH_BACKOFF"
	default:
		return "UNKNOWN"
	}
}

// ErrorContext provides structured context for debugging and monitoring
type ErrorContext struct {
	Component   string         `json:"component"` // Which service/component (e.g., "defra", "rpc", "converter")
	Operation   string         `json:"operation"` // What operation (e.g., "CreateBlock", "GetTransaction")
	BlockNumber *int64         `json:"blockNumber,omitempty"`
	TxHash      *string        `json:"txHash,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// IndexerError is the main error interface for the blockchain indexing system
//
// Usage Guidelines:
// - Use NetworkError for RPC/HTTP communication issues (often retryable)
// - Use DataError for parsing/validation issues (usually non-retryable)
// - Use StorageError for database operations (sometimes retryable)
// - Use SystemError for critical system-level failures
//
// Error Handling Patterns:
// - Check error type with errors.As() or type assertions
// - Use Retryable() to implement retry logic
// - Use Severity() for logging level and alerting
// - Use Code() for metrics and monitoring
// - Use Context() for debugging and tracing
type IndexerError interface {
	error

	// Code returns a standardized error code for monitoring/metrics
	Code() string

	// Severity returns the severity level for logging and alerting
	Severity() Severity

	// Retryable returns the retry behavior recommendation
	Retryable() RetryBehavior

	// Context returns structured context for debugging
	Context() ErrorContext

	// Unwrap returns the underlying error for error wrapping
	Unwrap() error
}

// baseError provides common functionality for all error types
type baseError struct {
	code       string
	message    string
	severity   Severity
	retryable  RetryBehavior
	context    ErrorContext
	input_data string
	underlying error
}

func (e *baseError) Error() string {
	if e.underlying != nil {
		return fmt.Sprintf("[%s] %s: %v", e.code, e.message, e.underlying)
	}
	return fmt.Sprintf("[%s] %s", e.code, e.message)
}

func (e *baseError) Code() string             { return e.code }
func (e *baseError) Severity() Severity       { return e.severity }
func (e *baseError) Retryable() RetryBehavior { return e.retryable }
func (e *baseError) Context() ErrorContext    { return e.context }
func (e *baseError) Unwrap() error            { return e.underlying }

// NetworkError represents RPC/HTTP communication failures
//
// Usage: Use for Ethereum RPC timeouts, connection failures, HTTP errors
// Characteristics: Usually retryable, can indicate infrastructure issues
// Examples: RPC timeout, connection refused, DNS resolution failure
type NetworkError struct {
	*baseError
}

// DataError represents data parsing and validation failures
//
// Usage: Use for invalid hex strings, malformed JSON, schema validation failures
// Characteristics: Usually non-retryable, indicates data quality issues
// Examples: Invalid block format, unparseable transaction hash, missing required fields
type DataError struct {
	*baseError
}

// StorageError represents database operation failures
//
// Usage: Use for DefraDB connection issues, query failures, constraint violations
// Characteristics: Sometimes retryable depending on root cause
// Examples: Database connection timeout, constraint violation, disk full
type StorageError struct {
	*baseError
}

// SystemError represents critical system-level failures
//
// Usage: Use for configuration errors, resource exhaustion, critical service failures
// Characteristics: Usually requires immediate attention, may need manual intervention
// Examples: Invalid configuration, out of memory, service unavailable
type SystemError struct {
	*baseError
}
