package logger

import (
	stderrors "errors"
	"os"
	"path/filepath"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Sugar is the global sugared logger instance used throughout the application. It is initialized in Init() and should be used for all logging to ensure consistent formatting and output.
var Sugar *zap.SugaredLogger //nolint:gochecknoglobals // logger is intentionally a package-level global

// Custom log levels for different contexts.
const (
	TestLevel = zapcore.Level(-2) // Between DEBUG (-1) and INFO (0), specifically for tests

	// PerfLevel is for per-stage duration ("where does the time go") logs.
	// It sits below DEBUG so it never passes the standard console/file cores
	// (whose thresholds are DEBUG or INFO); a dedicated core with a
	// perf-only enabler (see initLogger) carries exactly this level, so
	// timing lines are always emitted in production without lowering the
	// ambient threshold.
	PerfLevel = zapcore.Level(-3)
)

// perfOnlyEnabler is a zapcore.LevelEnabler that enables only PerfLevel,
// turning a core built on it into a dedicated PERF channel.
type perfOnlyEnabler struct{}

func (perfOnlyEnabler) Enabled(lvl zapcore.Level) bool { return lvl == PerfLevel }

// using the logger looks like this:

// logger.Sugar.Info("here is a log example");
// or
// logger := logger.Sugar()
// logger.Info("here is a log example")
//
// For tests, use:
// logger.Test("test-specific message")
// logger.Testf("test message with %s", "formatting")
//
// For per-stage timing logs, use:
// logger.Perff("Block %d: %s", blockNum, timer.Total())

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
	case PerfLevel:
		enc.AppendString("\x1b[36mPERF\x1b[0m") // Cyan color for PERF level.
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

// Perff logs a formatted message at PERF level (per-stage timing).
func Perff(template string, args ...any) {
	if Sugar != nil {
		Sugar.Logf(PerfLevel, template, args...)
	}
}

// Init initializes logger with default behavior (files enabled unless NO_LOG_FILES env var is set).
func Init(development bool) {
	initLogger(development, true)
}

func initLogger(development, enableFiles bool) {
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

	// Dedicated PERF channel (per-stage timing): enabled regardless of the
	// ambient threshold so timing lines are emitted in production (which
	// runs at InfoLevel). NO_PERF_LOGS disables it.
	perfEnabled := os.Getenv("NO_PERF_LOGS") == ""
	if perfEnabled {
		cores = append(cores, zapcore.NewCore(
			zapcore.NewConsoleEncoder(encoderConfig), consoleWriter, perfOnlyEnabler{}))
	}

	// Only create log files if enabled.
	if enableFiles {
		logsDir := "logs"
		if err := os.MkdirAll(logsDir, 0o750); err == nil { // nolint:mnd
			// Directory exists or was created successfully.
			logFile := filepath.Join(logsDir, "logfile.log")
			errorFile := filepath.Join(logsDir, "errorfile.log")

			// Create file writer for all logs.
			if logFileWriter, err := os.OpenFile(filepath.Clean(logFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil { //nolint:mnd
				// Core for all logs to logfile.
				logFileCore := zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(logFileWriter), zapLevel)
				cores = append(cores, logFileCore)

				// PERF lines also go to the general logfile.
				if perfEnabled {
					cores = append(cores, zapcore.NewCore(
						zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(logFileWriter), perfOnlyEnabler{}))
				}

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

	// Combine all cores
	core := zapcore.NewTee(cores...)
	logger := zap.New(core)

	Sugar = logger.Sugar()
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
