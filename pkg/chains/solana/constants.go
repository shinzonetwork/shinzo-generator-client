package solana

import "sort"

// Field names used to build and stamp Solana documents.
//
// These constants live in pkg/chains/solana, not pkg/constants, because
// they are Solana-specific: non-test code outside this package must not
// reference them. The block document's number/hash fields, the shared
// blockNumber/blockHash payload fields, and every signature-document field
// name are the generator-host contract and live in pkg/constants. The
// boundary is enforced by the solana-boundary depguard rule in
// .golangci.yml.
//
// The names are carried through from the Solana JSON-RPC object shapes,
// except where the generator-host contract renames them (the block doc's
// number/hash/parentHash in place of slot/blockhash/parentBlockhash).

// TransactionSignatureFieldName is the document join field carrying the
// parent transaction's signature on instruction and token-balance-change
// documents. DefraDB docIDs are content hashes, so without it two
// byte-identical documents in different transactions of one slot (identical
// memo transfers are routine on mainnet) collide on one docID and the batch
// write drops the batch's docIDs, leaving the block unsigned. It is
// necessary but not sufficient for instruction uniqueness: byte-identical
// instructions inside ONE transaction also collide, which the
// instructionIndex/innerIndex ordinals separate. It is a data field, not a
// link field: the LinkStamper resolves _transactionID from the document's
// own value.
const TransactionSignatureFieldName = "transactionSignature"

// Block document field names, carried through from the Solana getBlock
// response (blockTime is Unix seconds, the counts are the meta payload and
// reward list lengths).
const (
	ParentHashFieldName       = "parentHash"
	BlockTimeFieldName        = "blockTime"
	TransactionCountFieldName = "transactionCount"
	RewardCountFieldName      = "rewardCount"
)

// Transaction document field names, carried through from the transaction +
// meta shapes of the getBlock response.
const (
	SignatureFieldName            = "signature"
	VersionFieldName              = "version"
	ErrFieldName                  = "err"
	FeeFieldName                  = "fee"
	ComputeUnitsConsumedFieldName = "computeUnitsConsumed"
	LogMessagesFieldName          = "logMessages"
	PreBalancesFieldName          = "preBalances"
	PostBalancesFieldName         = "postBalances"
	RecentBlockhashFieldName      = "recentBlockhash"
	AccountKeysFieldName          = "accountKeys"
	LoadedAddressesFieldName      = "loadedAddresses"
)

// Instruction document field names, carried through from the compiled and
// inner instruction shapes of the getBlock response. The ordinals are
// positional, not wire fields: the converter derives them from loop
// positions and the inner instruction group's index. stackHeight is set
// only on inner documents (key presence is the stamper's inner/outer
// discriminator); instructionIndex is set on both kinds, innerIndex only
// on inner documents.
const (
	ProgramIDFieldName        = "programId"
	AccountsFieldName         = "accounts"
	DataFieldName             = "data"
	StackHeightFieldName      = "stackHeight"
	InstructionIndexFieldName = "instructionIndex"
	InnerIndexFieldName       = "innerIndex"
)

// TokenBalanceChange document field names, carried through from the
// preTokenBalances/postTokenBalances diff. Amounts are strings because token
// amounts can exceed int64 for zero-decimal large-supply mints.
const (
	MintFieldName         = "mint"
	OwnerFieldName        = "owner"
	TokenAccountFieldName = "tokenAccount"
	PreAmountFieldName    = "preAmount"
	PostAmountFieldName   = "postAmount"
)

// Reward document field names, carried through from the getBlock rewards
// list. Lamports is signed: rewards are credits, rent and fee are debits.
const (
	PubkeyFieldName      = "pubkey"
	LamportsFieldName    = "lamports"
	PostBalanceFieldName = "postBalance"
	RewardTypeFieldName  = "rewardType"
	CommissionFieldName  = "commission"
)

// defaultNetwork is the network fallback when a hand-built config leaves
// chain.network unset. Shared by the converter's collection-prefix fallback
// and the fetcher's genesis-check identity, matching the Tron adapter's
// default.
const defaultNetwork = "Mainnet"

// genesisHashes maps each supported chain.network to the cluster's genesis
// hash. A genesis hash is a network's permanent identity: it is fixed at
// cluster creation and survives devnet/testnet ledger resets, so two
// networks cannot share one. JSON-RPC endpoints are network-agnostic, so
// this table is what Connect pins an endpoint to the network the config
// names, the same role the Tron adapter's genesis blockID table plays and
// the EVM adapter's chain-id check serves. The values are the documented
// cluster hashes from solana-labs/solana sdk/src/genesis_config.rs.
var genesisHashes = map[string]string{ //nolint:gochecknoglobals // fixed network-identity table, same pattern as SupportedChainAdapters
	"Mainnet": "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d",
	"Devnet":  "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
	"Testnet": "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY",
}

// SupportedNetworks returns the sorted keys of the genesis-hash table so
// error messages name the enforced set.
func SupportedNetworks() []string {
	networks := make([]string, 0, len(genesisHashes))
	for network := range genesisHashes {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	return networks
}
