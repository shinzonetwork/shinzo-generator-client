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

	assert.Len(t, entries, len(expectedNames))

	var names, typeNames []string
	for _, e := range entries {
		names = append(names, e.Name)
		typeNames = append(typeNames, e.TypeName)
	}

	assert.Equal(t, expectedNames, names)
	assert.Equal(t, expectedTypeNames, typeNames)
}

func TestListCollections_DefaultPrefix(t *testing.T) {
	t.Parallel()

	entries := ListCollections(constants.DefaultCollectionPrefix)

	order := constants.SchemaApplyOrder()
	assert.Len(t, entries, len(order))

	var typeNames []string
	for _, e := range entries {
		typeNames = append(typeNames, e.TypeName)
	}

	assert.Equal(t, order, typeNames)
}

func TestLoadCollectionSDLForChain_EmptyPrefix(t *testing.T) {
	t.Parallel()

	_, err := LoadCollectionSDLForChain("block.graphql", "")
	assert.ErrorIs(t, err, ErrEmptyPrefix)
}

func TestLoadSchemaSDLForChain_EmptyPrefix(t *testing.T) {
	t.Parallel()

	_, err := LoadSchemaSDLForChain("")
	assert.ErrorIs(t, err, ErrEmptyPrefix)
}

func TestPrecomputeCollectionSDLs_DefaultPrefix(t *testing.T) {
	t.Parallel()

	cache := PrecomputeCollectionSDLs(constants.DefaultCollectionPrefix)

	assert.NotEmpty(t, cache)

	knownCollections := []string{"block", "transaction", "log", "blockSignature", "snapshotSignature", "accessListEntry"}
	for _, name := range knownCollections {
		assert.Contains(t, cache, name)
		assert.NotEmpty(t, cache[name])
	}
}

func TestPrecomputeCollectionSDLs_KeysMatchValidCollections(t *testing.T) {
	t.Parallel()

	cache := PrecomputeCollectionSDLs("Ethereum__Mainnet")

	for _, name := range []string{"block", "transaction", "log"} {
		assert.Contains(t, cache, name)
	}

	assert.NotContains(t, cache, "nonexistent")
}

func TestPrecomputeCollectionSDLs_PrefixReplacement(t *testing.T) {
	t.Parallel()

	prefix := "Arbitrum__Sepolia"
	cache := PrecomputeCollectionSDLs(prefix)

	sdl, ok := cache["block"]
	assert.True(t, ok, "expected block entry in cache")
	assert.Contains(t, sdl, prefix, "SDL should contain the chain prefix")
	assert.NotContains(t, sdl, constants.DefaultCollectionPrefix, "SDL should not contain default prefix")
}
