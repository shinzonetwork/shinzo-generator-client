// Package chains provides a unified abstraction layer for multiple blockchain implementations.
// It defines common interfaces that allow the indexer to work with different blockchains
// (Ethereum, Solana, etc.) through a consistent API.
package chains

import (
	"context"
)

// ChainType represents the type of blockchain
type ChainType string

const (
	ChainTypeEthereum ChainType = "ethereum"
	ChainTypeSolana   ChainType = "solana"
)

// NetworkType represents the network (mainnet, testnet, etc.)
type NetworkType string

const (
	NetworkMainnet NetworkType = "mainnet"
	NetworkTestnet NetworkType = "testnet"
	NetworkDevnet  NetworkType = "devnet"
)

// BlockchainClient is the primary interface for interacting with a blockchain.
// Each blockchain implementation (Ethereum, Solana, etc.) must implement this interface.
type BlockchainClient interface {
	// GetLatestBlockNumber returns the latest block/slot number
	GetLatestBlockNumber(ctx context.Context) (uint64, error)

	// GetBlock retrieves a block by its number. The returned ChainBlock is chain-specific.
	GetBlock(ctx context.Context, blockNumber uint64) (ChainBlock, error)

	// GetBlockWithReceipts retrieves a block with all transaction receipts/metadata
	GetBlockWithReceipts(ctx context.Context, blockNumber uint64) (ChainBlock, []ChainReceipt, error)

	// Close closes the client connection
	Close() error

	// ChainName returns the name of the blockchain (e.g., "ethereum", "solana")
	ChainName() string

	// NetworkName returns the network name (e.g., "mainnet", "testnet")
	NetworkName() string

	// GetNetworkID returns the network/chain ID
	GetNetworkID(ctx context.Context) (string, error)
}

// ChainBlock represents a block in a chain-agnostic way.
// Each blockchain implementation provides its own implementation.
type ChainBlock interface {
	// Number returns the block/slot number
	Number() uint64

	// Hash returns the block hash as a hex string
	Hash() string

	// ParentHash returns the parent block hash
	ParentHash() string

	// Timestamp returns the block timestamp (unix seconds)
	Timestamp() uint64

	// TransactionCount returns the number of transactions in the block
	TransactionCount() int

	// Transactions returns all transactions in the block
	Transactions() []ChainTransaction

	// Raw returns the underlying chain-specific block data
	Raw() interface{}
}

// ChainTransaction represents a transaction in a chain-agnostic way.
type ChainTransaction interface {
	// Hash returns the transaction hash/signature
	Hash() string

	// BlockNumber returns the block number this transaction belongs to
	BlockNumber() uint64

	// Index returns the transaction index within the block
	Index() int

	// Raw returns the underlying chain-specific transaction data
	Raw() interface{}
}

// ChainReceipt represents transaction receipt/metadata in a chain-agnostic way.
type ChainReceipt interface {
	// TransactionHash returns the associated transaction hash
	TransactionHash() string

	// Status returns the transaction status (true = success)
	Status() bool

	// Logs returns the logs/events from this transaction
	Logs() []ChainLog

	// Raw returns the underlying chain-specific receipt data
	Raw() interface{}
}

// ChainLog represents a log/event in a chain-agnostic way.
type ChainLog interface {
	// Index returns the log index
	Index() int

	// Address returns the emitting contract/program address
	Address() string

	// Data returns the log data
	Data() string

	// Raw returns the underlying chain-specific log data
	Raw() interface{}
}

// CollectionSet contains all collection names for a specific chain/network combination.
type CollectionSet struct {
	Block       string
	Transaction string
	Log         string
	// Solana-specific collections
	Instruction   string
	AccountUpdate string
	// Ethereum-specific collections
	AccessListEntry string
	// Common
	BatchSignature string
}

// ChainDocumentBuilder transforms chain-specific data into DefraDB documents.
// Each blockchain implementation provides its own document builder.
type ChainDocumentBuilder interface {
	// BuildBlockDocument creates a document map for a block
	BuildBlockDocument(block ChainBlock) (map[string]interface{}, error)

	// BuildTransactionDocument creates a document map for a transaction
	BuildTransactionDocument(tx ChainTransaction, blockDocID string) (map[string]interface{}, error)

	// BuildLogDocument creates a document map for a log
	BuildLogDocument(log ChainLog, blockDocID, txDocID string) (map[string]interface{}, error)

	// BuildReceiptDocuments creates all documents from a receipt (logs, etc.)
	BuildReceiptDocuments(receipt ChainReceipt, blockDocID, txDocID string) ([]map[string]interface{}, error)

	// GetCollectionNames returns the collection names for this chain
	GetCollectionNames() CollectionSet

	// ChainName returns the chain name
	ChainName() string

	// NetworkName returns the network name
	NetworkName() string
}

// ChainConfig holds configuration for a specific blockchain.
type ChainConfig struct {
	// Chain type (ethereum, solana)
	Type ChainType `yaml:"type"`

	// Network (mainnet, testnet, devnet)
	Network NetworkType `yaml:"network"`

	// Enabled indicates if this chain should be indexed
	Enabled bool `yaml:"enabled"`

	// StartHeight is the block/slot number to start indexing from
	StartHeight uint64 `yaml:"start_height"`

	// RPC configuration
	RPCURL   string `yaml:"rpc_url"`
	WSURL    string `yaml:"ws_url"`
	APIKey   string `yaml:"api_key"`

	// Solana-specific configuration
	Commitment string `yaml:"commitment,omitempty"` // processed, confirmed, finalized
}

// GenerateCollectionName generates a collection name following the pattern: Chain__Network__Type
func GenerateCollectionName(chain ChainType, network NetworkType, collectionType string) string {
	chainName := string(chain)
	networkName := string(network)

	// Capitalize first letter
	if len(chainName) > 0 {
		chainName = string(chainName[0]-32) + chainName[1:]
	}
	if len(networkName) > 0 {
		networkName = string(networkName[0]-32) + networkName[1:]
	}

	return chainName + "__" + networkName + "__" + collectionType
}
