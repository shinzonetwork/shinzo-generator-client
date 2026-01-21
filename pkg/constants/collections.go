package constants

// DefraDB Collection Names - matches schema.graphql types
const (
	CollectionBlock           = "Ethereum__Mainnet__Block"
	CollectionTransaction     = "Ethereum__Mainnet__Transaction"
	CollectionLog             = "Ethereum__Mainnet__Log"
	CollectionAccessListEntry = "Ethereum__Mainnet__AccessListEntry"
	CollectionBatchSignature  = "Ethereum__Mainnet__BatchSignature"
)

// Collection name slice for bulk operations
var AllCollections = []string{
	CollectionBlock,
	CollectionTransaction,
	CollectionAccessListEntry,
	CollectionLog,
	CollectionBatchSignature,
}
