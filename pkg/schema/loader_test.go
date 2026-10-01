package schema

import (
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListCollections(t *testing.T) {
	t.Parallel()

	entries, err := ListCollections(evm.NewCollectionNames("Arbitrum__Sepolia"))
	require.NoError(t, err)

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

	entries, err := ListCollections(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	require.NoError(t, err)

	expectedTypeNames := evm.DefaultCollections()
	assert.Len(t, entries, len(expectedTypeNames))

	for i, e := range entries {
		assert.Equal(t, expectedTypeNames[i], e.TypeName)
	}
}

func TestPrecomputeCollectionSDLs_DefaultPrefix(t *testing.T) {
	t.Parallel()

	cache, err := PrecomputeCollectionSDLs(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
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

	cache, err := PrecomputeCollectionSDLs(evm.NewCollectionNames("Ethereum__Mainnet"))
	require.NoError(t, err)

	for _, name := range []string{"block", "transaction", "log"} {
		assert.Contains(t, cache, name)
	}

	assert.NotContains(t, cache, "nonexistent")
}

func TestPrecomputeCollectionSDLs_PrefixReplacement(t *testing.T) {
	t.Parallel()

	prefix := "Arbitrum__Sepolia"
	cache, err := PrecomputeCollectionSDLs(evm.NewCollectionNames(prefix))
	require.NoError(t, err)

	sdl, ok := cache["block"]
	assert.True(t, ok, "expected block entry in cache")
	assert.Contains(t, sdl, prefix, "SDL should contain the chain prefix")
	assert.NotContains(t, sdl, evm.DefaultCollectionPrefix, "SDL should not contain default prefix")
}

func TestLoadSchemaSDLForChain_DefaultPrefix(t *testing.T) {
	sdl, err := LoadSchemaSDLForChain(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
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

func TestLoadSchemaSDLForChain_ReplacesPrefix(t *testing.T) {
	defaultSchema, err := LoadSchemaSDLForChain(evm.NewCollectionNames(evm.DefaultCollectionPrefix))
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() error: %v", err)
	}
	arbSchema, err := LoadSchemaSDLForChain(evm.NewCollectionNames("Arbitrum__Mainnet"))
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() error: %v", err)
	}

	if arbSchema == defaultSchema {
		t.Fatal("LoadSchemaSDLForChain should produce different output for different prefix")
	}

	if strings.Contains(arbSchema, evm.DefaultCollectionPrefix) {
		t.Errorf("LoadSchemaSDLForChain should not contain default prefix %q", evm.DefaultCollectionPrefix)
	}

	if !strings.Contains(arbSchema, "Arbitrum__Mainnet__Block") {
		t.Error("LoadSchemaSDLForChain should contain Arbitrum__Mainnet__Block")
	}
}

func TestLoadSchemaSDLForChain_Deterministic(t *testing.T) {
	c := evm.NewCollectionNames(evm.DefaultCollectionPrefix)
	s1, err := LoadSchemaSDLForChain(c)
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() failed: %v", err)
	}
	s2, err := LoadSchemaSDLForChain(c)
	if err != nil {
		t.Fatalf("LoadSchemaSDLForChain() failed: %v", err)
	}
	if s1 != s2 {
		t.Error("LoadSchemaSDLForChain() should produce identical output on repeated calls")
	}
}
