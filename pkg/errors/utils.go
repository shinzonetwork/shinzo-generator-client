package errors

import (
	"errors"
	"io"
	"maps"
	"net"
	"strings"
	"syscall"
	"time"
)

// Error substring constants for matching upstream errors that don't use typed errors.
const (
	ErrStrNotFound                = "not found"
	ErrStrDoesNotExist            = "does not exist"
	ErrStrAlreadyExists           = "already exists"
	ErrStrCollectionAlreadyExists = "collection already exists"
	ErrStrTransactionConflict     = "transaction conflict"
	ErrStrTxTypeNotSupported      = "transaction type not supported"
	ErrStrInvalidTxType           = "invalid transaction type"
	DefaultRetryDelay             = 30 * time.Second
)

// IsErrNotFound checks if an error message indicates a "not found" condition.
func IsErrNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, ErrStrNotFound) || strings.Contains(msg, ErrStrDoesNotExist)
}

// IsErrAlreadyExists checks if an error message indicates a resource already exists.
func IsErrAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrStrAlreadyExists)
}

// IsErrTransactionConflict checks if an error message indicates a transaction conflict.
func IsErrTransactionConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrStrTransactionConflict)
}

// IsErrUnsupportedTxType checks if an error indicates an unsupported transaction type.
func IsErrUnsupportedTxType(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, ErrStrTxTypeNotSupported) || strings.Contains(msg, ErrStrInvalidTxType)
}

// IsRetryable checks if an error can be retried.
func IsRetryable(err error) bool {
	if indexerErr, ok := errors.AsType[IndexerError](err); ok {
		return indexerErr.Retryable() != NonRetryable
	}
	return false
}

// IsRetryableWithBackoff checks if error requires exponential backoff.
func IsRetryableWithBackoff(err error) bool {
	if indexerErr, ok := errors.AsType[IndexerError](err); ok {
		return indexerErr.Retryable() == RetryableWithBackoff
	}
	return false
}

// IsCritical checks if error is critical severity.
func IsCritical(err error) bool {
	if indexerErr, ok := errors.AsType[IndexerError](err); ok {
		return indexerErr.Severity() == Critical
	}
	return false
}

// IsNetworkError checks if error is network-related.
func IsNetworkError(err error) bool {
	var networkErr *NetworkError
	return errors.As(err, &networkErr)
}

// transientHTTPStatuses are the HTTP statuses that mean the endpoint is
// temporarily unavailable. go-ethereum's rpc.HTTPError renders as
// "<code> <text>: <body>"; the Cosmos client renders "status <code>".
var transientHTTPStatuses = []string{ //nolint:gochecknoglobals // read-only match table
	"429 Too Many Requests", "502 Bad Gateway", "503 Service Unavailable", "504 Gateway Timeout",
	"status 429", "status 502", "status 503", "status 504",
}

// IsTransientRPCError reports whether err means the RPC endpoint is
// temporarily unreachable (connection refused or reset, DNS failure, timeout,
// unexpected EOF, HTTP 429/502/503/504) rather than a problem with the
// requested data. Callers retry such errors until the endpoint returns
// instead of giving up on a block: a node restart must never skip heights.
func IsTransientRPCError(err error) bool {
	if err == nil {
		return false
	}
	if IsNetworkError(err) {
		return true
	}
	for _, target := range []error{
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE,
		syscall.EHOSTUNREACH, syscall.ENETUNREACH, io.EOF, io.ErrUnexpectedEOF,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	// Dial, DNS and timeout failures (*net.OpError, *net.DNSError, *url.Error).
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	msg := err.Error()
	for _, status := range transientHTTPStatuses {
		if strings.Contains(msg, status) {
			return true
		}
	}
	return false
}

// IsDataError checks if error is data-related.
func IsDataError(err error) bool {
	var dataErr *DataError
	return errors.As(err, &dataErr)
}

// IsStorageError checks if error is storage-related.
func IsStorageError(err error) bool {
	var storageErr *StorageError
	return errors.As(err, &storageErr)
}

// GetErrorCode extracts error code from IndexerError, returns "UNKNOWN" for other errors.
func GetErrorCode(err error) string {
	if indexerErr, ok := errors.AsType[IndexerError](err); ok {
		return indexerErr.Code()
	}
	return "UNKNOWN"
}

// GetRetryDelay calculates appropriate retry delay based on error type and attempt count.
func GetRetryDelay(err error, attemptCount int) time.Duration {
	if !IsRetryable(err) {
		return 0
	}

	baseDelay := time.Second

	if IsRetryableWithBackoff(err) {
		// Exponential backoff: 1s, 2s, 4s, 8s, 16s, max 30s.
		delay := baseDelay
		for i := 0; i < attemptCount && delay < 30*time.Second; i++ {
			delay *= 2
		}
		delay = min(delay, DefaultRetryDelay) //nolint:mnd
		return delay
	}

	// Simple retry - fixed delay.
	return baseDelay
}

// WrapError wraps a standard error with IndexerError context.
func WrapError(err error, component, operation string) IndexerError {
	if err == nil {
		return nil
	}

	// If already an IndexerError, return as-is.
	if indexerErr, ok := errors.AsType[IndexerError](err); ok {
		return indexerErr
	}

	// Create generic error wrapper.
	return &SystemError{
		baseError: newBaseError("WRAPPED_ERROR", "Wrapped standard error", Error, NonRetryable,
			component, operation, "", err),
	}
}

// LogContext extracts structured logging context from error.
func LogContext(err error) map[string]any {
	if indexerErr, ok := errors.AsType[IndexerError](err); ok {
		ctx := indexerErr.Context()
		logCtx := map[string]any{
			"error_code": indexerErr.Code(),
			"severity":   indexerErr.Severity().String(),
			"retryable":  indexerErr.Retryable().String(),
			"component":  ctx.Component,
			"operation":  ctx.Operation,
			"timestamp":  ctx.Timestamp,
		}

		if ctx.BlockNumber != nil {
			logCtx["block_number"] = *ctx.BlockNumber
		}

		if ctx.TxHash != nil {
			logCtx["tx_hash"] = *ctx.TxHash
		}

		maps.Copy(logCtx, ctx.Metadata)

		return logCtx
	}

	return map[string]any{
		"error":      err.Error(),
		"error_type": "standard_error",
	}
}
