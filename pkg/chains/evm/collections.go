package evm

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// DefaultCollectionPrefix is the default collection prefix for backward compatibility.
const DefaultCollectionPrefix = "Ethereum__Mainnet"

// Collection name constants for the default Ethereum Mainnet chain.
const (
	CollectionBlock             = DefaultCollectionPrefix + "__Block"
	CollectionTransaction       = DefaultCollectionPrefix + "__Transaction"
	CollectionLog               = DefaultCollectionPrefix + "__Log"
	CollectionAccessListEntry   = DefaultCollectionPrefix + "__AccessListEntry"
	CollectionBlockSignature    = DefaultCollectionPrefix + "__BlockSignature"
	CollectionSnapshotSignature = DefaultCollectionPrefix + "__SnapshotSignature"
)

// CollectionNames holds the dynamically generated EVM collection names for a
// chain.
//
// It implements chains.Collections for the generic schema loader and future
// generic BlockHandler.
type CollectionNames struct {
	prefix            string
	Block             string
	BlockSignature    string
	SnapshotSignature string
	Transaction       string
	AccessListEntry   string
	Log               string
}

// Compile-time guarantee that CollectionNames implements chains.Collections.
var _ chains.Collections = (*CollectionNames)(nil)

// NewCollectionNames creates EVM collection names using the given prefix
// (e.g. "Arbitrum__Mainnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		prefix:            prefix,
		Block:             fmt.Sprintf("%s__Block", prefix),
		BlockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:       fmt.Sprintf("%s__Transaction", prefix),
		AccessListEntry:   fmt.Sprintf("%s__AccessListEntry", prefix),
		Log:               fmt.Sprintf("%s__Log", prefix),
	}
}

// Prefix returns the chain prefix (e.g. "Ethereum__Mainnet").
func (c *CollectionNames) Prefix() string {
	return c.prefix
}

// AllCollections returns all collection names as a slice in P2P filter order.
func (c *CollectionNames) AllCollections() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.AccessListEntry,
		c.Log,
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

// DefaultCollections returns all default collection names as a slice.
func DefaultCollections() []string {
	return []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
	}
}
