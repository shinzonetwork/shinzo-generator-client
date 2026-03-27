package errors

import (
	"errors"
	"fmt"
	"testing"
)

func TestNetworkErrorConstructors(t *testing.T) {
	underlying := fmt.Errorf("dial tcp: connection refused")

	tests := []struct {
		name       string
		err        IndexerError
		code       string
		severity   Severity
		retryable  RetryBehavior
	}{
		{
			"HTTPConnectionFailed",
			NewHTTPConnectionFailed("rpc", "GetBlock", "http://localhost", underlying),
			CodeHTTPError, Error, Retryable,
		},
		{
			"RPCTimeout",
			NewRPCTimeout("rpc", "GetBlock", "http://localhost", underlying),
			CodeRPCTimeout, Error, Retryable,
		},
		{
			"RPCConnectionFailed",
			NewRPCConnectionFailed("rpc", "GetBlock", "http://localhost", underlying),
			CodeRPCConnectionFailed, Error, Retryable,
		},
		{
			"RateLimited",
			NewRateLimited("rpc", "GetBlock", "http://localhost", underlying),
			CodeRateLimited, Warning, RetryableWithBackoff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code() != tt.code {
				t.Errorf("Code() = %q, want %q", tt.err.Code(), tt.code)
			}
			if tt.err.Severity() != tt.severity {
				t.Errorf("Severity() = %v, want %v", tt.err.Severity(), tt.severity)
			}
			if tt.err.Retryable() != tt.retryable {
				t.Errorf("Retryable() = %v, want %v", tt.err.Retryable(), tt.retryable)
			}

			var netErr *NetworkError
			if !errors.As(tt.err, &netErr) {
				t.Error("expected error to be *NetworkError")
			}

			ctx := tt.err.Context()
			if ctx.Component != "rpc" {
				t.Errorf("Context().Component = %q, want %q", ctx.Component, "rpc")
			}
			if ctx.Operation != "GetBlock" {
				t.Errorf("Context().Operation = %q, want %q", ctx.Operation, "GetBlock")
			}
		})
	}
}

func TestDataErrorConstructors(t *testing.T) {
	underlying := fmt.Errorf("parse error")

	tests := []struct {
		name      string
		err       IndexerError
		code      string
		severity  Severity
		retryable RetryBehavior
	}{
		{
			"InvalidHex",
			NewInvalidHex("converter", "ParseHex", "0xZZ", underlying),
			CodeInvalidHex, Error, NonRetryable,
		},
		{
			"InvalidBlockFormat",
			NewInvalidBlockFormat("converter", "ParseBlock", "badblock", underlying),
			CodeInvalidBlockFormat, Error, NonRetryable,
		},
		{
			"InvalidInputFormat",
			NewInvalidInputFormat("converter", "ParseInput", "badinput", underlying),
			CodeInvalidInputFormat, Error, NonRetryable,
		},
		{
			"ParsingFailed",
			NewParsingFailed("converter", "ParseJSON", "json_data", underlying),
			CodeParsingFailed, Error, NonRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code() != tt.code {
				t.Errorf("Code() = %q, want %q", tt.err.Code(), tt.code)
			}
			if tt.err.Severity() != tt.severity {
				t.Errorf("Severity() = %v, want %v", tt.err.Severity(), tt.severity)
			}
			if tt.err.Retryable() != tt.retryable {
				t.Errorf("Retryable() = %v, want %v", tt.err.Retryable(), tt.retryable)
			}

			var dataErr *DataError
			if !errors.As(tt.err, &dataErr) {
				t.Error("expected error to be *DataError")
			}
		})
	}
}

func TestStorageErrorConstructors(t *testing.T) {
	underlying := fmt.Errorf("db error")

	tests := []struct {
		name      string
		err       IndexerError
		code      string
		severity  Severity
		retryable RetryBehavior
	}{
		{
			"DBConnectionFailed",
			NewDBConnectionFailed("defra", "Connect", "localhost:9181", underlying),
			CodeDBConnectionFailed, Critical, Retryable,
		},
		{
			"QueryFailed",
			NewQueryFailed("defra", "GetBlock", "query_string", underlying),
			CodeQueryFailed, Error, Retryable,
		},
		{
			"DocumentNotFound",
			NewDocumentNotFound("defra", "GetBlock", "Block", "block_123"),
			CodeDocumentNotFound, Warning, NonRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code() != tt.code {
				t.Errorf("Code() = %q, want %q", tt.err.Code(), tt.code)
			}
			if tt.err.Severity() != tt.severity {
				t.Errorf("Severity() = %v, want %v", tt.err.Severity(), tt.severity)
			}
			if tt.err.Retryable() != tt.retryable {
				t.Errorf("Retryable() = %v, want %v", tt.err.Retryable(), tt.retryable)
			}

			var storageErr *StorageError
			if !errors.As(tt.err, &storageErr) {
				t.Error("expected error to be *StorageError")
			}
		})
	}
}

func TestSystemErrorConstructors(t *testing.T) {
	underlying := fmt.Errorf("system error")

	tests := []struct {
		name      string
		err       IndexerError
		code      string
		severity  Severity
		retryable RetryBehavior
	}{
		{
			"ConfigurationError",
			NewConfigurationError("config", "Load", "missing field", "config.yaml", underlying),
			CodeConfigurationError, Critical, NonRetryable,
		},
		{
			"ServiceUnavailable",
			NewServiceUnavailable("indexer", "Start", "DefraDB", "localhost:9181", underlying),
			CodeServiceUnavailable, Critical, RetryableWithBackoff,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code() != tt.code {
				t.Errorf("Code() = %q, want %q", tt.err.Code(), tt.code)
			}
			if tt.err.Severity() != tt.severity {
				t.Errorf("Severity() = %v, want %v", tt.err.Severity(), tt.severity)
			}
			if tt.err.Retryable() != tt.retryable {
				t.Errorf("Retryable() = %v, want %v", tt.err.Retryable(), tt.retryable)
			}

			var sysErr *SystemError
			if !errors.As(tt.err, &sysErr) {
				t.Error("expected error to be *SystemError")
			}
		})
	}
}

func TestWithBlockNumber(t *testing.T) {
	err := NewRPCTimeout("rpc", "GetBlock", "", nil, WithBlockNumber(42))
	ctx := err.Context()
	if ctx.BlockNumber == nil {
		t.Fatal("BlockNumber should not be nil")
	}
	if *ctx.BlockNumber != 42 {
		t.Errorf("BlockNumber = %d, want 42", *ctx.BlockNumber)
	}
}

func TestWithTxHash(t *testing.T) {
	hash := "0xabc123"
	err := NewRPCTimeout("rpc", "GetTx", "", nil, WithTxHash(hash))
	ctx := err.Context()
	if ctx.TxHash == nil {
		t.Fatal("TxHash should not be nil")
	}
	if *ctx.TxHash != hash {
		t.Errorf("TxHash = %q, want %q", *ctx.TxHash, hash)
	}
}

func TestWithMetadata(t *testing.T) {
	err := NewRPCTimeout("rpc", "GetBlock", "", nil, WithMetadata("retry_count", 3))
	ctx := err.Context()
	if ctx.Metadata == nil {
		t.Fatal("Metadata should not be nil")
	}
	if ctx.Metadata["retry_count"] != 3 {
		t.Errorf("Metadata[retry_count] = %v, want 3", ctx.Metadata["retry_count"])
	}
}

func TestWithMetadata_NilMapInitialization(t *testing.T) {
	// The WithMetadata function has a nil check on ctx.Metadata
	// Since newBaseError always initializes Metadata, we test the nil branch
	// by calling WithMetadata directly on a context with nil Metadata
	ctx := &ErrorContext{}
	opt := WithMetadata("key", "value")
	opt(ctx)
	if ctx.Metadata == nil {
		t.Fatal("Metadata should have been initialized")
	}
	if ctx.Metadata["key"] != "value" {
		t.Errorf("Metadata[key] = %v, want %q", ctx.Metadata["key"], "value")
	}
}

func TestNewBaseError_TimestampAndMetadata(t *testing.T) {
	err := NewRPCTimeout("comp", "op", "data", nil)
	ctx := err.Context()
	if ctx.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
	if ctx.Metadata == nil {
		t.Error("Metadata should be initialized")
	}
}

func TestConstructor_ErrorMessage_IncludesInputData(t *testing.T) {
	err := NewInvalidHex("converter", "ParseHex", "0xZZZ", nil)
	msg := err.Error()
	if msg == "" {
		t.Error("Error message should not be empty")
	}
	// InvalidHex includes input_data in message
	if !contains(msg, "0xZZZ") {
		t.Errorf("Error message should contain input data, got: %s", msg)
	}
}

func TestConstructor_ParsingFailed_IncludesInputData(t *testing.T) {
	err := NewParsingFailed("converter", "ParseJSON", "json_data", nil)
	msg := err.Error()
	if !contains(msg, "json_data") {
		t.Errorf("ParsingFailed message should contain input data, got: %s", msg)
	}
}

func TestDocumentNotFound_NilUnderlying(t *testing.T) {
	err := NewDocumentNotFound("defra", "GetBlock", "Block", "block_123")
	if err.Unwrap() != nil {
		t.Error("DocumentNotFound should have nil underlying error")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
