package ethereum

import (
	"fmt"
	"strconv"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
)

// DocumentBuilder implements chains.ChainDocumentBuilder for Ethereum
type DocumentBuilder struct {
	network     chains.NetworkType
	collections chains.CollectionSet
}

// NewDocumentBuilder creates a new Ethereum document builder
func NewDocumentBuilder(network chains.NetworkType) *DocumentBuilder {
	return &DocumentBuilder{
		network:     network,
		collections: GetCollectionNames(network),
	}
}

// BuildBlockDocument implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) BuildBlockDocument(block chains.ChainBlock) (map[string]interface{}, error) {
	ethBlock, ok := block.(*EthereumBlock)
	if !ok {
		return nil, fmt.Errorf("expected *EthereumBlock, got %T", block)
	}

	internalBlock := ethBlock.GetInternalBlock()
	if internalBlock == nil {
		return nil, fmt.Errorf("internal block is nil")
	}

	blockInt, err := utils.HexToInt(internalBlock.Number)
	if err != nil {
		// Try parsing as decimal
		blockInt, err = strconv.ParseInt(internalBlock.Number, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse block number: %w", err)
		}
	}

	return map[string]interface{}{
		"hash":             internalBlock.Hash,
		"number":           blockInt,
		"timestamp":        internalBlock.Timestamp,
		"parentHash":       internalBlock.ParentHash,
		"difficulty":       internalBlock.Difficulty,
		"totalDifficulty":  internalBlock.TotalDifficulty,
		"gasUsed":          internalBlock.GasUsed,
		"gasLimit":         internalBlock.GasLimit,
		"baseFeePerGas":    internalBlock.BaseFeePerGas,
		"nonce":            internalBlock.Nonce,
		"miner":            internalBlock.Miner,
		"size":             internalBlock.Size,
		"stateRoot":        internalBlock.StateRoot,
		"sha3Uncles":       internalBlock.Sha3Uncles,
		"transactionsRoot": internalBlock.TransactionsRoot,
		"receiptsRoot":     internalBlock.ReceiptsRoot,
		"logsBloom":        internalBlock.LogsBloom,
		"extraData":        internalBlock.ExtraData,
		"mixHash":          internalBlock.MixHash,
		"uncles":           internalBlock.Uncles,
	}, nil
}

// BuildTransactionDocument implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) BuildTransactionDocument(tx chains.ChainTransaction, blockDocID string) (map[string]interface{}, error) {
	ethTx, ok := tx.(*EthereumTransaction)
	if !ok {
		return nil, fmt.Errorf("expected *EthereumTransaction, got %T", tx)
	}

	internalTx := ethTx.GetInternalTransaction()
	if internalTx == nil {
		return nil, fmt.Errorf("internal transaction is nil")
	}

	blockInt, _ := strconv.ParseInt(internalTx.BlockNumber, 10, 64)

	return map[string]interface{}{
		"hash":                 internalTx.Hash,
		"blockNumber":          blockInt,
		"blockHash":            internalTx.BlockHash,
		"transactionIndex":     internalTx.TransactionIndex,
		"from":                 internalTx.From,
		"to":                   internalTx.To,
		"value":                internalTx.Value,
		"gas":                  internalTx.Gas,
		"gasPrice":             internalTx.GasPrice,
		"maxFeePerGas":         internalTx.MaxFeePerGas,
		"maxPriorityFeePerGas": internalTx.MaxPriorityFeePerGas,
		"input":                string(internalTx.Input),
		"nonce":                internalTx.Nonce,
		"type":                 internalTx.Type,
		"chainId":              internalTx.ChainId,
		"v":                    internalTx.V,
		"r":                    internalTx.R,
		"s":                    internalTx.S,
		"cumulativeGasUsed":    internalTx.CumulativeGasUsed,
		"effectiveGasPrice":    internalTx.EffectiveGasPrice,
		"status":               internalTx.Status,
		"_blockID":             blockDocID,
	}, nil
}

// BuildLogDocument implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) BuildLogDocument(log chains.ChainLog, blockDocID, txDocID string) (map[string]interface{}, error) {
	ethLog, ok := log.(*EthereumLog)
	if !ok {
		return nil, fmt.Errorf("expected *EthereumLog, got %T", log)
	}

	internalLog := ethLog.GetInternalLog()
	if internalLog == nil {
		return nil, fmt.Errorf("internal log is nil")
	}

	blockInt, err := utils.HexToInt(internalLog.BlockNumber)
	if err != nil {
		blockInt, _ = strconv.ParseInt(internalLog.BlockNumber, 10, 64)
	}

	return map[string]interface{}{
		"address":          internalLog.Address,
		"topics":           internalLog.Topics,
		"data":             internalLog.Data,
		"blockNumber":      blockInt,
		"transactionHash":  internalLog.TransactionHash,
		"transactionIndex": internalLog.TransactionIndex,
		"blockHash":        internalLog.BlockHash,
		"logIndex":         internalLog.LogIndex,
		"removed":          fmt.Sprintf("%v", internalLog.Removed),
		"_transactionID":   txDocID,
		"_blockID":         blockDocID,
	}, nil
}

// BuildReceiptDocuments implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) BuildReceiptDocuments(receipt chains.ChainReceipt, blockDocID, txDocID string) ([]map[string]interface{}, error) {
	ethReceipt, ok := receipt.(*EthereumReceipt)
	if !ok {
		return nil, fmt.Errorf("expected *EthereumReceipt, got %T", receipt)
	}

	internalReceipt := ethReceipt.GetInternalReceipt()
	if internalReceipt == nil {
		return nil, fmt.Errorf("internal receipt is nil")
	}

	var docs []map[string]interface{}

	// Build log documents
	for i := range internalReceipt.Logs {
		log := NewEthereumLog(&internalReceipt.Logs[i])
		logDoc, err := b.BuildLogDocument(log, blockDocID, txDocID)
		if err != nil {
			return nil, err
		}
		docs = append(docs, logDoc)
	}

	return docs, nil
}

// BuildAccessListEntryDocument builds a document for an access list entry
func (b *DocumentBuilder) BuildAccessListEntryDocument(ale *types.AccessListEntry, txDocID string, blockNumber int64) (map[string]interface{}, error) {
	if ale == nil {
		return nil, fmt.Errorf("access list entry is nil")
	}

	return map[string]interface{}{
		"address":        ale.Address,
		"blockNumber":    blockNumber,
		"storageKeys":    ale.StorageKeys,
		"_transactionID": txDocID,
	}, nil
}

// GetCollectionNames implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) GetCollectionNames() chains.CollectionSet {
	return b.collections
}

// ChainName implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) ChainName() string {
	return string(chains.ChainTypeEthereum)
}

// NetworkName implements chains.ChainDocumentBuilder
func (b *DocumentBuilder) NetworkName() string {
	return string(b.network)
}

// init registers the Ethereum document builder factory
func init() {
	chains.RegisterDocumentBuilderFactory(chains.ChainTypeEthereum, func(chain chains.ChainType, network chains.NetworkType) chains.ChainDocumentBuilder {
		return NewDocumentBuilder(network)
	})
}
