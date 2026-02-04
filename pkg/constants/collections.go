package constants

// DefraDB Collection Names - matches schema.graphql types for Avalanche
const (
	CollectionBlock       = "Avalanche__Mainnet__Block"
	CollectionTransaction = "Avalanche__Mainnet__Transaction"
	CollectionLog         = "Avalanche__Mainnet__Log"
)

// Collection name slice for bulk operations
var AllCollections = []string{
	CollectionBlock,
	CollectionTransaction,
	CollectionLog,
}