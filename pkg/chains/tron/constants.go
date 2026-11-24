package tron

import "sort"

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

// apiKeyHeader is the default header name for the TronGrid authentication
// key, sent on every request when an API key is configured. HTTP header
// names are matched case-insensitively on the wire, so the canonical MIME
// spelling net/http writes is accepted wherever docs show the upper-case
// form. A configured tron.api_key_type overrides the name.
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

// genesisBlockIDs maps each supported chain.network to block 0's blockID.
// A Tron blockID embeds its block number in the leading 8 hex chars (16
// zeros at genesis), so a genesis blockID is a network's permanent identity:
// two networks cannot share one. The trailing 4 bytes of each entry are the
// documented eth_chainId of that network, so the identity stays
// cross-checkable against published chain registries. Wallet endpoints are
// network-agnostic, so this table is what Connect pins an endpoint to the
// network the config names.
var genesisBlockIDs = map[string]string{ //nolint:gochecknoglobals // fixed network-identity table, same pattern as SupportedChainAdapters
	"Mainnet": "00000000000000001ebf88508a03865c71d452e25f4d51194196a1d22b6653dc",
	"Nile":    "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc",
	"Shasta":  "0000000000000000de1aa88295e1fcf982742f773e0419c5a9c134c994a9059e",
}

// SupportedNetworks returns the chain.network values Connect can pin a
// wallet endpoint to, in stable alphabetical order, so error messages and
// operator docs name the same set the code enforces.
func SupportedNetworks() []string {
	networks := make([]string, 0, len(genesisBlockIDs))
	for network := range genesisBlockIDs {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	return networks
}
