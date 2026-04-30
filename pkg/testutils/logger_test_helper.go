package testutils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TestLoggerSetup holds the test logger and buffer for log capture
type TestLoggerSetup struct {
	Logger *zap.SugaredLogger
	Buffer *bytes.Buffer
	t      *testing.T
}

// NewTestLogger creates a logger that writes to a buffer for testing
func NewTestLogger(t *testing.T) *TestLoggerSetup {
	buffer := &bytes.Buffer{}

	// Create encoder config for consistent output
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.MessageKey = "message"
	encoderConfig.LevelKey = "level"
	encoderConfig.TimeKey = "timestamp"

	// Create core that writes to buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig), // Use JSON for easier parsing in tests
		zapcore.AddSync(buffer),
		zapcore.DebugLevel, // Capture all levels
	)

	logger := zap.New(core).Sugar()

	return &TestLoggerSetup{
		Logger: logger,
		Buffer: buffer,
		t:      t,
	}
}

// GetLogOutput returns the current log output as a string
func (tls *TestLoggerSetup) GetLogOutput() string {
	return tls.Buffer.String()
}

// ClearBuffer clears the log buffer
func (tls *TestLoggerSetup) ClearBuffer() {
	tls.Buffer.Reset()
}

// AssertLogContains checks if the log output contains the expected message
func (tls *TestLoggerSetup) AssertLogContains(expectedMessage string) {
	tls.t.Helper()
	output := tls.GetLogOutput()
	if !strings.Contains(output, expectedMessage) {
		tls.t.Errorf("Expected log to contain '%s', but got:\n%s", expectedMessage, output)
	}
}

// AssertLogLevel checks if a log entry with the specified level exists
func (tls *TestLoggerSetup) AssertLogLevel(level string) {
	tls.t.Helper()
	output := tls.GetLogOutput()
	// Check for both full "level" and abbreviated "L" field names
	if strings.Contains(output, `"level":"`+level+`"`) {
		return
	}
	tls.t.Errorf("Expected log to contain level '%s', but got:\n%s", level, output)
}

// AssertLogField checks if a log entry contains a specific field with value
// This parses the JSON log entries and checks for the field in any format
func (tls *TestLoggerSetup) AssertLogField(fieldName, expectedValue string) {
	tls.t.Helper()

	// Parse all log entries as JSON
	entries := tls.GetLogEntries()

	// Check each log entry for the field
	for _, entry := range entries {
		if tls.hasFieldWithValue(entry, fieldName, expectedValue) {
			return // Found it!
		}
	}

	tls.t.Errorf("Expected log to contain field '%s' with value '%s', but got:\n%s",
		fieldName, expectedValue, tls.GetLogOutput())
}

// hasFieldWithValue checks if a JSON object contains the field with expected value
// It checks both camelCase and snake_case variants, and also nested objects
func (tls *TestLoggerSetup) hasFieldWithValue(entry map[string]any, fieldName, expectedValue string) bool {
	// Check top-level fields first
	if tls.checkFieldInObject(entry, fieldName, expectedValue) {
		return true
	}

	// Check nested objects (like "ignored" from zap structured logging)
	for _, value := range entry {
		if nestedObj, ok := value.(map[string]any); ok {
			if tls.checkFieldInObject(nestedObj, fieldName, expectedValue) {
				return true
			}
		}
	}

	return false
}

// checkFieldInObject checks a single object for the field with both naming conventions
func (tls *TestLoggerSetup) checkFieldInObject(obj map[string]any, fieldName, expectedValue string) bool {
	// Try the field name as-is
	if value, exists := obj[fieldName]; exists {
		if fmt.Sprintf("%v", value) == expectedValue {
			return true
		}
	}

	// Try snake_case version
	snakeCase := tls.camelToSnake(fieldName)
	if value, exists := obj[snakeCase]; exists {
		if fmt.Sprintf("%v", value) == expectedValue {
			return true
		}
	}

	return false
}

// camelToSnake converts camelCase to snake_case
func (tls *TestLoggerSetup) camelToSnake(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('_')
		}
		result.WriteRune(r | 32) // Convert to lowercase
	}
	return result.String()
}

// AssertLogStructuredContext checks if the log contains structured context from errors.LogContext
func (tls *TestLoggerSetup) AssertLogStructuredContext(expectedComponent, expectedOperation string) {
	tls.t.Helper()
	output := tls.GetLogOutput()

	// Check for component
	if !strings.Contains(output, `"component":"`+expectedComponent+`"`) {
		tls.t.Errorf("Expected log to contain component '%s', but got:\n%s", expectedComponent, output)
	}

	// Check for operation
	if !strings.Contains(output, `"operation":"`+expectedOperation+`"`) {
		tls.t.Errorf("Expected log to contain operation '%s', but got:\n%s", expectedOperation, output)
	}
}

// GetLogEntries parses the log output and returns individual log entries
func (tls *TestLoggerSetup) GetLogEntries() []map[string]any {
	output := tls.GetLogOutput()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	var entries []map[string]any
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			tls.t.Logf("Failed to parse log line: %s, error: %v", line, err)
			continue
		}
		entries = append(entries, entry)
	}

	return entries
}

// AssertLogCount checks if the expected number of log entries were created
func (tls *TestLoggerSetup) AssertLogCount(expectedCount int) {
	tls.t.Helper()
	entries := tls.GetLogEntries()
	if len(entries) != expectedCount {
		tls.t.Errorf("Expected %d log entries, but got %d:\n%s", expectedCount, len(entries), tls.GetLogOutput())
	}
}
