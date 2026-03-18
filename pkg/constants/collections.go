package constants

import "fmt"

// Default collection prefix for backward compatibility
const DefaultCollectionPrefix = "Ethereum__Mainnet"

// CollectionNames holds the dynamically generated collection names for a chain.
type CollectionNames struct {
	Block             string
	Transaction       string
	Log               string
	AccessListEntry   string
	BlockSignature    string
	SnapshotSignature string
}

// NewCollectionNames creates collection names using the given prefix (e.g. "Arbitrum__Mainnet").
func NewCollectionNames(prefix string) *CollectionNames {
	return &CollectionNames{
		Block:             fmt.Sprintf("%s__Block", prefix),
		Transaction:       fmt.Sprintf("%s__Transaction", prefix),
		Log:               fmt.Sprintf("%s__Log", prefix),
		AccessListEntry:   fmt.Sprintf("%s__AccessListEntry", prefix),
		BlockSignature:    fmt.Sprintf("%s__BlockSignature", prefix),
		SnapshotSignature: fmt.Sprintf("%s__SnapshotSignature", prefix),
	}
}

// AllCollections returns all collection names as a slice.
func (c *CollectionNames) AllCollections() []string {
	return []string{
		c.Block,
		c.Transaction,
		c.AccessListEntry,
		c.Log,
		c.BlockSignature,
		c.SnapshotSignature,
	}
}

// Default collection names for backward compatibility.
// These are used when no chain config is specified.
var (
	CollectionBlock             = fmt.Sprintf("%s__Block", DefaultCollectionPrefix)
	CollectionTransaction       = fmt.Sprintf("%s__Transaction", DefaultCollectionPrefix)
	CollectionLog               = fmt.Sprintf("%s__Log", DefaultCollectionPrefix)
	CollectionAccessListEntry   = fmt.Sprintf("%s__AccessListEntry", DefaultCollectionPrefix)
	CollectionBlockSignature    = fmt.Sprintf("%s__BlockSignature", DefaultCollectionPrefix)
	CollectionSnapshotSignature = fmt.Sprintf("%s__SnapshotSignature", DefaultCollectionPrefix)

	AllCollections = []string{
		CollectionBlock,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
	}
)
