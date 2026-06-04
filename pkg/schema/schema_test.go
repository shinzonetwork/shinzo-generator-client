//go:build !branchable

package schema

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

func TestGetSchema(t *testing.T) {
	t.Parallel()
	s := GetSchema()
	if s == "" {
		t.Fatal("GetSchema() returned empty string")
	}
	expectedType := constants.DefaultCollectionPrefix + "__Block"
	if !strings.Contains(s, expectedType) {
		t.Errorf("schema should contain %s type", expectedType)
	}
}

func TestLoadSchemaContainsAllCollectionTypes(t *testing.T) {
	t.Parallel()
	s := GetSchema()

	expectedTypes := constants.DefaultCollections()
	for _, typeName := range expectedTypes {
		if !strings.Contains(s, typeName) {
			t.Errorf("schema missing expected type: %s", typeName)
		}
	}
}

func TestGetSchemaForChain_ReplacesPrefix(t *testing.T) {
	t.Parallel()
	defaultSchema := GetSchema()
	arbSchema := GetSchemaForChain("Arbitrum__Mainnet")

	if arbSchema == defaultSchema {
		t.Fatal("GetSchemaForChain should produce different output for different prefix")
	}

	if strings.Contains(arbSchema, constants.DefaultCollectionPrefix) {
		t.Errorf("GetSchemaForChain should not contain default prefix %q", constants.DefaultCollectionPrefix)
	}

	if !strings.Contains(arbSchema, "Arbitrum__Mainnet__Block") {
		t.Error("GetSchemaForChain should contain Arbitrum__Mainnet__Block")
	}
}

func TestLoadSchemaDeterministic(t *testing.T) {
	t.Parallel()
	s1, err := LoadSchemaSDL()
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}
	s2, err := LoadSchemaSDL()
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}
	if s1 != s2 {
		t.Error("LoadSchemaSDL() should produce identical output on repeated calls")
	}
}

func TestLoadSchemaSDL_NotEmpty(t *testing.T) {
	t.Parallel()
	s, err := LoadSchemaSDL()
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}
	if s == "" {
		t.Fatal("LoadSchemaSDL() returned empty string")
	}
}

func TestLoadSchemaMatchesLegacyCollections(t *testing.T) {
	t.Parallel()
	loaded, err := LoadSchemaSDL()
	if err != nil {
		t.Fatalf("LoadSchemaSDL() failed: %v", err)
	}

	legacy := SchemaGraphQL

	loadedNorm := normalizeWhitespace(loaded)
	legacyNorm := normalizeWhitespace(legacy)

	if loadedNorm != legacyNorm {
		t.Errorf("loaded schema does not match legacy schema.graphql\n--- loaded (%d chars) ---\n%s\n--- legacy (%d chars) ---\n%s",
			len(loadedNorm), loadedNorm[:min(200, len(loadedNorm))],
			len(legacyNorm), legacyNorm[:min(200, len(legacyNorm))])
	}
}

func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
