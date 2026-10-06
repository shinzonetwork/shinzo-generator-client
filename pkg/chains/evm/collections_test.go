package evm

import (
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionConstants(t *testing.T) {
	t.Parallel()
	prefix := DefaultCollectionPrefix
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{"Block", DefaultCollectionBlock, prefix + "__Block"},
		{"Transaction", DefaultCollectionTransaction, prefix + "__Transaction"},
		{"Log", DefaultCollectionLog, prefix + "__Log"},
		{"AccessListEntry", DefaultCollectionAccessListEntry, prefix + "__AccessListEntry"},
		{"BlockSignature", DefaultCollectionBlockSignature, prefix + "__BlockSignature"},
		{"SnapshotSignature", DefaultCollectionSnapshotSignature, prefix + "__SnapshotSignature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.constant)
		})
	}
}

func TestDefaultCollections(t *testing.T) {
	t.Parallel()

	collections := DefaultCollections()

	require.NotNil(t, collections)
	require.Len(t, collections, 6)

	expected := []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionAccessListEntry,
		DefaultCollectionLog,
	}
	assert.Equal(t, expected, collections)
}

func TestNewCollectionNames(t *testing.T) {
	t.Parallel()

	c := NewCollectionNames("Arbitrum__Mainnet")

	assert.Equal(t, "Arbitrum__Mainnet", c.Prefix())
	assert.Equal(t, "Arbitrum__Mainnet__Block", c.Block)
	assert.Equal(t, "Arbitrum__Mainnet__BlockSignature", c.BlockSignature)
	assert.Equal(t, "Arbitrum__Mainnet__SnapshotSignature", c.SnapshotSignature)
	assert.Equal(t, "Arbitrum__Mainnet__Transaction", c.Transaction)
	assert.Equal(t, "Arbitrum__Mainnet__AccessListEntry", c.AccessListEntry)
	assert.Equal(t, "Arbitrum__Mainnet__Log", c.Log)
}

func TestAllCollections(t *testing.T) {
	t.Parallel()

	c := NewCollectionNames(DefaultCollectionPrefix)
	collections := c.AllCollections()

	require.Len(t, collections, 6)

	expected := []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionAccessListEntry,
		DefaultCollectionLog,
	}
	assert.Equal(t, expected, collections)
}

func TestBlockAndSignatureCollections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		prefix                 string
		expectedBlockCol       string
		expectedBlockSigCol    string
		expectedSnapshotSigCol string
	}{
		{"DefaultPrefix", DefaultCollectionPrefix, DefaultCollectionBlock, DefaultCollectionBlockSignature, DefaultCollectionSnapshotSignature},
		{"CustomPrefix", "Arbitrum__Mainnet", "Arbitrum__Mainnet__Block", "Arbitrum__Mainnet__BlockSignature", "Arbitrum__Mainnet__SnapshotSignature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c chains.Collections = NewCollectionNames(tt.prefix)
			assert.Equal(t, tt.expectedBlockCol, c.BlockCollection())
			assert.Equal(t, tt.expectedBlockSigCol, c.BlockSignatureCollection())
			assert.Equal(t, tt.expectedSnapshotSigCol, c.SnapshotSignatureCollection())
		})
	}
}
