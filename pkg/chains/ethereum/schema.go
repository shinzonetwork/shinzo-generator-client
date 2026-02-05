package ethereum

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
)

// GenerateSchema generates the GraphQL schema for Ethereum collections
func GenerateSchema(network chains.NetworkType) string {
	collections := GetCollectionNames(network)

	return fmt.Sprintf(`
type %s {
	hash: String @index(unique: true)
	number: Int @index
	timestamp: String
	parentHash: String
	difficulty: String
	totalDifficulty: String
	gasUsed: String
	gasLimit: String
	baseFeePerGas: String
	nonce: String
	miner: String
	size: String
	stateRoot: String
	sha3Uncles: String
	transactionsRoot: String
	receiptsRoot: String
	logsBloom: String
	extraData: String
	mixHash: String
	uncles: [String]
	transactions: [%s] @relation(name: "block_transactions")
	logs: [%s] @relation(name: "block_logs")
}

type %s {
	hash: String @index(unique: true)
	blockNumber: Int @index
	blockHash: String @index
	transactionIndex: Int
	from: String
	to: String
	value: String
	gas: String
	gasPrice: String
	maxFeePerGas: String
	maxPriorityFeePerGas: String
	input: String
	nonce: String
	type: String
	chainId: String
	v: String
	r: String
	s: String
	cumulativeGasUsed: String
	effectiveGasPrice: String
	status: Boolean
	block: %s @primary @relation(name: "block_transactions")
	logs: [%s] @relation(name: "transaction_logs")
	accessList: [%s] @relation(name: "transaction_accessList")
}

type %s {
	address: String
	topics: [String]
	data: String
	blockNumber: Int @index
	transactionHash: String
	transactionIndex: Int
	blockHash: String
	logIndex: Int
	removed: String
	transaction: %s @primary @relation(name: "transaction_logs")
	block: %s @primary @relation(name: "block_logs")
}

type %s {
	address: String
	blockNumber: Int
	storageKeys: [String]
	transaction: %s @primary @relation(name: "transaction_accessList")
}

type %s {
	blockNumber: Int @index(unique: true)
	blockHash: String
	merkleRoot: String
	cidCount: Int
	signatureType: String
	signatureIdentity: String
	signatureValue: String
	createdAt: String
}
`,
		collections.Block,
		collections.Transaction,
		collections.Log,
		collections.Transaction,
		collections.Block,
		collections.Log,
		collections.AccessListEntry,
		collections.Log,
		collections.Transaction,
		collections.Block,
		collections.AccessListEntry,
		collections.Transaction,
		collections.BatchSignature,
	)
}

// GetSchemaCollectionNames returns the collection names used in the schema
func GetSchemaCollectionNames(network chains.NetworkType) []string {
	collections := GetCollectionNames(network)
	return []string{
		collections.Block,
		collections.Transaction,
		collections.Log,
		collections.AccessListEntry,
		collections.BatchSignature,
	}
}
