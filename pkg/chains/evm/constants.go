package evm

// Ethereum JSON-RPC field names used to build and stamp EVM documents.
//
// These constants live in pkg/chains/evm — not pkg/constants — because they
// are EVM-specific: non-test code outside this package must not reference
// them. The boundary is enforced by
// pkg/constants/constants_boundary_test.go.

// NumberFieldValue is the string value "number" assigned to a field.
const NumberFieldValue = "number"

// HashKeyValue is the string value "hash" assigned to a key field.
const HashKeyValue = "hash"

// BlockNumberKeyValue is the string value "blockNumber" assigned to a key field.
const BlockNumberKeyValue = "blockNumber"

// BlockHashKeyValue is the string value "blockHash" assigned to a key field.
const BlockHashKeyValue = "blockHash"

// AddressKeyValue is the string value "address" assigned to a key field.
const AddressKeyValue = "address"

// TransactionHashKeyValue is the string value "transactionHash" assigned to a key field.
const TransactionHashKeyValue = "transactionHash"

// TimestampKeyValue is the string value "timestamp" assigned to a key field.
const TimestampKeyValue = "timestamp"

// ParentHashKeyValue is the string value "parentHash" assigned to a key field.
const ParentHashKeyValue = "parentHash"

// DifficultyKeyValue is the string value "difficulty" assigned to a key field.
const DifficultyKeyValue = "difficulty"

// GasUsedKeyValue is the string value "gasUsed" assigned to a key field.
const GasUsedKeyValue = "gasUsed"

// GasLimitKeyValue is the string value "gasLimit" assigned to a key field.
const GasLimitKeyValue = "gasLimit"

// NonceKeyValue is the string value "nonce" assigned to a key field.
const NonceKeyValue = "nonce"

// MinerKeyValue is the string value "miner" assigned to a key field.
const MinerKeyValue = "miner"

// StateRootKeyValue is the string value "stateRoot" assigned to a key field.
const StateRootKeyValue = "stateRoot"

// Sha3UnclesKeyValue is the string value "sha3Uncles" assigned to a key field.
const Sha3UnclesKeyValue = "sha3Uncles"

// TransactionsRootKeyValue is the string value "transactionsRoot" assigned to a key field.
const TransactionsRootKeyValue = "transactionsRoot"

// ReceiptsRootKeyValue is the string value "receiptsRoot" assigned to a key field.
const ReceiptsRootKeyValue = "receiptsRoot"

// LogsBloomKeyValue is the string value "logsBloom" assigned to a key field.
const LogsBloomKeyValue = "logsBloom"

// ExtraDataKeyValue is the string value "extraData" assigned to a key field.
const ExtraDataKeyValue = "extraData"

// MixHashKeyValue is the string value "mixHash" assigned to a key field.
const MixHashKeyValue = "mixHash"

// TransactionIndexKeyValue is the string value "transactionIndex" assigned to a key field.
const TransactionIndexKeyValue = "transactionIndex"

// TypeKeyValue is the string value "type" assigned to a key field.
const TypeKeyValue = "type"

// CumulativeGasUsedKeyValue is the string value "cumulativeGasUsed" assigned to a key field.
const CumulativeGasUsedKeyValue = "cumulativeGasUsed"

// EffectiveGasPriceKeyValue is the string value "effectiveGasPrice" assigned to a key field.
const EffectiveGasPriceKeyValue = "effectiveGasPrice"

// StatusKeyValue is the string value "status" assigned to a key field.
const StatusKeyValue = "status"
