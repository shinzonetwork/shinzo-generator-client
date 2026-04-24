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
	if !strings.Contains(s, "Polygon__Mainnet__Block") {
		t.Error("schema should contain Polygon__Mainnet__Block type")
	}
}
