package evm

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// DefaultCollectionPrefix is the prefix the embedded SDL files are written with;
// CollectionFiles replaces it with the configured chain's prefix.
const DefaultCollectionPrefix = "Ethereum__Mainnet"

// Collection names for Ethereum Mainnet, built from DefaultCollectionPrefix.
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

// NewCollectionsFromConfig creates the EVM collection names for the chain cfg
// names. It returns config.ErrChainUnset when cfg is nil or has no chain name
// or network, mirroring NewConverter, so no caller gets a prefix it did not
// configure. It is the Collections factory the evm package exposes in its
// Factories() bundle.
func NewCollectionsFromConfig(cfg *config.Config) (chains.Collections, error) {
	prefix, err := chainPrefixFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return NewCollectionNames(prefix), nil
}

// DefaultCollections returns the Ethereum Mainnet collection names as a slice.
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
