package logger

import (
	stderrors "errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Sugar is the global sugared logger instance used throughout the application. It is initialized in Init() and should be used for all logging to ensure consistent formatting and output.
var Sugar *zap.SugaredLogger //nolint:gochecknoglobals // logger is intentionally a package-level global

var (
	// initMu serializes initLogger so Sugar is assigned at most once per nil state.
	initMu sync.Mutex //nolint:gochecknoglobals // guards the package-level logger
	// activeCore holds the core built by the most recent initLogger call.
	activeCore atomic.Pointer[coreBox] //nolint:gochecknoglobals // swapped by initLogger, read by swapCore
)

// coreBox wraps a zapcore.Core so it can be stored in an atomic.Pointer.
type coreBox struct{ zapcore.Core }

// swapCore delegates to activeCore, letting initLogger reconfigure logging
// without reassigning Sugar while other goroutines are logging through it.
type swapCore struct{}

func (swapCore) Enabled(l zapcore.Level) bool { return activeCore.Load().Enabled(l) }

func (swapCore) With(fields []zapcore.Field) zapcore.Core { return activeCore.Load().With(fields) }

func (swapCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return activeCore.Load().Check(ent, ce)
}

func (swapCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	return activeCore.Load().Write(ent, fields)
}

func (swapCore) Sync() error { return activeCore.Load().Sync() }

// Custom log levels for different contexts.
const (
	TestLevel = zapcore.Level(-2) // Between DEBUG (-1) and INFO (0), specifically for tests
)

// using the logger looks like this:

// logger.Sugar.Info("here is a log example");
// or
// logger := logger.Sugar()
// logger.Info("here is a log example")
//
// For tests, use:
// logger.Test("test-specific message")
// logger.Testf("test message with %s", "formatting")

// InitConsoleOnly initializes logger with console output only (for tests).
func InitConsoleOnly(development bool) {
	initLogger(development, false)
}

// InitWithFiles initializes logger with both console and file output (for production).
func InitWithFiles(development bool) {
	initLogger(development, true)
}

// customLevelEncoder handles our custom TEST log level with color coding.
func customLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch level { //nolint:exhaustive // all other levels handled by default case
	case TestLevel:
		enc.AppendString("\x1b[95mTEST\x1b[0m") // Pink/Magenta color for TEST level.
	default:
		zapcore.CapitalColorLevelEncoder(level, enc)
	}
}

// Test logs a message at TEST level - specifically for test output.
func Test(msg string) {
	if Sugar != nil {
		Sugar.Log(TestLevel, msg)
	}
}

// Testf logs a formatted message at TEST level - specifically for test output.
func Testf(template string, args ...any) {
	if Sugar != nil {
		Sugar.Logf(TestLevel, template, args...)
	}
}

// Init initializes logger with default behavior (files enabled unless NO_LOG_FILES env var is set).
func Init(development bool) {
	initLogger(development, true)
}

// logDir returns the directory log files are written to. SHINZO_LOG_DIR lets
// each process (or test package) keep its own logs instead of writing to a
// "logs" directory relative to whatever the working directory happens to be.
func logDir() string {
	if dir := os.Getenv("SHINZO_LOG_DIR"); dir != "" {
		return dir
	}
	return "logs"
}

func initLogger(development, enableFiles bool) {
	// NO_LOG_FILES disables file output entirely (documented on Init, and used
	// by tests so they never write log files into the package directory).
	enableFiles = enableFiles && os.Getenv("NO_LOG_FILES") == ""

	var zapLevel zapcore.Level
	if development {
		zapLevel = TestLevel // Show TEST level and above in development mode.
	} else {
		zapLevel = zap.InfoLevel
	}

	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.EncodeLevel = customLevelEncoder

	// Create console writer (stdout).
	consoleWriter := zapcore.Lock(os.Stdout)
	var cores []zapcore.Core

	// Always add console core.
	consoleCore := zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), consoleWriter, zapLevel)
	cores = append(cores, consoleCore)

	// Only create log files if enabled.
	if enableFiles {
		logsDir := logDir()
		if err := os.MkdirAll(logsDir, 0o750); err == nil { // nolint:mnd
			// Directory exists or was created successfully.
			logFile := filepath.Join(logsDir, "logfile.log")
			errorFile := filepath.Join(logsDir, "errorfile.log")

			// Create file writer for all logs.
			if logFileWriter, err := os.OpenFile(filepath.Clean(logFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil { // nolint:mnd
				// Core for all logs to logfile.
				logFileCore := zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(logFileWriter), zapLevel)
				cores = append(cores, logFileCore)

				// Create file writer for errors only.
				if errorFileWriter, err := os.OpenFile(filepath.Clean(errorFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil { // nolint:mnd
					// Core for ERROR level logs only to errorfile.
					errorCore := zapcore.NewCore(
						zapcore.NewConsoleEncoder(encoderConfig),
						zapcore.AddSync(errorFileWriter),
						zapcore.ErrorLevel, // Only ERROR level and above.
					)
					cores = append(cores, errorCore)
				}
			}
		}
	}

	// Combine all cores and swap them in. Sugar is only assigned when unset, so
	// re-initializing (e.g. from parallel tests) never races with active loggers.
	initMu.Lock()
	defer initMu.Unlock()
	activeCore.Store(&coreBox{zapcore.NewTee(cores...)})
	if Sugar == nil {
		Sugar = zap.New(swapCore{}).Sugar()
	}
}

// LogError logs an error with structured fields based on its type.
func LogError(err error, message string, fields ...zap.Field) {
	if indexerErr, ok := stderrors.AsType[errors.IndexerError](err); ok {
		ctx := indexerErr.Context()
		allFields := []zap.Field{
			zap.String("error_code", indexerErr.Code()),
			zap.String("severity", indexerErr.Severity().String()),
			zap.String("retryable", indexerErr.Retryable().String()),
			zap.String("component", ctx.Component),
			zap.String("operation", ctx.Operation),
			zap.Time("error_timestamp", ctx.Timestamp),
			zap.Error(err),
		}

		if ctx.BlockNumber != nil {
			allFields = append(allFields, zap.Int64("block_number", *ctx.BlockNumber))
		}

		if ctx.TxHash != nil {
			allFields = append(allFields, zap.String("tx_hash", *ctx.TxHash))
		}

		// Add custom fields.
		allFields = append(allFields, fields...)

		// Log at appropriate level based on severity using non-sugared logger.
		switch indexerErr.Severity() {
		case errors.Critical:
			Sugar.Desugar().Error(message, allFields...)
		case errors.Error:
			Sugar.Desugar().Error(message, allFields...)
		case errors.Warning:
			Sugar.Desugar().Warn(message, allFields...)
		case errors.Info:
			Sugar.Desugar().Info(message, allFields...)
		}
	} else {
		// For non-IndexerError, use non-sugared logger with fields.
		fields = append(fields, zap.Error(err))
		Sugar.Desugar().Error(message, fields...)
	}
}
