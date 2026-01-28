package constants

// DefraDB Collection Names - matches schema.graphql types for Arbitrum
const (
	CollectionBlock       = "Arbitrum__Mainnet__Block"
	CollectionTransaction = "Arbitrum__Mainnet__Transaction"
	CollectionLog         = "Arbitrum__Mainnet__Log"
)

// Collection name slice for bulk operations
var AllCollections = []string{
	CollectionBlock,
	CollectionTransaction,
	CollectionLog,
}