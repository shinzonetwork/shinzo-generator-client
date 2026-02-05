package constants

import (
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
)

// DefraDB Collection Names - matches schema.graphql types
// These are the default Ethereum Mainnet collection names for backward compatibility
const (
	CollectionBlock           = "Ethereum__Mainnet__Block"
	CollectionTransaction     = "Ethereum__Mainnet__Transaction"
	CollectionLog             = "Ethereum__Mainnet__Log"
	CollectionAccessListEntry = "Ethereum__Mainnet__AccessListEntry"
	CollectionBatchSignature  = "Ethereum__Mainnet__BatchSignature"
)

// Collection name slice for bulk operations (Ethereum Mainnet)
var AllCollections = []string{
	CollectionBlock,
	CollectionTransaction,
	CollectionAccessListEntry,
	CollectionLog,
	CollectionBatchSignature,
}

// GetCollectionsForChain returns collection names for a specific chain and network
func GetCollectionsForChain(chainType chains.ChainType, network chains.NetworkType) []string {
	switch chainType {
	case chains.ChainTypeEthereum:
		return []string{
			chains.GenerateCollectionName(chainType, network, "Block"),
			chains.GenerateCollectionName(chainType, network, "Transaction"),
			chains.GenerateCollectionName(chainType, network, "Log"),
			chains.GenerateCollectionName(chainType, network, "AccessListEntry"),
			chains.GenerateCollectionName(chainType, network, "BatchSignature"),
		}
	case chains.ChainTypeSolana:
		return []string{
			chains.GenerateCollectionName(chainType, network, "Slot"),
			chains.GenerateCollectionName(chainType, network, "Transaction"),
			chains.GenerateCollectionName(chainType, network, "Instruction"),
			chains.GenerateCollectionName(chainType, network, "BatchSignature"),
		}
	default:
		return AllCollections
	}
}

// Solana Mainnet collection names
var (
	SolanaCollectionSlot          = chains.GenerateCollectionName(chains.ChainTypeSolana, chains.NetworkMainnet, "Slot")
	SolanaCollectionTransaction   = chains.GenerateCollectionName(chains.ChainTypeSolana, chains.NetworkMainnet, "Transaction")
	SolanaCollectionInstruction   = chains.GenerateCollectionName(chains.ChainTypeSolana, chains.NetworkMainnet, "Instruction")
	SolanaCollectionBatchSignature = chains.GenerateCollectionName(chains.ChainTypeSolana, chains.NetworkMainnet, "BatchSignature")
)

// AllSolanaCollections contains all Solana Mainnet collection names
var AllSolanaCollections = []string{
	SolanaCollectionSlot,
	SolanaCollectionTransaction,
	SolanaCollectionInstruction,
	SolanaCollectionBatchSignature,
}
