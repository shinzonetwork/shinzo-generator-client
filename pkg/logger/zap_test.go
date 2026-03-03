package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestInitConsoleOnly_Development(t *testing.T) {
	InitConsoleOnly(true)
	if Sugar == nil {
		t.Fatal("Sugar should not be nil after InitConsoleOnly(true)")
	}
}

func TestInitConsoleOnly_Production(t *testing.T) {
	InitConsoleOnly(false)
	if Sugar == nil {
		t.Fatal("Sugar should not be nil after InitConsoleOnly(false)")
	}
}

func TestInitWithFiles(t *testing.T) {
	tempDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tempDir)

	InitWithFiles(true)
	if Sugar == nil {
		t.Fatal("Sugar should not be nil after InitWithFiles")
	}

	// Write some logs to trigger file creation
	Sugar.Info("test info")
	Sugar.Error("test error")
	Sugar.Sync()

	// Verify log files were created
	logFile := filepath.Join(tempDir, "logs", "logfile.log")
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("logfile.log should have been created")
	}

	errorFile := filepath.Join(tempDir, "logs", "errorfile.log")
	if _, err := os.Stat(errorFile); os.IsNotExist(err) {
		t.Error("errorfile.log should have been created")
	}
}

func TestInitWithFiles_Production(t *testing.T) {
	tempDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tempDir)

	InitWithFiles(false)
	if Sugar == nil {
		t.Fatal("Sugar should not be nil after InitWithFiles(false)")
	}
}

func TestInit(t *testing.T) {
	tempDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tempDir)

	Init(true)
	if Sugar == nil {
		t.Fatal("Sugar should not be nil after Init(true)")
	}

	Init(false)
	if Sugar == nil {
		t.Fatal("Sugar should not be nil after Init(false)")
	}
}

func TestCustomLevelEncoder_TestLevel(t *testing.T) {
	enc := &testArrayEncoder{}
	customLevelEncoder(TestLevel, enc)

	if len(enc.values) != 1 {
		t.Fatalf("expected 1 appended string, got %d", len(enc.values))
	}
	if enc.values[0] != "\x1b[95mTEST\x1b[0m" {
		t.Errorf("expected magenta TEST string, got %q", enc.values[0])
	}
}

func TestCustomLevelEncoder_DefaultLevel(t *testing.T) {
	enc := &testArrayEncoder{}
	customLevelEncoder(zapcore.InfoLevel, enc)

	if len(enc.values) != 1 {
		t.Fatalf("expected 1 appended string, got %d", len(enc.values))
	}
	// CapitalColorLevelEncoder should produce something containing "INFO"
}

func TestCustomLevelEncoder_AllLevels(t *testing.T) {
	levels := []zapcore.Level{
		zapcore.DebugLevel,
		zapcore.WarnLevel,
		zapcore.ErrorLevel,
	}

	for _, level := range levels {
		enc := &testArrayEncoder{}
		customLevelEncoder(level, enc)
		if len(enc.values) == 0 {
			t.Errorf("expected appended string for level %v", level)
		}
	}
}

func TestTest_NilSugar(t *testing.T) {
	oldSugar := Sugar
	Sugar = nil
	defer func() { Sugar = oldSugar }()

	// Should not panic
	Test("test message")
}

func TestTest_WithSugar(t *testing.T) {
	InitConsoleOnly(true)
	// Should not panic
	Test("test message")
}

func TestTestf_NilSugar(t *testing.T) {
	oldSugar := Sugar
	Sugar = nil
	defer func() { Sugar = oldSugar }()

	// Should not panic
	Testf("test %s %d", "message", 42)
}

func TestTestf_WithSugar(t *testing.T) {
	InitConsoleOnly(true)
	// Should not panic
	Testf("test %s %d", "message", 42)
}

func TestLogError_CriticalSeverity(t *testing.T) {
	InitConsoleOnly(true)
	err := errors.NewDBConnectionFailed("defra", "Connect", "", fmt.Errorf("connection refused"))
	// Should not panic
	LogError(err, "database connection failed")
}

func TestLogError_ErrorSeverity(t *testing.T) {
	InitConsoleOnly(true)
	err := errors.NewRPCTimeout("rpc", "GetBlock", "", fmt.Errorf("timeout"))
	LogError(err, "rpc timeout")
}

func TestLogError_WarningSeverity(t *testing.T) {
	InitConsoleOnly(true)
	err := errors.NewRateLimited("rpc", "GetBlock", "", fmt.Errorf("429"))
	LogError(err, "rate limited")
}

func TestLogError_InfoSeverity(t *testing.T) {
	InitConsoleOnly(true)
	err := &mockInfoError{}
	LogError(err, "info level log")
}

// mockInfoError implements errors.IndexerError with Info severity
// (no constructor produces Info severity, but LogError handles it)
type mockInfoError struct{}

func (e *mockInfoError) Error() string                   { return "info error" }
func (e *mockInfoError) Code() string                    { return "INFO_CODE" }
func (e *mockInfoError) Severity() errors.Severity       { return errors.Info }
func (e *mockInfoError) Retryable() errors.RetryBehavior { return errors.NonRetryable }
func (e *mockInfoError) Context() errors.ErrorContext {
	return errors.ErrorContext{Component: "test", Operation: "TestOp"}
}
func (e *mockInfoError) Unwrap() error { return nil }

func TestLogError_WithBlockNumberAndTxHash(t *testing.T) {
	InitConsoleOnly(true)
	blockNum := int64(42)
	txHash := "0xabc123"
	err := errors.NewRPCTimeout("rpc", "GetBlock", "", fmt.Errorf("timeout"),
		errors.WithBlockNumber(blockNum),
		errors.WithTxHash(txHash),
	)
	LogError(err, "rpc timeout with context")
}

func TestLogError_WithCustomFields(t *testing.T) {
	InitConsoleOnly(true)
	err := errors.NewRPCTimeout("rpc", "GetBlock", "", fmt.Errorf("timeout"))
	LogError(err, "rpc timeout", zap.Int("attempt", 3), zap.String("endpoint", "localhost"))
}

func TestLogError_NonIndexerError(t *testing.T) {
	InitConsoleOnly(true)
	err := fmt.Errorf("standard error")
	LogError(err, "something failed")
}

func TestLogError_NonIndexerError_WithFields(t *testing.T) {
	InitConsoleOnly(true)
	err := fmt.Errorf("standard error")
	LogError(err, "something failed", zap.String("context", "test"))
}

// testArrayEncoder is a minimal PrimitiveArrayEncoder for testing customLevelEncoder
type testArrayEncoder struct {
	values []string
}

func (e *testArrayEncoder) AppendBool(v bool)             {}
func (e *testArrayEncoder) AppendByteString(v []byte)     {}
func (e *testArrayEncoder) AppendComplex128(v complex128) {}
func (e *testArrayEncoder) AppendComplex64(v complex64)   {}
func (e *testArrayEncoder) AppendFloat64(v float64)       {}
func (e *testArrayEncoder) AppendFloat32(v float32)       {}
func (e *testArrayEncoder) AppendInt(v int)               {}
func (e *testArrayEncoder) AppendInt64(v int64)           {}
func (e *testArrayEncoder) AppendInt32(v int32)           {}
func (e *testArrayEncoder) AppendInt16(v int16)           {}
func (e *testArrayEncoder) AppendInt8(v int8)             {}
func (e *testArrayEncoder) AppendString(v string)         { e.values = append(e.values, v) }
func (e *testArrayEncoder) AppendUint(v uint)             {}
func (e *testArrayEncoder) AppendUint64(v uint64)         {}
func (e *testArrayEncoder) AppendUint32(v uint32)         {}
func (e *testArrayEncoder) AppendUint16(v uint16)         {}
func (e *testArrayEncoder) AppendUint8(v uint8)           {}
func (e *testArrayEncoder) AppendUintptr(v uintptr)       {}
