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

func TestAllGraphQLFilesListedInConstants(t *testing.T) {
	t.Parallel()
	entries, err := collectionFS.ReadDir("collections")
	if err != nil {
		t.Fatalf("failed to read collections directory: %v", err)
	}

	files, err := ListCollectionFiles()
	if err != nil {
		t.Fatalf("ListCollectionFiles() failed: %v", err)
	}
	manifestSet := make(map[string]bool)
	for _, f := range files {
		manifestSet[f] = true
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".graphql") {
			continue
		}
		if !manifestSet[e.Name()] {
			t.Errorf("collections/%s exists on disk but is not listed in ListCollectionFiles() — add it to constants.SchemaApplyOrder()", e.Name())
		}
	}

	for _, f := range files {
		data, err := collectionFS.ReadFile("collections/" + f)
		if err != nil {
			t.Errorf("ListCollectionFiles() lists %s but no such file exists in collections/", f)
		}
		if len(data) > 0 && strings.TrimSpace(string(data)) == "" {
			t.Errorf("collections/%s is empty", f)
		}
	}
}

func TestLoadSchemaSDLForChain_DefaultPrefix(t *testing.T) {
	t.Parallel()
	sdl, err := LoadSchemaSDLForChain(constants.DefaultCollectionPrefix)
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() failed: %v", err)
	}
	if sdl == "" {
		t.Fatal("LoadSchemaSDLForChain() returned empty string")
	}
	if !strings.Contains(sdl, constants.DefaultCollectionPrefix+"__Block") {
		t.Error("schema should contain default Block type")
	}
}

func TestLoadSchemaSDLForChain_CustomPrefix(t *testing.T) {
	t.Parallel()
	sdl, err := LoadSchemaSDLForChain("Arbitrum__Mainnet")
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() failed: %v", err)
	}
	if strings.Contains(sdl, constants.DefaultCollectionPrefix) {
		t.Error("schema with custom prefix should not contain default prefix")
	}
	if !strings.Contains(sdl, "Arbitrum__Mainnet__Block") {
		t.Error("schema should contain Arbitrum__Mainnet__Block")
	}
}
