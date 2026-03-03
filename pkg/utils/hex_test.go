package utils

import (
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
)

func TestNumberToHex(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{"zero", 0, "0x0"},
		{"small", 255, "0xff"},
		{"large", 4096, "0x1000"},
		{"negative", -1, "0x-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NumberToHex(tt.input)
			if got != tt.expected {
				t.Errorf("NumberToHex(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNumberToHex_UnsignedTypes(t *testing.T) {
	t.Run("uint8", func(t *testing.T) {
		got := NumberToHex(uint8(255))
		if got != "0xff" {
			t.Errorf("NumberToHex(uint8(255)) = %q, want %q", got, "0xff")
		}
	})

	t.Run("uint32", func(t *testing.T) {
		got := NumberToHex(uint32(65535))
		if got != "0xffff" {
			t.Errorf("NumberToHex(uint32(65535)) = %q, want %q", got, "0xffff")
		}
	})

	t.Run("uint64", func(t *testing.T) {
		got := NumberToHex(uint64(18446744073709551615))
		if got != "0xffffffffffffffff" {
			t.Errorf("NumberToHex(uint64(max)) = %q, want %q", got, "0xffffffffffffffff")
		}
	})
}

func TestHexToInt(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  int64
		expectErr bool
	}{
		{"with 0x prefix", "0x1", 1, false},
		{"hex ff", "0xff", 255, false},
		{"hex zero", "0x0", 0, false},
		{"large hex", "0x1234567890ABCDEF", 0x1234567890ABCDEF, false},
		{"without prefix base10", "100", 100, false},
		{"empty string", "", 0, true},
		{"invalid string", "xyz", 0, true},
		{"invalid hex chars", "0xGG", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HexToInt(tt.input)
			if tt.expectErr {
				if err == nil {
					t.Errorf("HexToInt(%q) expected error, got nil", tt.input)
				}
				// Verify it returns an IndexerError (DataError)
				if errors.IsDataError(err) == false && err != nil {
					// For empty string, the underlying is nil so it's still a DataError
					var indexerErr errors.IndexerError
					if ie, ok := err.(errors.IndexerError); ok {
						indexerErr = ie
					}
					if indexerErr != nil && indexerErr.Code() != "INVALID_HEX" {
						t.Errorf("expected INVALID_HEX error code, got %q", indexerErr.Code())
					}
				}
			} else {
				if err != nil {
					t.Errorf("HexToInt(%q) unexpected error: %v", tt.input, err)
				}
				if got != tt.expected {
					t.Errorf("HexToInt(%q) = %d, want %d", tt.input, got, tt.expected)
				}
			}
		})
	}
}
