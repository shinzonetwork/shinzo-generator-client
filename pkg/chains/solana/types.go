package solana

import "github.com/shinzonetwork/shinzo-generator-client/pkg/chains"

// Local JSON-shaped types mirroring the Solana JSON-RPC getBlock response.
// They are the boundary contract of the adapter: solana-go SDK types are
// converted into these at the RPC layer (solana_client.go) and never escape
// this package, the same role pkg/chains/evm/types.go plays for geth types.
// Keys, hashes, and instruction data are stored as base58 strings already
// resolved from the compiled-instruction index form, so the converter works
// with document-ready values.
//
// Nullable RPC fields (blockTime, computeUnitsConsumed, commission) are
// pointers; absent optional collections (logMessages, innerInstructions,
// token balances) are nil slices, never invented. The document-facing
// renames (number/hash/parentHash in place of slot/blockhash/
// previousBlockhash) and the int64 widening of unsigned numerics happen in
// the converter's document maps, not here: these types keep the wire shapes.

// Block is a Solana block as returned by a full-fidelity getBlock call. Slot
// doubles as the fetch height across the adapter; not every slot produces a
// block (a leader may skip it), which the fetcher surfaces as
// ErrHeightSkipped.
type Block struct {
	Slot              uint64        `json:"slot"`
	Blockhash         string        `json:"blockhash"`
	PreviousBlockhash string        `json:"previousBlockhash"`
	BlockTime         *int64        `json:"blockTime,omitempty"`
	Transactions      []Transaction `json:"transactions"`
	Rewards           []Reward      `json:"rewards"`
}

// Transaction is a Solana transaction with its execution metadata. Signature
// is the primary key (the transaction's first signature). Failed
// transactions (meta.err != null) carry the raw JSON error object in Err;
// a successful transaction leaves Err empty, so its key presence
// distinguishes failure without a separate failed field.
type Transaction struct {
	Signature            string   `json:"signature"`
	Version              string   `json:"version"` // "legacy", "0", "1", ...
	Err                  string   `json:"err,omitempty"`
	Fee                  uint64   `json:"fee"`
	ComputeUnitsConsumed *uint64  `json:"computeUnitsConsumed,omitempty"`
	LogMessages          []string `json:"logMessages"`
	PreBalances          []uint64 `json:"preBalances"`
	PostBalances         []uint64 `json:"postBalances"`
	RecentBlockhash      string   `json:"recentBlockhash"`
	AccountKeys          []string `json:"accountKeys"` // static keys from the message
	// LoadedAddresses holds the ALT-loaded writable keys followed by the
	// readonly keys, so one collapsed field covers both lists.
	LoadedAddresses   []string                `json:"loadedAddresses"`
	Instructions      []Instruction           `json:"instructions"` // outer (compiled) instructions
	InnerInstructions []InnerInstructionGroup `json:"innerInstructions,omitempty"`
	PreTokenBalances  []TokenBalance          `json:"preTokenBalances,omitempty"`
	PostTokenBalances []TokenBalance          `json:"postTokenBalances,omitempty"`
}

// Instruction is one compiled (outer) or inner (CPI) instruction. Program and
// accounts are resolved to base58 pubkeys. Data is kept raw (base58); decoding
// is deliberately deferred to a later phase. StackHeight is nil for outer
// instructions and populated for inner ones.
type Instruction struct {
	ProgramID   string   `json:"programId"`
	Accounts    []string `json:"accounts"`
	Data        string   `json:"data"` // raw, base58-encoded
	StackHeight *uint16  `json:"stackHeight,omitempty"`
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

// ErrHeightSkipped aliases chains.ErrHeightSkipped, the chain-agnostic
// sentinel the generic processor classifies (a fetch height the chain will
// never fill). The adapter keeps the local name so its docs and tests read
// in adapter vocabulary; errors.Is treats both names as the same sentinel
// because they are the same error value. EVM never returns the sentinel, so
// EVM behavior is unchanged.
var ErrHeightSkipped = chains.ErrHeightSkipped
