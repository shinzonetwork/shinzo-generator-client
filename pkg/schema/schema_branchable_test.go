//go:build branchable
// +build branchable

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

func TestGetBranchableSchema(t *testing.T) {
	s := GetBranchableSchema()
	if s == "" {
		t.Fatal("GetBranchableSchema() returned empty string")
	}
	if !strings.Contains(s, "Ethereum__Mainnet__Block") {
		t.Error("branchable schema should contain Ethereum__Mainnet__Block type")
	}
}

func TestIsBranchable_WithTag(t *testing.T) {
	if !IsBranchable() {
		t.Error("IsBranchable() should return true when built with branchable tag")
	}
}

func TestGetSchemaForBuild_WithTag(t *testing.T) {
	s := GetSchemaForBuild()
	if s == "" {
		t.Fatal("GetSchemaForBuild() returned empty string")
	}
	expected := GetBranchableSchema()
	if s != expected {
		t.Error("GetSchemaForBuild() should return branchable schema with branchable tag")
	}
}
