package solana

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// DefaultCollectionPrefix is the default collection prefix for the Solana
// adapter.
const DefaultCollectionPrefix = "Solana__Mainnet"

// Collection name constants for the default Solana Mainnet chain.
const (
	DefaultCollectionBlock              = DefaultCollectionPrefix + "__Block"
	DefaultCollectionBlockSignature     = DefaultCollectionPrefix + "__BlockSignature"
	DefaultCollectionSnapshotSignature  = DefaultCollectionPrefix + "__SnapshotSignature"
	DefaultCollectionTransaction        = DefaultCollectionPrefix + "__Transaction"
	DefaultCollectionInstruction        = DefaultCollectionPrefix + "__Instruction"
	DefaultCollectionTokenBalanceChange = DefaultCollectionPrefix + "__TokenBalanceChange"
	DefaultCollectionReward             = DefaultCollectionPrefix + "__Reward"
)

// CollectionNames holds the dynamically generated Solana collection names for
// a chain.
//
// It implements chains.Collections for the generic schema loader, BlockHandler,
// reorg roller, pruner, and snapshotter.
//
// Every collection except SnapshotSignature must declare an @index on its
// block-number field (number on Block, blockNumber elsewhere): the pruner
// orders and filters by it, and GetDocIDsByBlockRange queries it. Enforced by
// pruner.TestSchemaIndexesBlockNumberField.
type CollectionNames struct {
	prefix             string
	Block              string
	BlockSignature     string
	SnapshotSignature  string
	Transaction        string
	Instruction        string
	TokenBalanceChange string
	Reward             string
}

// Compile-time guarantee that CollectionNames implements chains.Collections.
var _ chains.Collections = (*CollectionNames)(nil)

// NewCollectionNames creates Solana collection names using the given prefix
// (e.g. "Solana__Devnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		prefix:             prefix,
		Block:              fmt.Sprintf("%s__Block", prefix),
		BlockSignature:     fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature:  fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:        fmt.Sprintf("%s__Transaction", prefix),
		Instruction:        fmt.Sprintf("%s__Instruction", prefix),
		TokenBalanceChange: fmt.Sprintf("%s__TokenBalanceChange", prefix),
		Reward:             fmt.Sprintf("%s__Reward", prefix),
	}
}

// Prefix returns the chain prefix (e.g. "Solana__Mainnet").
func (c *CollectionNames) Prefix() string {
	return c.prefix
}

// AllCollections returns all collection names as a slice in P2P filter order.
//
// The order is load-bearing beyond P2P: the reorg roller deletes
// dependents-first by reversing it with the block collection last, and the
// schema apply order follows the same sequence (block first so later files
// resolve its relations).
func (c *CollectionNames) AllCollections() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.Instruction,
		c.TokenBalanceChange,
		c.Reward,
	}
}

// BlockCollection implements chains.Collections. It returns the collection
// name that stores block documents.
func (c *CollectionNames) BlockCollection() string {
	return c.Block
}

// SnapshotSignatureCollection implements chains.Collections. It returns the
// collection name that stores snapshot signature documents.
func (c *CollectionNames) SnapshotSignatureCollection() string {
	return c.SnapshotSignature
}

// BlockSignatureCollection implements chains.Collections. It returns the
// collection name that stores block signature documents.
func (c *CollectionNames) BlockSignatureCollection() string {
	return c.BlockSignature
}

// DefaultCollections returns all default collection names as a slice.
func DefaultCollections() []string {
	return []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionInstruction,
		DefaultCollectionTokenBalanceChange,
		DefaultCollectionReward,
	}
}
