package solana

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
)

// GenerateSchema generates the GraphQL schema for Solana collections
func GenerateSchema(network chains.NetworkType) string {
	collections := GetCollectionNames(network)

	return fmt.Sprintf(`
type %s {
	slot: Int @index(unique: true)
	blockhash: String @index
	parentSlot: Int
	blockTime: Int
	blockHeight: Int
	previousBlockhash: String
	transactionCount: Int
	transactions: [%s] @relation(name: "slot_transactions")
}

type %s {
	signature: String @index(unique: true)
	slot: Int @index
	transactionIndex: Int
	fee: Int
	err: String
	preBalances: [Int]
	postBalances: [Int]
	logMessages: [String]
	successful: Boolean
	slot_ref: %s @primary @relation(name: "slot_transactions")
	instructions: [%s] @relation(name: "transaction_instructions")
}

type %s {
	instructionIndex: Int
	slot: Int @index
	transactionSignature: String @index
	programId: String @index
	accounts: [String]
	data: String
	stackHeight: Int
	transaction: %s @primary @relation(name: "transaction_instructions")
}

type %s {
	slot: Int @index(unique: true)
	blockhash: String
	merkleRoot: String
	cidCount: Int
	signatureType: String
	signatureIdentity: String
	signatureValue: String
	createdAt: String
}
`,
		collections.Block,       // Slot
		collections.Transaction, // Slot -> transactions relation
		collections.Transaction, // Transaction type
		collections.Block,       // Transaction -> slot relation
		collections.Instruction, // Transaction -> instructions relation
		collections.Instruction, // Instruction type
		collections.Transaction, // Instruction -> transaction relation
		collections.BatchSignature,
	)
}

// GetSchemaCollectionNames returns the collection names used in the schema
func GetSchemaCollectionNames(network chains.NetworkType) []string {
	collections := GetCollectionNames(network)
	return []string{
		collections.Block,
		collections.Transaction,
		collections.Instruction,
		collections.BatchSignature,
	}
}
