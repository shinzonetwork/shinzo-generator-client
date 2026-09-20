package solana

// AdapterName is the name under which this package registers its chain
// factories with the chains registry.
const AdapterName = "solana"

// embeddedSDLPrefix is the literal collection prefix baked into the embedded
// .graphql files under collections/. CollectionSDL swaps it with the
// configured prefix at load time, mirroring the shared loader's embeddedPrefix
// behaviour for the EVM SDL. It coincides with DefaultCollectionPrefix.
const embeddedSDLPrefix = "Solana__Mainnet"

// Field names used to build and stamp Solana documents.
//
// These constants live in pkg/chains/solana — not pkg/constants — because
// they are Solana-specific: non-test code outside this package must not
// reference them. The boundary is enforced by the chain-boundary depguard
// rule in .golangci.yml.
//
// Unlike the EVM adapter, every Solana collection (block, transaction,
// instruction, token balance change, reward) carries its height in the same
// field name: "slot". Only the signature documents use the shared
// pkg/constants.BlockNumberFieldName, and those are written by the generic
// BlockHandler.

// Block document field names.
const (
	SlotFieldName             = "slot"
	BlockhashFieldName        = "blockhash"
	ParentSlotFieldName       = "parentSlot"
	BlockHeightFieldName      = "blockHeight"
	BlockTimeFieldName        = "blockTime"
	TransactionCountFieldName = "transactionCount"
	RewardCountFieldName      = "rewardCount"
)

// Transaction document field names.
const (
	SignatureFieldName               = "signature"
	TransactionIndexFieldName        = "transactionIndex"
	VersionFieldName                 = "version"
	FailedFieldName                  = "failed"
	ErrFieldName                     = "err"
	FeeFieldName                     = "fee"
	ComputeUnitsConsumedFieldName    = "computeUnitsConsumed"
	LogMessagesFieldName             = "logMessages"
	PreBalancesFieldName             = "preBalances"
	PostBalancesFieldName            = "postBalances"
	RecentBlockhashFieldName         = "recentBlockhash"
	AccountKeysFieldName             = "accountKeys"
	LoadedAddressesWritableFieldName = "loadedAddressesWritable"
	LoadedAddressesReadonlyFieldName = "loadedAddressesReadonly"
)

// Instruction document field names.
const (
	ProgramIDFieldName        = "programId"
	AccountsFieldName         = "accounts"
	DataFieldName             = "data"
	InstructionIndexFieldName = "instructionIndex"
	InnerIndexFieldName       = "innerIndex"
	StackHeightFieldName      = "stackHeight"
	// TransactionSignatureFieldName carries the parent transaction's
	// signature on instruction and token-balance-change documents. DefraDB
	// docIDs are content hashes, so without it two byte-identical documents
	// in one slot (identical memo transfers are routine on mainnet) collide
	// on one docID and the batch's docIDs are dropped, leaving the block
	// unsigned. It is a data field, not a link field: the LinkStamper still
	// resolves _transactionID from the converter's parallel ref arrays.
	TransactionSignatureFieldName = "transactionSignature"
)

// TokenBalanceChange document field names.
const (
	MintFieldName         = "mint"
	OwnerFieldName        = "owner"
	TokenAccountFieldName = "tokenAccount"
	PreAmountFieldName    = "preAmount"
	PostAmountFieldName   = "postAmount"
)

// Reward document field names.
const (
	PubkeyFieldName      = "pubkey"
	LamportsFieldName    = "lamports"
	PostBalanceFieldName = "postBalance"
	RewardTypeFieldName  = "rewardType"
	CommissionFieldName  = "commission"
)
