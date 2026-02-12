package defra

import (
	"errors"
	"os"
	"testing"

	shinzoerrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
)

// TestMain sets up testing environment
func TestMain(m *testing.M) {
	// Initialize logger for all tests
	logger.InitConsoleOnly(true)

	// Run tests
	code := m.Run()

	// Exit with test result code
	os.Exit(code)
}

func TestNewBlockHandler_NilNode(t *testing.T) {
	// Test that nil node returns error
	handler, err := NewBlockHandler(nil, 1000)
	if err == nil {
		t.Error("Expected error for nil node, got nil")
	}
	if handler != nil {
		t.Error("Expected handler to be nil for nil node")
	}
}

func TestNewBlockHandler_DefaultMaxDocs(t *testing.T) {
	// Test that maxDocsPerTxn defaults to 1000 when <= 0
	// Note: This test would require a real DefraDB node, so we skip it
	// The logic is tested by verifying the constructor returns an error for nil node
	t.Skip("Requires embedded DefraDB node")
}

func TestStructuredLogging_ConfigurationError(t *testing.T) {
	// Test structured logging with configuration errors
	testLogger := testutils.NewTestLogger(t)

	// Create a configuration error
	host := "localhost"
	port := 9181
	originalErr := errors.New("connection refused")

	handlerErr := shinzoerrors.NewConfigurationError(
		"defra",
		"NewBlockHandler",
		"failed to create handler",
		"host=localhost, port=9181",
		originalErr,
		shinzoerrors.WithMetadata("host", host),
		shinzoerrors.WithMetadata("port", port),
	)

	// Log with structured context
	logCtx := shinzoerrors.LogContext(handlerErr)
	testLogger.Logger.With(logCtx).Error("Handler creation failed")

	// Verify the structured logging worked
	testLogger.AssertLogLevel("ERROR")
	testLogger.AssertLogContains("Handler creation failed")
	testLogger.AssertLogStructuredContext("defra", "NewBlockHandler")
	testLogger.AssertLogField("host", "localhost")
	testLogger.AssertLogField("port", "9181")
	testLogger.AssertLogField("errorCode", "CONFIGURATION_ERROR")
}

func TestStructuredLogging_NilHandlerError(t *testing.T) {
	// Test structured logging for nil handler scenario
	testLogger := testutils.NewTestLogger(t)

	host := "localhost"
	port := 9181

	nilErr := shinzoerrors.NewConfigurationError(
		"defra",
		"NewBlockHandler",
		"handler is nil after successful creation",
		"host=localhost, port=9181",
		nil,
		shinzoerrors.WithMetadata("host", host),
		shinzoerrors.WithMetadata("port", port),
	)

	// Log with structured context
	logCtx := shinzoerrors.LogContext(nilErr)
	testLogger.Logger.With(logCtx).Error("Nil handler after creation")

	// Verify the structured logging worked
	testLogger.AssertLogLevel("ERROR")
	testLogger.AssertLogContains("Nil handler after creation")
	testLogger.AssertLogStructuredContext("defra", "NewBlockHandler")
	testLogger.AssertLogField("host", "localhost")
	testLogger.AssertLogField("port", "9181")
	testLogger.AssertLogField("errorCode", "CONFIGURATION_ERROR")
}

func TestConvertHexToInt(t *testing.T) {
	// Set up test logger
	testLogger := testutils.NewTestLogger(t)

	tests := []struct {
		name     string
		input    string
		expected int64
	}{
		{"Simple hex", "0x1", 1},
		{"Larger hex", "0xff", 255},
		{"Zero", "0x0", 0},
		{"Large number", "0x1000", 4096},
		{"Block number", "0x1234", 4660},
		{"All characters, lowercase", "0x1234567890abcdef", 1311768467294899695},
		{"All characters, uppercase", "0x1234567890ABCDEF", 1311768467294899695},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := utils.HexToInt(tt.input)
			if err != nil {
				logCtx := shinzoerrors.LogContext(err)
				testLogger.Logger.With(logCtx).Error("ConvertHexToInt failed")
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestConvertHexToInt_UnhappyPaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"Empty string", ""},
		{"Invalid hex", "invalid hex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := utils.HexToInt(tt.input)
			if err == nil {
				t.Error("Expected error, got nil")
			}
			if result != 0 {
				t.Errorf("Expected 0, got %d", result)
			}
		})
	}
}

// Note: Tests for CreateBlockBatch, GetHighestBlockNumber, and other methods
// that require an embedded DefraDB node should be placed in integration tests
// since they require a running DefraDB instance.
