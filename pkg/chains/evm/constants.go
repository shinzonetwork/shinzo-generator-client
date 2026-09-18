package evm

// Field names used to build and stamp EVM documents.
//
// These constants live in pkg/chains/evm — not pkg/constants — because they
// are EVM-specific: non-test code outside this package must not reference
// them. The boundary is enforced by the evm-boundary depguard rule in
// .golangci.yml.
//
// *KeyName constants name the EVM-only fields used to join and look up documents
// across collections; the blockNumber and blockHash join keys
// are shared with the signature-document contract and therefore live in
// pkg/constants.
//
// *FieldName constants name the remaining payload fields
// carried through from the Ethereum JSON-RPC object shapes.

// Document join keys: the fields later write stages query to link and
// purge documents across collections.
const (
	HashKeyName            = "hash"
	TransactionHashKeyName = "transactionHash"
)

// Payload field names, carried through from the Ethereum JSON-RPC shapes.
const (
	NumberFieldName            = "number"
	AddressFieldName           = "address"
	TimestampFieldName         = "timestamp"
	ParentHashFieldName        = "parentHash"
	DifficultyFieldName        = "difficulty"
	GasUsedFieldName           = "gasUsed"
	GasLimitFieldName          = "gasLimit"
	NonceFieldName             = "nonce"
	MinerFieldName             = "miner"
	StateRootFieldName         = "stateRoot"
	Sha3UnclesFieldName        = "sha3Uncles"
	TransactionsRootFieldName  = "transactionsRoot"
	ReceiptsRootFieldName      = "receiptsRoot"
	LogsBloomFieldName         = "logsBloom"
	ExtraDataFieldName         = "extraData"
	MixHashFieldName           = "mixHash"
	TransactionIndexFieldName  = "transactionIndex"
	TypeFieldName              = "type"
	CumulativeGasUsedFieldName = "cumulativeGasUsed"
	EffectiveGasPriceFieldName = "effectiveGasPrice"
	StatusFieldName            = "status"
)
