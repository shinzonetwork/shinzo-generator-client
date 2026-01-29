package constants

// DefraDB Collection Names - matches schema.graphql types for Optimism
const (
	CollectionBlock       = "Optimism__Mainnet__Block"
	CollectionTransaction = "Optimism__Mainnet__Transaction"
	CollectionLog         = "Optimism__Mainnet__Log"
)

// Collection name slice for bulk operations
var AllCollections = []string{
	CollectionBlock,
	CollectionTransaction,
	CollectionLog,
}