//go:build !branchable
// +build !branchable

package schema

import (
	"strings"
	"testing"
)

func TestGetSchema(t *testing.T) {
	s := GetSchema()
	if s == "" {
		t.Fatal("GetSchema() returned empty string")
	}
	if !strings.Contains(s, "Ethereum__Mainnet__Block") {
		t.Error("schema should contain Ethereum__Mainnet__Block type")
	}
}
