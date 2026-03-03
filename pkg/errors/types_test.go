package errors

import (
	"fmt"
	"testing"
)

func TestSeverity_String(t *testing.T) {
	tests := []struct {
		severity Severity
		expected string
	}{
		{Info, "INFO"},
		{Warning, "WARNING"},
		{Error, "ERROR"},
		{Critical, "CRITICAL"},
		{Severity(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.severity.String(); got != tt.expected {
				t.Errorf("Severity(%d).String() = %q, want %q", tt.severity, got, tt.expected)
			}
		})
	}
}

func TestRetryBehavior_String(t *testing.T) {
	tests := []struct {
		behavior RetryBehavior
		expected string
	}{
		{NonRetryable, "NON_RETRYABLE"},
		{Retryable, "RETRYABLE"},
		{RetryableWithBackoff, "RETRYABLE_WITH_BACKOFF"},
		{RetryBehavior(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.behavior.String(); got != tt.expected {
				t.Errorf("RetryBehavior(%d).String() = %q, want %q", tt.behavior, got, tt.expected)
			}
		})
	}
}

func TestBaseError_Error_WithUnderlying(t *testing.T) {
	underlying := fmt.Errorf("connection refused")
	e := &baseError{
		code:       "TEST_CODE",
		message:    "test message",
		underlying: underlying,
	}
	expected := "[TEST_CODE] test message: connection refused"
	if got := e.Error(); got != expected {
		t.Errorf("Error() = %q, want %q", got, expected)
	}
}

func TestBaseError_Error_WithoutUnderlying(t *testing.T) {
	e := &baseError{
		code:    "TEST_CODE",
		message: "test message",
	}
	expected := "[TEST_CODE] test message"
	if got := e.Error(); got != expected {
		t.Errorf("Error() = %q, want %q", got, expected)
	}
}

func TestBaseError_Accessors(t *testing.T) {
	underlying := fmt.Errorf("wrapped")
	ctx := ErrorContext{
		Component: "test",
		Operation: "TestOp",
	}
	e := &baseError{
		code:       "CODE",
		message:    "msg",
		severity:   Warning,
		retryable:  RetryableWithBackoff,
		context:    ctx,
		underlying: underlying,
	}

	if e.Code() != "CODE" {
		t.Errorf("Code() = %q, want %q", e.Code(), "CODE")
	}
	if e.Severity() != Warning {
		t.Errorf("Severity() = %v, want %v", e.Severity(), Warning)
	}
	if e.Retryable() != RetryableWithBackoff {
		t.Errorf("Retryable() = %v, want %v", e.Retryable(), RetryableWithBackoff)
	}
	if e.Context().Component != "test" {
		t.Errorf("Context().Component = %q, want %q", e.Context().Component, "test")
	}
	if e.Context().Operation != "TestOp" {
		t.Errorf("Context().Operation = %q, want %q", e.Context().Operation, "TestOp")
	}
	if e.Unwrap() != underlying {
		t.Errorf("Unwrap() = %v, want %v", e.Unwrap(), underlying)
	}
}

func TestErrorTypes_ImplementIndexerError(t *testing.T) {
	base := &baseError{
		code:    "TEST",
		message: "test",
	}

	var _ IndexerError = &NetworkError{baseError: base}
	var _ IndexerError = &DataError{baseError: base}
	var _ IndexerError = &StorageError{baseError: base}
	var _ IndexerError = &SystemError{baseError: base}
}
