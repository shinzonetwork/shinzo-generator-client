//go:build !branchable

package schema_test

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
)

func TestGetSchema(t *testing.T) {
	t.Parallel()
	s, err := schema.LoadSchemaSDL(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaSDL() error: %v", err)
	}
	if s == "" {
		t.Fatal("LoadSchemaSDL() returned empty string")
	}
	expectedType := evm.DefaultCollectionPrefix + "__Block"
	if !strings.Contains(s, expectedType) {
		t.Errorf("schema should contain %s type", expectedType)
	}
}

func TestLoadSchemaContainsAllCollectionTypes(t *testing.T) {
	t.Parallel()
	s, err := schema.LoadSchemaSDL(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaSDL() error: %v", err)
	}

	expectedTypes := evm.DefaultCollections()
	for _, typeName := range expectedTypes {
		if !strings.Contains(s, typeName) {
			t.Errorf("schema missing expected type: %s", typeName)
		}
	}
}

func TestGetSchemaForChain_ReplacesPrefix(t *testing.T) {
	t.Parallel()
	defaultSchema, err := schema.LoadSchemaSDL(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaSDL() error: %v", err)
	}
	arbSchema, err := schema.GetSchemaForChain(evm.NewCollectionNames("Arbitrum__Mainnet"))
	if err != nil {
		t.Fatalf("GetSchemaForChain() error: %v", err)
	}

	if arbSchema == defaultSchema {
		t.Fatal("GetSchemaForChain should produce different output for different prefix")
	}

	if strings.Contains(arbSchema, evm.DefaultCollectionPrefix) {
		t.Errorf("GetSchemaForChain should not contain default prefix %q", evm.DefaultCollectionPrefix)
	}

	if !strings.Contains(arbSchema, "Arbitrum__Mainnet__Block") {
		t.Error("GetSchemaForChain should contain Arbitrum__Mainnet__Block")
	}
}

func TestLoadSchemaDeterministic(t *testing.T) {
	t.Parallel()
	c := evm.NewCollectionNames(evm.DefaultCollectionPrefix)
	s1, err := schema.LoadSchemaSDL(c)
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}
	s2, err := schema.LoadSchemaSDL(c)
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}
	if s1 != s2 {
		t.Error("LoadSchemaSDL() should produce identical output on repeated calls")
	}
}

func TestLoadSchemaSDL_NotEmpty(t *testing.T) {
	t.Parallel()
	s, err := schema.LoadSchemaSDL(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}
	if s == "" {
		t.Fatal("LoadSchemaSDL() returned empty string")
	}
}

func TestLoadSchemaSDLForChain_DefaultPrefix(t *testing.T) {
	t.Parallel()
	sdl, err := schema.LoadSchemaSDLForChain(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() failed: %v", err)
	}
	if sdl == "" {
		t.Fatal("LoadSchemaSDLForChain() returned empty string")
	}
	if !strings.Contains(sdl, evm.DefaultCollectionPrefix+"__Block") {
		t.Error("schema should contain default Block type")
	}
}

func TestLoadSchemaSDLForChain_CustomPrefix(t *testing.T) {
	t.Parallel()
	sdl, err := schema.LoadSchemaSDLForChain(evm.NewCollectionNames("Arbitrum__Mainnet"))
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() failed: %v", err)
	}
	if strings.Contains(sdl, evm.DefaultCollectionPrefix) {
		t.Error("schema with custom prefix should not contain default prefix")
	}
	if !strings.Contains(sdl, "Arbitrum__Mainnet__Block") {
		t.Error("schema should contain Arbitrum__Mainnet__Block")
	}
}
