package evm

import (
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// DefaultCollectionPrefix is the default collection prefix for backward compatibility.
const DefaultCollectionPrefix = "Ethereum__Mainnet"

// Collection name constants for the default Ethereum Mainnet chain.
const (
	DefaultCollectionBlock             = DefaultCollectionPrefix + "__Block"
	DefaultCollectionTransaction       = DefaultCollectionPrefix + "__Transaction"
	DefaultCollectionLog               = DefaultCollectionPrefix + "__Log"
	DefaultCollectionAccessListEntry   = DefaultCollectionPrefix + "__AccessListEntry"
	DefaultCollectionBlockSignature    = DefaultCollectionPrefix + "__BlockSignature"
	DefaultCollectionSnapshotSignature = DefaultCollectionPrefix + "__SnapshotSignature"
)

// CollectionNames holds the dynamically generated EVM collection names for a
// chain.
//
// It implements chains.Collections for the generic schema loader and future
// generic BlockHandler.
//
// Every collection except SnapshotSignature must declare an @index on its
// block-number field (number on Block, blockNumber elsewhere): the pruner
// orders and filters by it, and GetDocIDsByBlockRange queries it. Enforced by
// pruner.TestSchemaIndexesBlockNumberField.
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

// SchemaApplyOrder returns collection type names in dependency-safe order
// for per-file AddSchema calls.
func (c *CollectionNames) SchemaApplyOrder() []string {
	return []string{
		c.Block,
		c.BlockSignature,
		c.SnapshotSignature,
		c.Transaction,
		c.AccessListEntry,
		c.Log,
	}
}

// CollectionFileForType maps a collection type name to its .graphql filename.
// e.g. "Ethereum__Mainnet__Block" → "block.graphql"
// Returns empty string if the type name does not match this chain's prefix.
func (c *CollectionNames) CollectionFileForType(typeName string) string {
	prefix := c.prefix + "__"
	suffix := strings.TrimPrefix(typeName, prefix)
	if suffix == typeName {
		return ""
	}
	return strings.ToLower(suffix[:1]) + suffix[1:] + ".graphql"
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
		DefaultCollectionAccessListEntry,
		DefaultCollectionLog,
	}
}

// SchemaApplyOrder returns collection type names in dependency-safe order
// for per-file AddSchema calls, using the default prefix.
func SchemaApplyOrder() []string {
	return []string{
		DefaultCollectionBlock,
		DefaultCollectionBlockSignature,
		DefaultCollectionSnapshotSignature,
		DefaultCollectionTransaction,
		DefaultCollectionAccessListEntry,
		DefaultCollectionLog,
	}
}
