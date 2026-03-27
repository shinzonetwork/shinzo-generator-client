package errors

import (
	"fmt"
	"testing"
	"time"
)

func TestIsErrNotFound(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"not found", fmt.Errorf("document not found"), true},
		{"does not exist", fmt.Errorf("collection does not exist"), true},
		{"unrelated", fmt.Errorf("connection timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsErrNotFound(tt.err); got != tt.expected {
				t.Errorf("IsErrNotFound() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsErrAlreadyExists(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"already exists", fmt.Errorf("document already exists"), true},
		{"collection already exists", fmt.Errorf("collection already exists in database"), true},
		{"unrelated", fmt.Errorf("timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsErrAlreadyExists(tt.err); got != tt.expected {
				t.Errorf("IsErrAlreadyExists() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsErrTransactionConflict(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"conflict", fmt.Errorf("transaction conflict detected"), true},
		{"unrelated", fmt.Errorf("timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsErrTransactionConflict(tt.err); got != tt.expected {
				t.Errorf("IsErrTransactionConflict() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsErrUnsupportedTxType(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"not supported", fmt.Errorf("transaction type not supported"), true},
		{"invalid type", fmt.Errorf("invalid transaction type"), true},
		{"unrelated", fmt.Errorf("timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsErrUnsupportedTxType(tt.err); got != tt.expected {
				t.Errorf("IsErrUnsupportedTxType() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"plain error", fmt.Errorf("something"), false},
		{"retryable", NewRPCTimeout("rpc", "op", "", nil), true},
		{"non-retryable", NewInvalidHex("conv", "op", "bad", nil), false},
		{"retryable with backoff", NewRateLimited("rpc", "op", "", nil), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.expected {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsRetryableWithBackoff(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"plain error", fmt.Errorf("something"), false},
		{"retryable (no backoff)", NewRPCTimeout("rpc", "op", "", nil), false},
		{"retryable with backoff", NewRateLimited("rpc", "op", "", nil), true},
		{"non-retryable", NewInvalidHex("conv", "op", "bad", nil), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableWithBackoff(tt.err); got != tt.expected {
				t.Errorf("IsRetryableWithBackoff() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsCritical(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"plain error", fmt.Errorf("something"), false},
		{"critical", NewDBConnectionFailed("defra", "Connect", "", nil), true},
		{"non-critical", NewRPCTimeout("rpc", "op", "", nil), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCritical(tt.err); got != tt.expected {
				t.Errorf("IsCritical() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsNetworkError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"network error", NewRPCTimeout("rpc", "op", "", nil), true},
		{"data error", NewInvalidHex("conv", "op", "", nil), false},
		{"plain error", fmt.Errorf("test"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNetworkError(tt.err); got != tt.expected {
				t.Errorf("IsNetworkError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsDataError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"data error", NewInvalidHex("conv", "op", "", nil), true},
		{"network error", NewRPCTimeout("rpc", "op", "", nil), false},
		{"plain error", fmt.Errorf("test"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDataError(tt.err); got != tt.expected {
				t.Errorf("IsDataError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsStorageError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"storage error", NewDBConnectionFailed("defra", "Connect", "", nil), true},
		{"network error", NewRPCTimeout("rpc", "op", "", nil), false},
		{"plain error", fmt.Errorf("test"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStorageError(tt.err); got != tt.expected {
				t.Errorf("IsStorageError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetErrorCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"indexer error", NewRPCTimeout("rpc", "op", "", nil), CodeRPCTimeout},
		{"plain error", fmt.Errorf("test"), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetErrorCode(tt.err); got != tt.expected {
				t.Errorf("GetErrorCode() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetRetryDelay(t *testing.T) {
	nonRetryable := NewInvalidHex("conv", "op", "", nil)
	retryable := NewRPCTimeout("rpc", "op", "", nil)
	backoff := NewRateLimited("rpc", "op", "", nil)

	tests := []struct {
		name     string
		err      error
		attempt  int
		expected time.Duration
	}{
		{"non-retryable returns 0", nonRetryable, 0, 0},
		{"retryable returns 1s", retryable, 0, time.Second},
		{"retryable returns 1s regardless of attempt", retryable, 5, time.Second},
		{"backoff attempt 0", backoff, 0, time.Second},
		{"backoff attempt 1", backoff, 1, 2 * time.Second},
		{"backoff attempt 2", backoff, 2, 4 * time.Second},
		{"backoff attempt 3", backoff, 3, 8 * time.Second},
		{"backoff attempt 4", backoff, 4, 16 * time.Second},
		{"backoff attempt 5 capped", backoff, 5, 30 * time.Second},
		{"backoff attempt 10 capped", backoff, 10, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetRetryDelay(tt.err, tt.attempt); got != tt.expected {
				t.Errorf("GetRetryDelay(attempt=%d) = %v, want %v", tt.attempt, got, tt.expected)
			}
		})
	}
}

func TestWrapError(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		result := WrapError(nil, "comp", "op")
		if result != nil {
			t.Errorf("WrapError(nil) should return nil, got %v", result)
		}
	})

	t.Run("IndexerError returned as-is", func(t *testing.T) {
		original := NewRPCTimeout("rpc", "op", "", nil)
		result := WrapError(original, "other", "otherOp")
		if result != original {
			t.Error("WrapError should return IndexerError as-is")
		}
	})

	t.Run("plain error wrapped as SystemError", func(t *testing.T) {
		plain := fmt.Errorf("something went wrong")
		result := WrapError(plain, "comp", "op")
		if result == nil {
			t.Fatal("WrapError should not return nil for non-nil error")
		}
		if result.Code() != "WRAPPED_ERROR" {
			t.Errorf("Code() = %q, want %q", result.Code(), "WRAPPED_ERROR")
		}

		if _, ok := result.(*SystemError); !ok {
			t.Error("wrapped error should be *SystemError")
		}
	})
}

func TestLogContext_IndexerError(t *testing.T) {
	blockNum := int64(42)
	txHash := "0xabc"
	err := NewRPCTimeout("rpc", "GetBlock", "", nil,
		WithBlockNumber(blockNum),
		WithTxHash(txHash),
		WithMetadata("extra", "data"),
	)

	ctx := LogContext(err)

	if ctx["error_code"] != CodeRPCTimeout {
		t.Errorf("error_code = %v, want %v", ctx["error_code"], CodeRPCTimeout)
	}
	if ctx["severity"] != "ERROR" {
		t.Errorf("severity = %v, want ERROR", ctx["severity"])
	}
	if ctx["retryable"] != "RETRYABLE" {
		t.Errorf("retryable = %v, want RETRYABLE", ctx["retryable"])
	}
	if ctx["component"] != "rpc" {
		t.Errorf("component = %v, want rpc", ctx["component"])
	}
	if ctx["operation"] != "GetBlock" {
		t.Errorf("operation = %v, want GetBlock", ctx["operation"])
	}
	if ctx["block_number"] != blockNum {
		t.Errorf("block_number = %v, want %d", ctx["block_number"], blockNum)
	}
	if ctx["tx_hash"] != txHash {
		t.Errorf("tx_hash = %v, want %s", ctx["tx_hash"], txHash)
	}
	if ctx["extra"] != "data" {
		t.Errorf("extra = %v, want data", ctx["extra"])
	}
}

func TestLogContext_IndexerError_MinimalContext(t *testing.T) {
	err := NewInvalidHex("conv", "ParseHex", "bad", nil)
	ctx := LogContext(err)

	if ctx["error_code"] != CodeInvalidHex {
		t.Errorf("error_code = %v, want %v", ctx["error_code"], CodeInvalidHex)
	}
	// BlockNumber and TxHash should not be present
	if _, ok := ctx["block_number"]; ok {
		t.Error("block_number should not be present")
	}
	if _, ok := ctx["tx_hash"]; ok {
		t.Error("tx_hash should not be present")
	}
}

func TestLogContext_PlainError(t *testing.T) {
	err := fmt.Errorf("something broke")
	ctx := LogContext(err)

	if ctx["error_type"] != "standard_error" {
		t.Errorf("error_type = %v, want standard_error", ctx["error_type"])
	}
	if ctx["error"] != "something broke" {
		t.Errorf("error = %v, want 'something broke'", ctx["error"])
	}
}
