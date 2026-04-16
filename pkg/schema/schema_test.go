//go:build !branchable
// +build !branchable

package schema

import (
	"strings"
	"testing"
)

func TestGetSchema(t *testing.T) {
	t.Parallel()
	s := GetSchema()
	if s == "" {
		t.Fatal("GetSchema() returned empty string")
	}
	if !strings.Contains(s, "Ethereum__Sepolia__Block") {
		t.Error("schema should contain Ethereum__Sepolia__Block type")
	}
}
