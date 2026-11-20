package solana

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
		{"Instruction", DefaultCollectionInstruction, prefix + "__Instruction"},
		{"TokenBalanceChange", DefaultCollectionTokenBalanceChange, prefix + "__TokenBalanceChange"},
		{"Reward", DefaultCollectionReward, prefix + "__Reward"},
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
	require.Len(t, collections, 7)

	expected := []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionInstruction,
		DefaultCollectionTokenBalanceChange,
		DefaultCollectionReward,
	}
	assert.Equal(t, expected, collections)
}

func TestNewCollectionNames(t *testing.T) {
	t.Parallel()

	c := NewCollectionNames("Solana__Devnet")

	assert.Equal(t, "Solana__Devnet", c.Prefix())
	assert.Equal(t, "Solana__Devnet__Block", c.Block)
	assert.Equal(t, "Solana__Devnet__BlockSignature", c.BlockSignature)
	assert.Equal(t, "Solana__Devnet__SnapshotSignature", c.SnapshotSignature)
	assert.Equal(t, "Solana__Devnet__Transaction", c.Transaction)
	assert.Equal(t, "Solana__Devnet__Instruction", c.Instruction)
	assert.Equal(t, "Solana__Devnet__TokenBalanceChange", c.TokenBalanceChange)
	assert.Equal(t, "Solana__Devnet__Reward", c.Reward)
}

func TestAllCollections(t *testing.T) {
	t.Parallel()

	c := NewCollectionNames(DefaultCollectionPrefix)
	collections := c.AllCollections()

	require.Len(t, collections, 7)

	expected := []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionInstruction,
		DefaultCollectionTokenBalanceChange,
		DefaultCollectionReward,
	}
	assert.Equal(t, expected, collections)
}

func TestAllCollections_BlockFirst(t *testing.T) {
	t.Parallel()

	// The reorg roller orders deletions dependents-first by reversing
	// AllCollections(), so the block collection is deleted last. Block must
	// therefore be the first entry in AllCollections order.
	c := NewCollectionNames(DefaultCollectionPrefix)
	collections := c.AllCollections()

	require.Len(t, collections, 7)
	assert.Equal(t, c.Block, collections[0],
		"Block must be the first entry in AllCollections order: reversal makes it last for deletion")
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
		{"CustomPrefix", "Solana__Devnet", "Solana__Devnet__Block", "Solana__Devnet__BlockSignature", "Solana__Devnet__SnapshotSignature"},
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
