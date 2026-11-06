package bitcoin

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// DefaultCollectionPrefix is the default collection prefix for the Bitcoin
// Mainnet deployment.
const DefaultCollectionPrefix = "Bitcoin__Mainnet"

// Collection name constants for the default Bitcoin Mainnet chain.
const (
	DefaultCollectionBlock             = DefaultCollectionPrefix + "__Block"
	DefaultCollectionBlockSignature    = DefaultCollectionPrefix + "__BlockSignature"
	DefaultCollectionSnapshotSignature = DefaultCollectionPrefix + "__SnapshotSignature"
	DefaultCollectionTransaction       = DefaultCollectionPrefix + "__Transaction"
	DefaultCollectionTxInput           = DefaultCollectionPrefix + "__TxInput"
	DefaultCollectionTxOutput          = DefaultCollectionPrefix + "__TxOutput"
)

// CollectionNames holds the dynamically generated Bitcoin collection names
// for a chain.
//
// It implements chains.Collections for the generic schema loader and future
// generic BlockHandler.
//
// Every collection except SnapshotSignature must declare an @index on its
// block-number field (number on Block, blockNumber elsewhere): the pruner
// orders and filters by it, and GetDocIDsByBlockRange queries it.
// Transaction is the one carve-out: its block lookups run through the
// indexed blockHash, so its blockNumber stays unindexed. Enforced
// by the package's own schema test until the adapter registers with the
// chain factory registry and the pruner contract test picks it up.
type CollectionNames struct {
	prefix            string
	Block             string
	BlockSignature    string
	SnapshotSignature string
	Transaction       string
	TxInput           string
	TxOutput          string
}

// Compile-time guarantee that CollectionNames implements chains.Collections.
var _ chains.Collections = (*CollectionNames)(nil)

// NewCollectionNames creates Bitcoin collection names using the given prefix
// (e.g. "Bitcoin__Signet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		prefix:            prefix,
		Block:             fmt.Sprintf("%s__Block", prefix),
		BlockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:       fmt.Sprintf("%s__Transaction", prefix),
		TxInput:           fmt.Sprintf("%s__TxInput", prefix),
		TxOutput:          fmt.Sprintf("%s__TxOutput", prefix),
	}
}

// Prefix returns the chain prefix (e.g. "Bitcoin__Mainnet").
func (c *CollectionNames) Prefix() string {
	return c.prefix
}

// AllCollections returns all collection names as a slice in P2P filter
// order.
//
// The order is load-bearing: the reorg roller deletes dependents-first by
// reversing it, so the block collection sits first here and is deleted last
// (it is the collection the other documents reference).
func (c *CollectionNames) AllCollections() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.TxInput,
		c.TxOutput,
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
		DefaultCollectionTxInput,
		DefaultCollectionTxOutput,
	}
}
