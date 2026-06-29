package schema

import (
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	for i, e := range entries {
		assert.Equal(t, expectedNames[i], e.Name)
		assert.Equal(t, expectedTypeNames[i], e.TypeName)
	}
}

func TestListCollections_DefaultPrefix(t *testing.T) {
	t.Parallel()

	entries := ListCollections(constants.DefaultCollectionPrefix)

	expectedTypeNames := constants.SchemaApplyOrder()
	assert.Len(t, entries, len(expectedTypeNames))

	for i, e := range entries {
		assert.Equal(t, expectedTypeNames[i], e.TypeName)
	}
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

	cache, err := PrecomputeCollectionSDLs(constants.DefaultCollectionPrefix)
	require.NoError(t, err)

	assert.NotEmpty(t, cache)

	knownCollections := []string{"block", "transaction", "log", "blockSignature", "snapshotSignature", "accessListEntry"}
	for _, name := range knownCollections {
		assert.Contains(t, cache, name)
		assert.NotEmpty(t, cache[name])
	}
}

func TestPrecomputeCollectionSDLs_KeysMatchValidCollections(t *testing.T) {
	t.Parallel()

	cache, err := PrecomputeCollectionSDLs("Ethereum__Mainnet")
	require.NoError(t, err)

	for _, name := range []string{"block", "transaction", "log"} {
		assert.Contains(t, cache, name)
	}

	assert.NotContains(t, cache, "nonexistent")
}

func TestPrecomputeCollectionSDLs_PrefixReplacement(t *testing.T) {
	t.Parallel()

	prefix := "Arbitrum__Sepolia"
	cache, err := PrecomputeCollectionSDLs(prefix)
	require.NoError(t, err)

	sdl, ok := cache["block"]
	assert.True(t, ok, "expected block entry in cache")
	assert.Contains(t, sdl, prefix, "SDL should contain the chain prefix")
	assert.NotContains(t, sdl, constants.DefaultCollectionPrefix, "SDL should not contain default prefix")
}

func TestPrecomputeCollectionSDLs_EmptyPrefixReturnsError(t *testing.T) {
	t.Parallel()

	cache, err := PrecomputeCollectionSDLs("")
	require.Error(t, err)
	assert.Nil(t, cache)
	assert.ErrorIs(t, err, ErrEmptyPrefix)
}
