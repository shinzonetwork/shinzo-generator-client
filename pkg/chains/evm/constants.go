package evm

// Field names used to build and stamp EVM documents.
//
// These constants live in pkg/chains/evm — not pkg/constants — because they
// are EVM-specific: non-test code outside this package must not reference
// them. The block document's own number/hash field names are the
// generator-host contract and live in pkg/constants. The boundary is
// enforced by the evm-boundary depguard rule in .golangci.yml.
//
// The names are carried through from the Ethereum JSON-RPC object shapes.

// TransactionHashFieldName is the document join field that later write
// stages query to link and purge documents across collections.
const TransactionHashFieldName = "transactionHash"

// Payload field names, carried through from the Ethereum JSON-RPC shapes.
const (
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
