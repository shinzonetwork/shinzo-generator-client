package bitcoin

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
		{"TxInput", DefaultCollectionTxInput, prefix + "__TxInput"},
		{"TxOutput", DefaultCollectionTxOutput, prefix + "__TxOutput"},
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
		DefaultCollectionTxInput,
		DefaultCollectionTxOutput,
	}
	assert.Equal(t, expected, collections)
}

func TestNewCollectionNames(t *testing.T) {
	t.Parallel()

	c := NewCollectionNames("Bitcoin__Signet")

	assert.Equal(t, "Bitcoin__Signet", c.Prefix())
	assert.Equal(t, "Bitcoin__Signet__Block", c.Block)
	assert.Equal(t, "Bitcoin__Signet__BlockSignature", c.BlockSignature)
	assert.Equal(t, "Bitcoin__Signet__SnapshotSignature", c.SnapshotSignature)
	assert.Equal(t, "Bitcoin__Signet__Transaction", c.Transaction)
	assert.Equal(t, "Bitcoin__Signet__TxInput", c.TxInput)
	assert.Equal(t, "Bitcoin__Signet__TxOutput", c.TxOutput)
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
		DefaultCollectionTxInput,
		DefaultCollectionTxOutput,
	}
	assert.Equal(t, expected, collections)
}

// TestAllCollections_BlockFirstPinsReorgDeletionOrder pins Block as the
// first AllCollections entry. The reorg roller deletes dependents-first by
// reversing the slice, so the block collection lands last in deletion order
// and is only removed once every document referencing it is gone. A reorder
// here would delete the block collection while its dependents still exist.
func TestAllCollections_BlockFirstPinsReorgDeletionOrder(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{DefaultCollectionPrefix, "Bitcoin__Signet"} {
		c := NewCollectionNames(prefix)
		assert.Equal(t, c.Block, c.AllCollections()[0],
			"Block must be the first AllCollections entry for prefix %q", prefix)
	}
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
		{"CustomPrefix", "Bitcoin__Signet", "Bitcoin__Signet__Block", "Bitcoin__Signet__BlockSignature", "Bitcoin__Signet__SnapshotSignature"},
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
