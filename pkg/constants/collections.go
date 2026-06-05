package constants

import "fmt"

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

// CollectionNames holds the dynamically generated collection names for a chain.
type CollectionNames struct {
	Block             string
	BlockSignature    string
	SnapshotSignature string
	Transaction       string
	AccessListEntry   string
	Log               string
}

// NewCollectionNames creates collection names using the given prefix (e.g. "Arbitrum__Mainnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		Block:             fmt.Sprintf("%s__Block", prefix),
		BlockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
		Transaction:       fmt.Sprintf("%s__Transaction", prefix),
		AccessListEntry:   fmt.Sprintf("%s__AccessListEntry", prefix),
		Log:               fmt.Sprintf("%s__Log", prefix),
	}
}

// AllCollections returns all collection names as a slice.
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
