package tron

// DefaultCollectionPrefix is the default collection prefix for backward compatibility.
const DefaultCollectionPrefix = "Tron__Mainnet"

// Wallet API endpoint paths. Requests are plain HTTP POST JSON commands;
// /walletsolidity paths read the SolidityNode view, whose latest block is
// the solidified tip and the reorg walk-back floor.
const (
	endpointGetBlockByNum       = "/wallet/getblockbynum"
	endpointGetBlockByID        = "/wallet/getblockbyid"
	endpointGetNowBlock         = "/wallet/getnowblock"
	endpointGetTxInfoByBlockNum = "/wallet/gettransactioninfobyblocknum"
	endpointSolidityGetNowBlock = "/walletsolidity/getnowblock"
)

// Metric method names for the shared rpc.errors counter, one per endpoint.
const (
	methodGetBlockByNum       = "wallet_getblockbynum"
	methodGetBlockByID        = "wallet_getblockbyid"
	methodGetNowBlock         = "wallet_getnowblock"
	methodGetTxInfoByBlockNum = "wallet_gettransactioninfobyblocknum"
	methodSolidityGetNowBlock = "walletsolidity_getnowblock"
)

// apiKeyHeader is the fixed TronGrid authentication header sent on every
// request when an API key is configured. HTTP header names are matched
// case-insensitively on the wire, so the canonical MIME spelling net/http
// writes is accepted wherever docs show the upper-case form.
const apiKeyHeader = "Tron-Pro-Api-Key" //nolint:gosec // public HTTP header name, not a credential

// httpSuccessMax is the greatest 2xx status the wallet client treats as a
// success response; anything above carries a transport failure.
const httpSuccessMax = 299

// TransactionHashFieldName is the document join field that later write stages
// stamp and query to link documents across collections.
const TransactionHashFieldName = "transactionHash"

// Document field names for the Tron block, transaction, log, and internal
// transfer schemas. They are the doc shape vocabulary, not the wire shape;
// doc_builders.go maps wallet API spellings (owner_address, txID, ...) onto
// them. The generator-host contract names (number, hash, blockNumber,
// blockHash, signature fields) come from pkg/constants.
const (
	AddressFieldName          = "address"
	AmountFieldName           = "amount"
	AssetNameFieldName        = "assetName"
	DataFieldName             = "data"
	EnergyUsageFieldName      = "energyUsage"
	FeeFieldName              = "fee"
	FromFieldName             = "from"
	LogIndexFieldName         = "logIndex"
	ParentHashFieldName       = "parentHash"
	RawDataFieldName          = "rawData"
	RejectedFieldName         = "rejected"
	RetFieldName              = "ret"
	TimestampFieldName        = "timestamp"
	ToFieldName               = "to"
	TopicsFieldName           = "topics"
	TransactionCountFieldName = "transactionCount"
	TransactionIndexFieldName = "transactionIndex"
	TransferIndexFieldName    = "transferIndex"
	TxTrieRootFieldName       = "txTrieRoot"
	TypeFieldName             = "type"
	VersionFieldName          = "version"
	WitnessAddressFieldName   = "witnessAddress"
	WitnessSignatureFieldName = "witnessSignature"
)

// Collection-name fallbacks, mirrored from the EVM converter, applied when
// config leaves chain.name or chain.network empty.
const (
	defaultChainName = "Tron"
	defaultNetwork   = "Mainnet"
)
