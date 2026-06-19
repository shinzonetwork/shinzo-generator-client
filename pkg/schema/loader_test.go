package schema

import (
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/stretchr/testify/assert"
)

func TestListCollections(t *testing.T) {
	t.Parallel()

	entries := ListCollections("Arbitrum__Sepolia")

	expectedNames := []string{"block", "blockSignature", "snapshotSignature", "transaction", "accessListEntry", "log"}
	expectedTypeNames := []string{
		"Arbitrum__Sepolia__Block",
		"Arbitrum__Sepolia__BlockSignature",
		"Arbitrum__Sepolia__SnapshotSignature",
		"Arbitrum__Sepolia__Transaction",
		"Arbitrum__Sepolia__AccessListEntry",
		"Arbitrum__Sepolia__Log",
	}

	if len(entries) != len(expectedNames) {
		t.Fatalf("expected %d entries, got %d", len(expectedNames), len(entries))
	}

	for i, e := range entries {
		if e.Name != expectedNames[i] {
			t.Errorf("entries[%d].Name = %q, want %q", i, e.Name, expectedNames[i])
		}
		if e.TypeName != expectedTypeNames[i] {
			t.Errorf("entries[%d].TypeName = %q, want %q", i, e.TypeName, expectedTypeNames[i])
		}
	}
}

func TestListCollections_DefaultPrefix(t *testing.T) {
	t.Parallel()

	entries := ListCollections(constants.DefaultCollectionPrefix)

	order := constants.SchemaApplyOrder()
	if len(entries) != len(order) {
		t.Fatalf("expected %d entries, got %d", len(order), len(entries))
	}

	for i, e := range entries {
		if e.TypeName != order[i] {
			t.Errorf("entries[%d].TypeName = %q, want %q (SchemaApplyOrder()[%d])", i, e.TypeName, order[i], i)
		}
	}
}

func TestLoadCollectionSDLForChain_EmptyPrefix(t *testing.T) {
	t.Parallel()

	_, err := LoadCollectionSDLForChain("block.graphql", "")
	assert.EqualError(t, err, "prefix must not be empty")
}

func TestLoadSchemaSDLForChain_EmptyPrefix(t *testing.T) {
	t.Parallel()

	_, err := LoadSchemaSDLForChain("")
	assert.EqualError(t, err, "prefix must not be empty")
}

func TestPrecomputeCollectionSDLs_DefaultPrefix(t *testing.T) {
	t.Parallel()

	cache := PrecomputeCollectionSDLs(constants.DefaultCollectionPrefix)

	if len(cache) == 0 {
		t.Fatal("expected non-empty cache for default prefix")
	}

	knownCollections := []string{"block", "transaction", "log", "blockSignature", "snapshotSignature", "accessListEntry"}
	for _, name := range knownCollections {
		sdl, ok := cache[name]
		if !ok {
			t.Errorf("expected cache entry for %q, not found", name)
			continue
		}
		if sdl == "" {
			t.Errorf("expected non-empty SDL for %q", name)
		}
	}
}

func TestPrecomputeCollectionSDLs_KeysMatchValidCollections(t *testing.T) {
	t.Parallel()

	cache := PrecomputeCollectionSDLs("Ethereum__Mainnet")

	for _, name := range []string{"block", "transaction", "log"} {
		if _, ok := cache[name]; !ok {
			t.Errorf("expected cache key %q", name)
		}
	}

	if _, ok := cache["nonexistent"]; ok {
		t.Error("did not expect cache key for nonexistent collection")
	}
}

func TestPrecomputeCollectionSDLs_PrefixReplacement(t *testing.T) {
	t.Parallel()

	prefix := "Arbitrum__Sepolia"
	cache := PrecomputeCollectionSDLs(prefix)

	if sdl, ok := cache["block"]; ok {
		assert.Contains(t, sdl, prefix, "SDL should contain the chain prefix")
		assert.NotContains(t, sdl, constants.DefaultCollectionPrefix, "SDL should not contain default prefix")
	}
}
