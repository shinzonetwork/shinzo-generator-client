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

func TestGetBranchableSchema(t *testing.T) {
	s := GetBranchableSchema()
	if s == "" {
		t.Fatal("GetBranchableSchema() returned empty string")
	}
	if !strings.Contains(s, "Ethereum__Mainnet__Block") {
		t.Error("branchable schema should contain Ethereum__Mainnet__Block type")
	}
}

func TestIsBranchable(t *testing.T) {
	// Default build (no branchable tag) should return false
	if IsBranchable() {
		t.Error("IsBranchable() should return false without branchable build tag")
	}
}

func TestGetSchemaForBuild(t *testing.T) {
	s := GetSchemaForBuild()
	if s == "" {
		t.Fatal("GetSchemaForBuild() returned empty string")
	}
	// Without branchable tag, should return standard schema
	expected := GetSchema()
	if s != expected {
		t.Error("GetSchemaForBuild() should return standard schema without branchable tag")
	}
}

func TestSchemaForBuild(t *testing.T) {
	t.Run("standard", func(t *testing.T) {
		s := schemaForBuild(false)
		if s != GetSchema() {
			t.Error("schemaForBuild(false) should return standard schema")
		}
	})
	t.Run("branchable", func(t *testing.T) {
		s := schemaForBuild(true)
		if s != GetBranchableSchema() {
			t.Error("schemaForBuild(true) should return branchable schema")
		}
	})
}
