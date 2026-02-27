package constants

// DefraDB Collection Names - matches schema.graphql types
const (
	CollectionBlock           = "Ethereum__Mainnet__Block"
	CollectionTransaction     = "Ethereum__Mainnet__Transaction"
	CollectionLog             = "Ethereum__Mainnet__Log"
	CollectionAccessListEntry = "Ethereum__Mainnet__AccessListEntry"
	CollectionBlockSignature    = "Ethereum__Mainnet__BlockSignature"
	CollectionSnapshotSignature = "Ethereum__Mainnet__SnapshotSignature"
)

// Collection name slice for bulk operations
var AllCollections = []string{
	CollectionBlock,
	CollectionTransaction,
	CollectionAccessListEntry,
	CollectionLog,
	CollectionBlockSignature,
	CollectionSnapshotSignature,
}
