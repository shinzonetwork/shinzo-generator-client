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
		{"Block", CollectionBlock, prefix + "__Block"},
		{"Transaction", CollectionTransaction, prefix + "__Transaction"},
		{"Log", CollectionLog, prefix + "__Log"},
		{"AccessListEntry", CollectionAccessListEntry, prefix + "__AccessListEntry"},
		{"BlockSignature", CollectionBlockSignature, prefix + "__BlockSignature"},
		{"SnapshotSignature", CollectionSnapshotSignature, prefix + "__SnapshotSignature"},
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
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
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

	c := NewCollectionNames("Ethereum__Mainnet")
	collections := c.AllCollections()

	require.Len(t, collections, 6)

	expected := []string{
		"Ethereum__Mainnet__Block",
		"Ethereum__Mainnet__BlockSignature",
		"Ethereum__Mainnet__SnapshotSignature",
		"Ethereum__Mainnet__Transaction",
		"Ethereum__Mainnet__AccessListEntry",
		"Ethereum__Mainnet__Log",
	}
	assert.Equal(t, expected, collections)
}

func TestBlockCollection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prefix   string
		expected string
	}{
		{"DefaultPrefix", "Ethereum__Mainnet", "Ethereum__Mainnet__Block"},
		{"CustomPrefix", "Arbitrum__Mainnet", "Arbitrum__Mainnet__Block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c chains.Collections = NewCollectionNames(tt.prefix)
			assert.Equal(t, tt.expected, c.BlockCollection())
		})
	}
}

func TestSnapshotSignatureCollection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		prefix   string
		expected string
	}{
		{"DefaultPrefix", "Ethereum__Mainnet", "Ethereum__Mainnet__SnapshotSignature"},
		{"CustomPrefix", "Arbitrum__Mainnet", "Arbitrum__Mainnet__SnapshotSignature"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c chains.Collections = NewCollectionNames(tt.prefix)
			assert.Equal(t, tt.expected, c.SnapshotSignatureCollection())
		})
	}
}
