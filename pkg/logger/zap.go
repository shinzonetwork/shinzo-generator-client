package logger

import (
	"os"
	"path/filepath"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Sugar *zap.SugaredLogger

// Custom log levels for different contexts
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

// InitConsoleOnly initializes logger with console output only (for tests)
func InitConsoleOnly(development bool) {
	initLogger(development, false)
}

// InitWithFiles initializes logger with both console and file output (for production)
func InitWithFiles(development bool) {
	initLogger(development, true)
}

// customLevelEncoder handles our custom TEST log level with color coding
func customLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch level {
	case TestLevel:
		enc.AppendString("\x1b[95mTEST\x1b[0m") // Pink/Magenta color for TEST level
	default:
		zapcore.CapitalColorLevelEncoder(level, enc)
	}
}

// Test logs a message at TEST level - specifically for test output
func Test(msg string) {
	if Sugar != nil {
		Sugar.Log(TestLevel, msg)
	}
}

// Testf logs a formatted message at TEST level - specifically for test output
func Testf(template string, args ...any) {
	if Sugar != nil {
		Sugar.Logf(TestLevel, template, args...)
	}
}

// Init initializes logger with default behavior (files enabled unless NO_LOG_FILES env var is set)
func Init(development bool) {
	initLogger(development, true)
}

func initLogger(development bool, enableFiles bool) {
	var zapLevel zapcore.Level
	if development {
		zapLevel = TestLevel // Show TEST level and above in development mode
	} else {
		zapLevel = zap.InfoLevel
	}

	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.EncodeLevel = customLevelEncoder

	// Create console writer (stdout)
	consoleWriter := zapcore.Lock(os.Stdout)
	var cores []zapcore.Core

	// Always add console core
	consoleCore := zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), consoleWriter, zapLevel)
	cores = append(cores, consoleCore)

	// Only create log files if enabled
	if enableFiles {
		logsDir := "logs"
		if err := os.MkdirAll(logsDir, 0755); err == nil {
			// Directory exists or was created successfully
			logFile := filepath.Join(logsDir, "logfile.log")
			errorFile := filepath.Join(logsDir, "errorfile.log")

			// Create file writer for all logs
			if logFileWriter, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err == nil {
				// Core for all logs to logfile
				logFileCore := zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), zapcore.AddSync(logFileWriter), zapLevel)
				cores = append(cores, logFileCore)

				// Create file writer for errors only
				if errorFileWriter, err := os.OpenFile(errorFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err == nil {
					// Core for ERROR level logs only to errorfile
					errorCore := zapcore.NewCore(
						zapcore.NewConsoleEncoder(encoderConfig),
						zapcore.AddSync(errorFileWriter),
						zapcore.ErrorLevel, // Only ERROR level and above
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

// New helper function for error logging
func LogError(err error, message string, fields ...zap.Field) {
	if indexerErr, ok := err.(errors.IndexerError); ok {
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

		// Add custom fields
		allFields = append(allFields, fields...)

		// Log at appropriate level based on severity using non-sugared logger
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
		// For non-IndexerError, use non-sugared logger with fields
		logFields := append(fields, zap.Error(err))
		Sugar.Desugar().Error(message, logFields...)
	}
}
