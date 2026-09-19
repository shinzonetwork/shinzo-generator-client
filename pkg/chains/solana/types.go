package solana

// Local JSON-shaped types mirroring the Solana JSON-RPC getBlock response.
// They are the boundary contract of the adapter: solana-go SDK types are
// converted into these at the RPC layer (solana_client.go) and never escape
// this package, the same role pkg/chains/evm/types.go plays for geth types.
// Keys, hashes, and instruction data are stored as base58 strings already
// resolved from the compiled-instruction index form, so the converter works
// with document-ready values.
//
// Nullable RPC fields (blockTime, blockHeight, computeUnitsConsumed,
// commission) are pointers; absent optional collections (logMessages,
// innerInstructions, token balances) are nil slices, never invented.

// Block is a Solana block as returned by a full-fidelity getBlock call. Slot
// doubles as the fetch height across the adapter; not every slot produces a
// block (a leader may skip it), which the fetcher surfaces as
// chains.ErrHeightSkipped.
type Block struct {
	Slot              uint64        `json:"slot"`
	Blockhash         string        `json:"blockhash"`
	PreviousBlockhash string        `json:"previousBlockhash"`
	ParentSlot        uint64        `json:"parentSlot"`
	BlockHeight       *uint64       `json:"blockHeight,omitempty"`
	BlockTime         *int64        `json:"blockTime,omitempty"`
	Transactions      []Transaction `json:"transactions"`
	Rewards           []Reward      `json:"rewards"`
}

// Transaction is a Solana transaction with its execution metadata. Signature
// is the primary key (the transaction's first signature). Failed transactions
// (meta.err != null) are represented with Failed=true and the raw JSON error
// object encoded in Err.
type Transaction struct {
	Signature               string                  `json:"signature"`
	Slot                    uint64                  `json:"slot"`
	TransactionIndex        int                     `json:"transactionIndex"`
	Version                 string                  `json:"version"` // "legacy", "0", or "1"
	Failed                  bool                    `json:"failed"`
	Err                     string                  `json:"err,omitempty"` // raw JSON error object, empty on success
	Fee                     uint64                  `json:"fee"`
	ComputeUnitsConsumed    *uint64                 `json:"computeUnitsConsumed,omitempty"`
	LogMessages             []string                `json:"logMessages"`
	PreBalances             []uint64                `json:"preBalances"`
	PostBalances            []uint64                `json:"postBalances"`
	RecentBlockhash         string                  `json:"recentBlockhash"`
	AccountKeys             []string                `json:"accountKeys"`             // static keys from the message
	LoadedAddressesWritable []string                `json:"loadedAddressesWritable"` // ALT-loaded write keys
	LoadedAddressesReadonly []string                `json:"loadedAddressesReadonly"` // ALT-loaded read keys
	Instructions            []Instruction           `json:"instructions"`            // outer (compiled) instructions
	InnerInstructions       []InnerInstructionGroup `json:"innerInstructions,omitempty"`
	PreTokenBalances        []TokenBalance          `json:"preTokenBalances,omitempty"`
	PostTokenBalances       []TokenBalance          `json:"postTokenBalances,omitempty"`
}

// Instruction is one compiled (outer) or inner (CPI) instruction. Program and
// accounts are resolved to base58 pubkeys. Data is kept raw (base58); decoding
// is deliberately deferred to a later phase. StackHeight is nil for outer
// instructions and populated for inner ones.
type Instruction struct {
	ProgramID        string   `json:"programId"`
	Accounts         []string `json:"accounts"`
	Data             string   `json:"data"` // raw, base58-encoded
	InstructionIndex int      `json:"instructionIndex"`
	InnerIndex       int      `json:"innerIndex"`
	StackHeight      *uint16  `json:"stackHeight,omitempty"`
}

// InnerInstructionGroup associates the CPI instructions of one outer
// instruction (identified by Index, the outer instruction's position in the
// transaction) so cross-document links can be resolved during conversion.
type InnerInstructionGroup struct {
	Index        uint16        `json:"index"`
	Instructions []Instruction `json:"instructions"`
}

// TokenBalance is one pre/postTokenBalances entry. Amount is a string because
// raw token amounts can exceed int64 for zero-decimal large-supply mints; the
// pre/post diff into a TokenBalanceChange is the converter's job.
type TokenBalance struct {
	AccountIndex uint16 `json:"accountIndex"`
	Mint         string `json:"mint"`
	Owner        string `json:"owner,omitempty"`
	ProgramID    string `json:"programId,omitempty"`
	Amount       string `json:"amount"`
	Decimals     uint8  `json:"decimals"`
}

// Reward is one per-block reward entry. Lamports is signed: staking/voting
// rewards are credits, fee/rent can be debits. Commission applies only to
// voting/staking rewards, hence the pointer.
type Reward struct {
	Pubkey      string `json:"pubkey"`
	Lamports    int64  `json:"lamports"`
	PostBalance uint64 `json:"postBalance"`
	RewardType  string `json:"rewardType,omitempty"`
	Commission  *uint8 `json:"commission,omitempty"`
}
