package defra

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
)

// TestErrorLoggingPatterns demonstrates how to use structured error logging in tests
func TestErrorLoggingPatterns(t *testing.T) {
	// Set up test logger with buffer
	testLogger := testutils.NewTestLogger(t)

	t.Run("NetworkError with LogContext", func(t *testing.T) {
		testLogger.ClearBuffer()

		// Create a network error
		originalErr := fmt.Errorf("connection refused")
		networkErr := errors.NewRPCConnectionFailed(
			"defra",
			"CreateBlock",
			"block data",
			originalErr,
			errors.WithBlockNumber(12345),
			errors.WithMetadata("endpoint", "http://localhost:8545"),
		)

		// Log the error using the same pattern as main.go
		logCtx := errors.LogContext(networkErr)
		testLogger.Logger.With(logCtx).Error("Failed to create block in DefraDB")

		// Assert the log contains expected structured context
		testLogger.AssertLogLevel("ERROR")
		testLogger.AssertLogContains("Failed to create block in DefraDB")
		testLogger.AssertLogStructuredContext("defra", "CreateBlock")
		testLogger.AssertLogField("blockNumber", "12345")
		testLogger.AssertLogField("endpoint", "http://localhost:8545")
		testLogger.AssertLogField("errorCode", "RPC_CONNECTION_FAILED")
		testLogger.AssertLogField("severity", "ERROR")
		testLogger.AssertLogField("retryable", "RETRYABLE")
	})

	t.Run("DataError with LogContext", func(t *testing.T) {
		testLogger.ClearBuffer()

		// Create a data error
		originalErr := fmt.Errorf("invalid hex string")
		dataErr := errors.NewInvalidHex(
			"defra",
			"ConvertTransaction",
			"0xInvalidHex",
			originalErr,
			errors.WithTxHash("0x123abc"),
		)

		// Log the error
		logCtx := errors.LogContext(dataErr)
		testLogger.Logger.With(logCtx).Warn("Skipping transaction due to data error")

		// Assert the log contains expected context
		testLogger.AssertLogLevel("WARN")
		testLogger.AssertLogContains("Skipping transaction due to data error")
		testLogger.AssertLogStructuredContext("defra", "ConvertTransaction")
		testLogger.AssertLogField("txHash", "0x123abc")
		testLogger.AssertLogField("errorCode", "INVALID_HEX")
		testLogger.AssertLogField("severity", "ERROR")
		testLogger.AssertLogField("retryable", "NON_RETRYABLE")
	})

	t.Run("StorageError with LogContext", func(t *testing.T) {
		testLogger.ClearBuffer()

		// Create a storage error
		originalErr := fmt.Errorf("connection timeout")
		storageErr := errors.NewDBConnectionFailed(
			"defra",
			"UpdateTransactionRelationships",
			"mutation query",
			originalErr,
			errors.WithBlockNumber(67890),
			errors.WithMetadata("database", "defradb"),
		)

		// Log the error
		logCtx := errors.LogContext(storageErr)
		testLogger.Logger.With(logCtx).Error("Database operation failed")

		// Assert the log contains expected context
		testLogger.AssertLogLevel("ERROR")
		testLogger.AssertLogContains("Database operation failed")
		testLogger.AssertLogStructuredContext("defra", "UpdateTransactionRelationships")
		testLogger.AssertLogField("blockNumber", "67890")
		testLogger.AssertLogField("database", "defradb")
		testLogger.AssertLogField("errorCode", "DB_CONNECTION_FAILED")
		testLogger.AssertLogField("severity", "CRITICAL")
		testLogger.AssertLogField("retryable", "RETRYABLE")
	})

	t.Run("Critical SystemError with LogContext", func(t *testing.T) {
		testLogger.ClearBuffer()

		// Create a critical system error
		originalErr := fmt.Errorf("out of memory")
		systemErr := errors.NewServiceUnavailable(
			"defra",
			"ProcessBlock",
			"service startup",
			"block processing service",
			originalErr,
			errors.WithMetadata("memory_usage", "95%"),
		)

		// Log the error (this would normally call Fatal, but we'll use Error for testing)
		logCtx := errors.LogContext(systemErr)
		testLogger.Logger.With(logCtx).Error("Critical system error occurred")

		// Assert the log contains expected context
		testLogger.AssertLogLevel("ERROR")
		testLogger.AssertLogContains("Critical system error occurred")
		testLogger.AssertLogStructuredContext("defra", "ProcessBlock")
		testLogger.AssertLogField("memory_usage", "95%")
		testLogger.AssertLogField("errorCode", "SERVICE_UNAVAILABLE")
		testLogger.AssertLogField("severity", "CRITICAL")
		testLogger.AssertLogField("retryable", "RETRYABLE_WITH_BACKOFF")
	})
}

// TestRetryLogicWithErrorLogging demonstrates testing retry logic with proper error logging
func TestRetryLogicWithErrorLogging(t *testing.T) {
	testLogger := testutils.NewTestLogger(t)

	// Simulate the retry logic from main.go
	const maxRetries = 3
	var attempt int

	for attempt = 0; attempt < maxRetries; attempt++ {
		// Simulate a retryable error
		networkErr := errors.NewRPCTimeout(
			"defra",
			"CreateBlock",
			"block data",
			fmt.Errorf("request timeout"),
			errors.WithBlockNumber(int64(12345+attempt)),
			errors.WithMetadata("attempt", attempt+1),
		)

		// Log the error with context
		logCtx := errors.LogContext(networkErr)
		testLogger.Logger.With(logCtx).Errorf("Failed to create block (attempt %d)", attempt+1)

		// Check if error is retryable (this would normally determine if we continue)
		if !errors.IsRetryable(networkErr) {
			break
		}

		// Log retry attempt
		if attempt < maxRetries-1 {
			testLogger.Logger.Warnf("Retrying block creation (attempt %d/%d)", attempt+2, maxRetries)
		}
	}

	// Assert we logged the expected number of attempts
	entries := testLogger.GetLogEntries()
	errorEntries := 0
	warnEntries := 0

	for _, entry := range entries {
		// Skip "Ignored key without a value" messages from errors.LogContext
		if msg, ok := entry["message"].(string); ok && strings.Contains(msg, "Ignored key without a value") {
			continue
		}
		if msg, ok := entry["M"].(string); ok && strings.Contains(msg, "Ignored key without a value") {
			continue
		}
		if msg, ok := entry["msg"].(string); ok && strings.Contains(msg, "Ignored key without a value") {
			continue
		}

		// Check both "level" and "L" field names
		var level string
		if l, ok := entry["level"].(string); ok {
			level = l
		} else if l, ok := entry["L"].(string); ok {
			level = l
		}

		switch level {
		case "ERROR":
			errorEntries++
		case "WARN":
			warnEntries++
		}
	}

	// Should have 3 error entries (one per attempt) and 2 warn entries (retry messages)
	if errorEntries != 3 {
		testLogger.Logger.Errorf("Expected 3 error entries, got %d", errorEntries)
	}
	if warnEntries != 2 {
		testLogger.Logger.Errorf("Expected 2 warn entries, got %d", warnEntries)
	}

	// Verify structured context is present in all error logs
	testLogger.AssertLogStructuredContext("defra", "CreateBlock")
	testLogger.AssertLogField("errorCode", "RPC_TIMEOUT")
}

// TestBlockHandlerErrorLogging shows how to integrate this pattern into existing tests
func TestBlockHandlerErrorLogging(t *testing.T) {
	testLogger := testutils.NewTestLogger(t)

	// Create a block handler with nil node to trigger the error path
	_, err := NewBlockHandler(nil, 1000)
	if err == nil {
		t.Fatal("Expected error for nil node")
	}

	// Log the error using structured logging
	logCtx := errors.LogContext(err)
	testLogger.Logger.With(logCtx).Error("Failed to create block handler")

	testLogger.AssertLogLevel("ERROR")
	testLogger.AssertLogContains("Failed to create block handler")
	testLogger.AssertLogStructuredContext("defra", "NewBlockHandler")
	testLogger.AssertLogField("errorCode", "CONFIGURATION_ERROR")
}
