package tron

import (
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/stretchr/testify/assert"
)

// TestNewCollectionNames covers name derivation from an arbitrary prefix.
func TestNewCollectionNames(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Tron__Testnet")
	assert.Equal(t, "Tron__Testnet", cols.Prefix())
	assert.Equal(t, "Tron__Testnet__Block", cols.Block)
	assert.Equal(t, "Tron__Testnet__Transaction", cols.Transaction)
	assert.Equal(t, "Tron__Testnet__Log", cols.Log)
	assert.Equal(t, "Tron__Testnet__InternalTransfer", cols.InternalTransfer)
	assert.Equal(t, "Tron__Testnet__BlockSignature", cols.BlockSignature)
	assert.Equal(t, "Tron__Testnet__SnapshotSignature", cols.SnapshotSignature)
}

// TestCollectionNames_AllCollectionsOrder pins the P2P filter order, which
// must match the EVM layout: block, signatures, then data collections.
func TestCollectionNames_AllCollectionsOrder(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	assert.Equal(t, []string{
		cols.Block,
		cols.BlockSignature,
		cols.SnapshotSignature,
		cols.Transaction,
		cols.Log,
		cols.InternalTransfer,
	}, cols.AllCollections())
}

// TestCollectionNames_Accessors covers the single-collection accessors
// required by the chains.Collections interface.
func TestCollectionNames_Accessors(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	assert.Equal(t, cols.Block, cols.BlockCollection())
	assert.Equal(t, cols.BlockSignature, cols.BlockSignatureCollection())
	assert.Equal(t, cols.SnapshotSignature, cols.SnapshotSignatureCollection())
}

// TestDefaultCollections pins the default-prefix set and its order.
func TestDefaultCollections(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionLog,
		DefaultCollectionInternalTransfer,
	}, DefaultCollections())
}

// TestCollectionNames_ImplementsInterface is a compile-time double check on
// the chains.Collections contract.
func TestCollectionNames_ImplementsInterface(t *testing.T) {
	t.Parallel()
	var cols chains.Collections = NewCollectionNames(DefaultCollectionPrefix)
	assert.Equal(t, DefaultCollectionBlock, cols.BlockCollection())
}
