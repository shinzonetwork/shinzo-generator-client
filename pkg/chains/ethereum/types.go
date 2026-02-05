// Package ethereum provides Ethereum-specific implementations of the chains interfaces.
package ethereum

import (
	"fmt"
	"strconv"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

// EthereumBlock wraps the internal Block type to implement chains.ChainBlock
type EthereumBlock struct {
	block    *types.Block
	receipts []*types.TransactionReceipt
}

// NewEthereumBlock creates a new EthereumBlock wrapper
func NewEthereumBlock(block *types.Block) *EthereumBlock {
	return &EthereumBlock{block: block}
}

// NewEthereumBlockWithReceipts creates a new EthereumBlock with receipts
func NewEthereumBlockWithReceipts(block *types.Block, receipts []*types.TransactionReceipt) *EthereumBlock {
	return &EthereumBlock{block: block, receipts: receipts}
}

// Number implements chains.ChainBlock
func (b *EthereumBlock) Number() uint64 {
	if b.block == nil {
		return 0
	}
	num, _ := strconv.ParseUint(b.block.Number, 10, 64)
	return num
}

// Hash implements chains.ChainBlock
func (b *EthereumBlock) Hash() string {
	if b.block == nil {
		return ""
	}
	return b.block.Hash
}

// ParentHash implements chains.ChainBlock
func (b *EthereumBlock) ParentHash() string {
	if b.block == nil {
		return ""
	}
	return b.block.ParentHash
}

// Timestamp implements chains.ChainBlock
func (b *EthereumBlock) Timestamp() uint64 {
	if b.block == nil {
		return 0
	}
	ts, _ := strconv.ParseUint(b.block.Timestamp, 10, 64)
	return ts
}

// TransactionCount implements chains.ChainBlock
func (b *EthereumBlock) TransactionCount() int {
	if b.block == nil {
		return 0
	}
	return len(b.block.Transactions)
}

// Transactions implements chains.ChainBlock
func (b *EthereumBlock) Transactions() []chains.ChainTransaction {
	if b.block == nil {
		return nil
	}
	txs := make([]chains.ChainTransaction, len(b.block.Transactions))
	for i := range b.block.Transactions {
		txs[i] = &EthereumTransaction{tx: &b.block.Transactions[i]}
	}
	return txs
}

// Raw implements chains.ChainBlock
func (b *EthereumBlock) Raw() interface{} {
	return b.block
}

// GetInternalBlock returns the internal block type
func (b *EthereumBlock) GetInternalBlock() *types.Block {
	return b.block
}

// GetReceipts returns the receipts
func (b *EthereumBlock) GetReceipts() []*types.TransactionReceipt {
	return b.receipts
}

// EthereumTransaction wraps the internal Transaction type to implement chains.ChainTransaction
type EthereumTransaction struct {
	tx *types.Transaction
}

// NewEthereumTransaction creates a new EthereumTransaction wrapper
func NewEthereumTransaction(tx *types.Transaction) *EthereumTransaction {
	return &EthereumTransaction{tx: tx}
}

// Hash implements chains.ChainTransaction
func (t *EthereumTransaction) Hash() string {
	if t.tx == nil {
		return ""
	}
	return t.tx.Hash
}

// BlockNumber implements chains.ChainTransaction
func (t *EthereumTransaction) BlockNumber() uint64 {
	if t.tx == nil {
		return 0
	}
	num, _ := strconv.ParseUint(t.tx.BlockNumber, 10, 64)
	return num
}

// Index implements chains.ChainTransaction
func (t *EthereumTransaction) Index() int {
	if t.tx == nil {
		return 0
	}
	return t.tx.TransactionIndex
}

// Raw implements chains.ChainTransaction
func (t *EthereumTransaction) Raw() interface{} {
	return t.tx
}

// GetInternalTransaction returns the internal transaction type
func (t *EthereumTransaction) GetInternalTransaction() *types.Transaction {
	return t.tx
}

// EthereumReceipt wraps the internal TransactionReceipt type to implement chains.ChainReceipt
type EthereumReceipt struct {
	receipt *types.TransactionReceipt
}

// NewEthereumReceipt creates a new EthereumReceipt wrapper
func NewEthereumReceipt(receipt *types.TransactionReceipt) *EthereumReceipt {
	return &EthereumReceipt{receipt: receipt}
}

// TransactionHash implements chains.ChainReceipt
func (r *EthereumReceipt) TransactionHash() string {
	if r.receipt == nil {
		return ""
	}
	return r.receipt.TransactionHash
}

// Status implements chains.ChainReceipt
func (r *EthereumReceipt) Status() bool {
	if r.receipt == nil {
		return false
	}
	return r.receipt.Status == "1"
}

// Logs implements chains.ChainReceipt
func (r *EthereumReceipt) Logs() []chains.ChainLog {
	if r.receipt == nil {
		return nil
	}
	logs := make([]chains.ChainLog, len(r.receipt.Logs))
	for i := range r.receipt.Logs {
		logs[i] = &EthereumLog{log: &r.receipt.Logs[i]}
	}
	return logs
}

// Raw implements chains.ChainReceipt
func (r *EthereumReceipt) Raw() interface{} {
	return r.receipt
}

// GetInternalReceipt returns the internal receipt type
func (r *EthereumReceipt) GetInternalReceipt() *types.TransactionReceipt {
	return r.receipt
}

// EthereumLog wraps the internal Log type to implement chains.ChainLog
type EthereumLog struct {
	log *types.Log
}

// NewEthereumLog creates a new EthereumLog wrapper
func NewEthereumLog(log *types.Log) *EthereumLog {
	return &EthereumLog{log: log}
}

// Index implements chains.ChainLog
func (l *EthereumLog) Index() int {
	if l.log == nil {
		return 0
	}
	return l.log.LogIndex
}

// Address implements chains.ChainLog
func (l *EthereumLog) Address() string {
	if l.log == nil {
		return ""
	}
	return l.log.Address
}

// Data implements chains.ChainLog
func (l *EthereumLog) Data() string {
	if l.log == nil {
		return ""
	}
	return l.log.Data
}

// Raw implements chains.ChainLog
func (l *EthereumLog) Raw() interface{} {
	return l.log
}

// GetInternalLog returns the internal log type
func (l *EthereumLog) GetInternalLog() *types.Log {
	return l.log
}

// Topics returns the log topics
func (l *EthereumLog) Topics() []string {
	if l.log == nil {
		return nil
	}
	return l.log.Topics
}

// BlockNumber returns the block number
func (l *EthereumLog) BlockNumber() string {
	if l.log == nil {
		return ""
	}
	return l.log.BlockNumber
}

// TransactionHash returns the transaction hash
func (l *EthereumLog) TransactionHash() string {
	if l.log == nil {
		return ""
	}
	return l.log.TransactionHash
}

// TransactionIndex returns the transaction index
func (l *EthereumLog) TransactionIndex() int {
	if l.log == nil {
		return 0
	}
	return l.log.TransactionIndex
}

// BlockHash returns the block hash
func (l *EthereumLog) BlockHash() string {
	if l.log == nil {
		return ""
	}
	return l.log.BlockHash
}

// Removed returns if the log was removed
func (l *EthereumLog) Removed() bool {
	if l.log == nil {
		return false
	}
	return l.log.Removed
}

// GetCollectionNames returns the Ethereum collection names for a given network
func GetCollectionNames(network chains.NetworkType) chains.CollectionSet {
	return chains.CollectionSet{
		Block:           chains.GenerateCollectionName(chains.ChainTypeEthereum, network, "Block"),
		Transaction:     chains.GenerateCollectionName(chains.ChainTypeEthereum, network, "Transaction"),
		Log:             chains.GenerateCollectionName(chains.ChainTypeEthereum, network, "Log"),
		AccessListEntry: chains.GenerateCollectionName(chains.ChainTypeEthereum, network, "AccessListEntry"),
		BatchSignature:  chains.GenerateCollectionName(chains.ChainTypeEthereum, network, "BatchSignature"),
	}
}

// FormatBlockNumber formats a block number for display
func FormatBlockNumber(blockNum uint64) string {
	return fmt.Sprintf("%d", blockNum)
}
