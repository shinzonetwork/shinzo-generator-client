package tron

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// Collection name constants for the default Tron Mainnet chain.
const (
	DefaultCollectionBlock             = DefaultCollectionPrefix + "__Block"
	DefaultCollectionTransaction       = DefaultCollectionPrefix + "__Transaction"
	DefaultCollectionLog               = DefaultCollectionPrefix + "__Log"
	DefaultCollectionInternalTransfer  = DefaultCollectionPrefix + "__InternalTransfer"
	DefaultCollectionBlockSignature    = DefaultCollectionPrefix + "__BlockSignature"
	DefaultCollectionSnapshotSignature = DefaultCollectionPrefix + "__SnapshotSignature"
)

// CollectionNames holds the dynamically generated Tron collection names for
// a chain. It implements chains.Collections for the generic schema loader,
// BlockHandler, pruner, and snapshotter.
//
// Every collection except SnapshotSignature must declare an @index on its
// block-number field (number on Block, blockNumber elsewhere): the pruner
// orders and filters by it, and GetDocIDsByBlockRange queries it.
type CollectionNames struct {
	prefix            string
	Block             string
	BlockSignature    string
	SnapshotSignature string
	Transaction       string
	InternalTransfer  string
	Log               string
}

// Compile-time guarantee that CollectionNames implements chains.Collections.
var _ chains.Collections = (*CollectionNames)(nil)

// NewCollectionNames creates Tron collection names using the given prefix
// (e.g. "Tron__Mainnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		prefix:            prefix,
		Block:             fmt.Sprintf("%s__Block", prefix),
		BlockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:       fmt.Sprintf("%s__Transaction", prefix),
		InternalTransfer:  fmt.Sprintf("%s__InternalTransfer", prefix),
		Log:               fmt.Sprintf("%s__Log", prefix),
	}
}

// Prefix returns the chain prefix (e.g. "Tron__Mainnet").
func (c *CollectionNames) Prefix() string {
	return c.prefix
}

// AllCollections returns all collection names as a slice in P2P filter
// order, mirroring the EVM layout.
func (c *CollectionNames) AllCollections() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.Log,
		c.InternalTransfer,
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
		DefaultCollectionLog,
		DefaultCollectionInternalTransfer,
	}
}
