// Package solana provides Solana-specific implementations of the chains interfaces.
package solana

import (
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
)

// Commitment levels for Solana
type Commitment string

const (
	CommitmentProcessed Commitment = "processed"
	CommitmentConfirmed Commitment = "confirmed"
	CommitmentFinalized Commitment = "finalized"
)

// ToRPCCommitment converts to the solana-go rpc.CommitmentType
func (c Commitment) ToRPCCommitment() rpc.CommitmentType {
	switch c {
	case CommitmentProcessed:
		return rpc.CommitmentProcessed
	case CommitmentConfirmed:
		return rpc.CommitmentConfirmed
	case CommitmentFinalized:
		return rpc.CommitmentFinalized
	default:
		return rpc.CommitmentConfirmed
	}
}

// SolanaBlock wraps Solana block data to implement chains.ChainBlock
type SolanaBlock struct {
	slot        uint64
	block       *rpc.GetBlockResult
	blockHeight *uint64
}

// NewSolanaBlock creates a new SolanaBlock wrapper
func NewSolanaBlock(slot uint64, block *rpc.GetBlockResult) *SolanaBlock {
	return &SolanaBlock{
		slot:  slot,
		block: block,
	}
}

// Number implements chains.ChainBlock - returns the slot number
func (b *SolanaBlock) Number() uint64 {
	return b.slot
}

// Hash implements chains.ChainBlock - returns the blockhash
func (b *SolanaBlock) Hash() string {
	if b.block == nil {
		return ""
	}
	return b.block.Blockhash.String()
}

// ParentHash implements chains.ChainBlock - returns the parent blockhash
func (b *SolanaBlock) ParentHash() string {
	if b.block == nil {
		return ""
	}
	return b.block.PreviousBlockhash.String()
}

// Timestamp implements chains.ChainBlock - returns the block time
func (b *SolanaBlock) Timestamp() uint64 {
	if b.block == nil || b.block.BlockTime == nil {
		return 0
	}
	return uint64(*b.block.BlockTime)
}

// TransactionCount implements chains.ChainBlock
func (b *SolanaBlock) TransactionCount() int {
	if b.block == nil || b.block.Transactions == nil {
		return 0
	}
	return len(b.block.Transactions)
}

// Transactions implements chains.ChainBlock
func (b *SolanaBlock) Transactions() []chains.ChainTransaction {
	if b.block == nil || b.block.Transactions == nil {
		return nil
	}
	txs := make([]chains.ChainTransaction, len(b.block.Transactions))
	for i := range b.block.Transactions {
		txs[i] = NewSolanaTransaction(b.slot, i, &b.block.Transactions[i])
	}
	return txs
}

// Raw implements chains.ChainBlock
func (b *SolanaBlock) Raw() interface{} {
	return b.block
}

// GetSlot returns the slot number
func (b *SolanaBlock) GetSlot() uint64 {
	return b.slot
}

// GetBlockHeight returns the block height if available
func (b *SolanaBlock) GetBlockHeight() *uint64 {
	if b.block == nil {
		return nil
	}
	return b.block.BlockHeight
}

// GetParentSlot returns the parent slot
func (b *SolanaBlock) GetParentSlot() uint64 {
	if b.block == nil {
		return 0
	}
	return b.block.ParentSlot
}

// GetInternalBlock returns the internal block data
func (b *SolanaBlock) GetInternalBlock() *rpc.GetBlockResult {
	return b.block
}

// SolanaTransaction wraps a Solana transaction to implement chains.ChainTransaction
type SolanaTransaction struct {
	slot  uint64
	index int
	tx    *rpc.TransactionWithMeta
}

// NewSolanaTransaction creates a new SolanaTransaction wrapper
func NewSolanaTransaction(slot uint64, index int, tx *rpc.TransactionWithMeta) *SolanaTransaction {
	return &SolanaTransaction{
		slot:  slot,
		index: index,
		tx:    tx,
	}
}

// Hash implements chains.ChainTransaction - returns the first signature
func (t *SolanaTransaction) Hash() string {
	if t.tx == nil || t.tx.Transaction == nil {
		return ""
	}

	// Get the transaction and its signatures
	tx, err := t.tx.GetTransaction()
	if err != nil || tx == nil {
		return ""
	}

	if len(tx.Signatures) == 0 {
		return ""
	}
	return tx.Signatures[0].String()
}

// BlockNumber implements chains.ChainTransaction - returns the slot
func (t *SolanaTransaction) BlockNumber() uint64 {
	return t.slot
}

// Index implements chains.ChainTransaction
func (t *SolanaTransaction) Index() int {
	return t.index
}

// Raw implements chains.ChainTransaction
func (t *SolanaTransaction) Raw() interface{} {
	return t.tx
}

// GetInternalTransaction returns the internal transaction data
func (t *SolanaTransaction) GetInternalTransaction() *rpc.TransactionWithMeta {
	return t.tx
}

// GetSignatures returns all signatures
func (t *SolanaTransaction) GetSignatures() []solana.Signature {
	if t.tx == nil || t.tx.Transaction == nil {
		return nil
	}
	tx, err := t.tx.GetTransaction()
	if err != nil || tx == nil {
		return nil
	}
	return tx.Signatures
}

// GetFee returns the transaction fee
func (t *SolanaTransaction) GetFee() uint64 {
	if t.tx == nil || t.tx.Meta == nil {
		return 0
	}
	return t.tx.Meta.Fee
}

// GetError returns the transaction error if any
func (t *SolanaTransaction) GetError() interface{} {
	if t.tx == nil || t.tx.Meta == nil {
		return nil
	}
	return t.tx.Meta.Err
}

// IsSuccessful returns true if the transaction succeeded
func (t *SolanaTransaction) IsSuccessful() bool {
	if t.tx == nil || t.tx.Meta == nil {
		return false
	}
	return t.tx.Meta.Err == nil
}

// GetPreBalances returns the pre-transaction balances
func (t *SolanaTransaction) GetPreBalances() []uint64 {
	if t.tx == nil || t.tx.Meta == nil {
		return nil
	}
	return t.tx.Meta.PreBalances
}

// GetPostBalances returns the post-transaction balances
func (t *SolanaTransaction) GetPostBalances() []uint64 {
	if t.tx == nil || t.tx.Meta == nil {
		return nil
	}
	return t.tx.Meta.PostBalances
}

// GetInstructions returns the transaction instructions
func (t *SolanaTransaction) GetInstructions() []SolanaInstruction {
	if t.tx == nil || t.tx.Transaction == nil {
		return nil
	}

	tx, err := t.tx.GetTransaction()
	if err != nil || tx == nil {
		return nil
	}

	instructions := make([]SolanaInstruction, len(tx.Message.Instructions))
	for i, ix := range tx.Message.Instructions {
		instructions[i] = SolanaInstruction{
			index:       i,
			instruction: ix,
			accounts:    tx.Message.AccountKeys,
		}
	}
	return instructions
}

// SolanaInstruction represents a Solana instruction
type SolanaInstruction struct {
	index       int
	instruction solana.CompiledInstruction
	accounts    []solana.PublicKey
}

// Index returns the instruction index
func (i *SolanaInstruction) Index() int {
	return i.index
}

// ProgramID returns the program ID
func (i *SolanaInstruction) ProgramID() string {
	if int(i.instruction.ProgramIDIndex) >= len(i.accounts) {
		return ""
	}
	return i.accounts[i.instruction.ProgramIDIndex].String()
}

// Accounts returns the account addresses involved
func (i *SolanaInstruction) Accounts() []string {
	accounts := make([]string, len(i.instruction.Accounts))
	for j, idx := range i.instruction.Accounts {
		if int(idx) < len(i.accounts) {
			accounts[j] = i.accounts[idx].String()
		}
	}
	return accounts
}

// Data returns the instruction data as base64
func (i *SolanaInstruction) Data() string {
	return string(i.instruction.Data)
}

// SolanaReceipt wraps transaction metadata to implement chains.ChainReceipt
type SolanaReceipt struct {
	signature string
	meta      *rpc.TransactionMeta
}

// NewSolanaReceipt creates a new SolanaReceipt wrapper
func NewSolanaReceipt(signature string, meta *rpc.TransactionMeta) *SolanaReceipt {
	return &SolanaReceipt{
		signature: signature,
		meta:      meta,
	}
}

// TransactionHash implements chains.ChainReceipt
func (r *SolanaReceipt) TransactionHash() string {
	return r.signature
}

// Status implements chains.ChainReceipt
func (r *SolanaReceipt) Status() bool {
	if r.meta == nil {
		return false
	}
	return r.meta.Err == nil
}

// Logs implements chains.ChainReceipt - returns log messages as ChainLog
func (r *SolanaReceipt) Logs() []chains.ChainLog {
	if r.meta == nil || r.meta.LogMessages == nil {
		return nil
	}
	logs := make([]chains.ChainLog, len(r.meta.LogMessages))
	for i, msg := range r.meta.LogMessages {
		logs[i] = &SolanaLog{
			index:   i,
			message: msg,
		}
	}
	return logs
}

// Raw implements chains.ChainReceipt
func (r *SolanaReceipt) Raw() interface{} {
	return r.meta
}

// GetInternalMeta returns the internal metadata
func (r *SolanaReceipt) GetInternalMeta() *rpc.TransactionMeta {
	return r.meta
}

// SolanaLog wraps a Solana log message to implement chains.ChainLog
type SolanaLog struct {
	index   int
	message string
}

// Index implements chains.ChainLog
func (l *SolanaLog) Index() int {
	return l.index
}

// Address implements chains.ChainLog - Solana logs don't have addresses
func (l *SolanaLog) Address() string {
	return ""
}

// Data implements chains.ChainLog - returns the log message
func (l *SolanaLog) Data() string {
	return l.message
}

// Raw implements chains.ChainLog
func (l *SolanaLog) Raw() interface{} {
	return l.message
}

// GetCollectionNames returns the Solana collection names for a given network
func GetCollectionNames(network chains.NetworkType) chains.CollectionSet {
	return chains.CollectionSet{
		Block:          chains.GenerateCollectionName(chains.ChainTypeSolana, network, "Slot"),
		Transaction:    chains.GenerateCollectionName(chains.ChainTypeSolana, network, "Transaction"),
		Instruction:    chains.GenerateCollectionName(chains.ChainTypeSolana, network, "Instruction"),
		BatchSignature: chains.GenerateCollectionName(chains.ChainTypeSolana, network, "BatchSignature"),
	}
}
