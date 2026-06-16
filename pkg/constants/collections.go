package constants

import (
	"fmt"
	"strings"
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

// SchemaApplyOrder returns collection type names in dependency-safe order
// for per-file AddSchema calls. Each name maps to a corresponding file in
// pkg/schema/collections/ via the naming convention:
//
//	Ethereum__Mainnet__Block → block.graphql
//
// NOTE: This is distinct from AllCollections() which is used for P2P
// collection filtering and may have a different order.
// Changing fields on already-existing collections is NOT supported by
// AddSchema — use purge_and_apply.sh or future schema patch/Lens migrations.
func SchemaApplyOrder() []string {
	return []string{
		CollectionBlock,
		CollectionBlockSignature,
		CollectionSnapshotSignature,
		CollectionTransaction,
		CollectionAccessListEntry,
		CollectionLog,
	}
}

// CollectionFileForType maps a collection type name to its .graphql filename.
// e.g. "Ethereum__Mainnet__Block" → "block.graphql"
// Returns empty string if the type name does not match the default prefix.
func CollectionFileForType(typeName string) string {
	prefix := DefaultCollectionPrefix + "__"
	suffix := strings.TrimPrefix(typeName, prefix)
	if suffix == typeName {
		return ""
	}
	return strings.ToLower(suffix[:1]) + suffix[1:] + ".graphql"
}
