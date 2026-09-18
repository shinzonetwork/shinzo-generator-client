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

func TestSchemaApplyOrder(t *testing.T) {
	t.Parallel()

	order := SchemaApplyOrder()
	require.Len(t, order, 6)

	expected := []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
	}
	assert.Equal(t, expected, order)
}

func TestCollectionFileForType(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	tests := []struct {
		name     string
		typeName string
		expected string
	}{
		{"Block", CollectionBlock, "block.graphql"},
		{"Transaction", CollectionTransaction, "transaction.graphql"},
		{"Log", CollectionLog, "log.graphql"},
		{"AccessListEntry", CollectionAccessListEntry, "accessListEntry.graphql"},
		{"BlockSignature", CollectionBlockSignature, "blockSignature.graphql"},
		{"SnapshotSignature", CollectionSnapshotSignature, "snapshotSignature.graphql"},
		{"UnknownPrefix", "UnknownPrefix__Block", ""},
		{"NoPrefix", "Block", ""},
		{"Empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.CollectionFileForType(tt.typeName)
			assert.Equal(t, tt.expected, got)
		})
	}
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

func TestGetCollection(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames("Ethereum__Mainnet")

	tests := []struct {
		name        string
		role        string
		expected    string
		expectError bool
	}{
		{"block", chains.TypeBlock, "Ethereum__Mainnet__Block", false},
		{"blockSignature", chains.TypeBlockSignature, "Ethereum__Mainnet__BlockSignature", false},
		{"snapshotSignature", chains.TypeSnapshotSignature, "Ethereum__Mainnet__SnapshotSignature", false},
		{"transaction", chains.TypeTransaction, "Ethereum__Mainnet__Transaction", false},
		{"accessListEntry", chains.TypeAccessListEntry, "Ethereum__Mainnet__AccessListEntry", false},
		{"log", chains.TypeLog, "Ethereum__Mainnet__Log", false},
		{"unknown", "unknown", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.GetCollection(tt.role)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, got)
			}
		})
	}
}
