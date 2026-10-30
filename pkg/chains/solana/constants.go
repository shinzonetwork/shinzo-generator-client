package solana

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
// byte-identical documents in one slot (identical memo transfers are routine
// on mainnet) collide on one docID and the batch write drops the batch's
// docIDs, leaving the block unsigned. It is a data field, not a link field:
// the LinkStamper resolves _transactionID from the document's own value.
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
// inner instruction shapes of the getBlock response.
const (
	ProgramIDFieldName   = "programId"
	AccountsFieldName    = "accounts"
	DataFieldName        = "data"
	StackHeightFieldName = "stackHeight"
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
