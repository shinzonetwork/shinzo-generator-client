package testutils

// TODO: Once pkg/chains/evm/adapter.go is removed, the import cycle
// (testutils → chains/evm → testutils via evm test files) is broken. At that
// point this file should be replaced with a thin wrapper calling
// evm.NewConverter(nil).Convert(ctx, &evm.BlockBundle{...}) to eliminate
// the data-map duplication with evm/converter.go.

import (
	"fmt"
	"strconv"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// NoopStamper is a chains.LinkStamper that does nothing. Used by test helpers
// that don't need link resolution.
type NoopStamper struct{}

// StampLinks implements chains.LinkStamper as a no-op.
func (NoopStamper) StampLinks(_ []chains.DocumentGroup, _ string, _ []map[string]any, _ []string) {
}

// WrapResult bundles groups and a signature collection name into a
// chains.ConversionResult with a NoopStamper. Used by test call sites that
// need to pass a ConversionResult to BlockHandler.Store or SignExisting.
func WrapResult(groups []chains.DocumentGroup, sigCol string) chains.ConversionResult {
	return chains.ConversionResult{
		Groups:              groups,
		SignatureCollection: sigCol,
		LinkStamper:         NoopStamper{},
	}
}

// BuildEVMGroups builds a chains.ConversionResult from raw EVM block types,
// producing document groups for block, transactions, logs, and access list
// entries with a NoopStamper.
func BuildEVMGroups(
	collections chains.Collections,
	block *types.Block,
	txs []*types.Transaction,
	receipts []*types.TransactionReceipt,
) (chains.ConversionResult, error) {
	if block == nil {
		return chains.ConversionResult{}, fmt.Errorf("nil block")
	}

	blockInt, err := utils.HexToInt(block.Number)
	if err != nil {
		return chains.ConversionResult{}, fmt.Errorf("parse block number: %w", err)
	}

	blockCol, _ := collections.GetCollection(chains.TypeBlock)
	txCol, _ := collections.GetCollection(chains.TypeTransaction)
	logCol, _ := collections.GetCollection(chains.TypeLog)
	aleCol, _ := collections.GetCollection(chains.TypeAccessListEntry)
	sigCol, _ := collections.GetCollection(chains.TypeBlockSignature)

	blockData := buildBlockMap(block, blockInt)
	txDocs := buildTxDocs(txs)
	receiptMap := buildReceiptMap(receipts)
	logDocs := buildLogDocs(txs, receiptMap)
	aleDocs, _ := buildALEDocs(txs, blockInt)

	groups := []chains.DocumentGroup{
		{Collection: blockCol, Docs: []map[string]any{blockData}, BlockNumField: constants.NumberFieldValue},
	}
	if len(txDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: txCol, Docs: txDocs, BlockNumField: constants.BlockNumberKeyValue})
	}
	if len(logDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: logCol, Docs: logDocs, BlockNumField: constants.BlockNumberKeyValue})
	}
	if len(aleDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: aleCol, Docs: aleDocs, BlockNumField: constants.BlockNumberKeyValue})
	}

	return chains.ConversionResult{
		Groups:              groups,
		SignatureCollection: sigCol,
		LinkStamper:         NoopStamper{},
	}, nil
}

func buildBlockMap(block *types.Block, blockInt int64) map[string]any {
	return map[string]any{
		constants.HashKeyValue:     block.Hash,
		constants.NumberFieldValue: blockInt,
		"timestamp":                block.Timestamp,
		"parentHash":               block.ParentHash,
		"difficulty":               block.Difficulty,
		"totalDifficulty":          block.TotalDifficulty,
		"gasUsed":                  block.GasUsed,
		"gasLimit":                 block.GasLimit,
		"baseFeePerGas":            block.BaseFeePerGas,
		"nonce":                    block.Nonce,
		"miner":                    block.Miner,
		"size":                     block.Size,
		"stateRoot":                block.StateRoot,
		"sha3Uncles":               block.Sha3Uncles,
		"transactionsRoot":         block.TransactionsRoot,
		"receiptsRoot":             block.ReceiptsRoot,
		"logsBloom":                block.LogsBloom,
		"extraData":                block.ExtraData,
		"mixHash":                  block.MixHash,
		"uncles":                   block.Uncles,
	}
}

func buildTxDocs(txs []*types.Transaction) []map[string]any {
	var docs []map[string]any
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		docs = append(docs, buildTxMap(tx))
	}
	return docs
}

func buildTxMap(tx *types.Transaction) map[string]any {
	txBlockNum, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
	return map[string]any{
		constants.HashKeyValue:        tx.Hash,
		constants.BlockNumberKeyValue: txBlockNum,
		constants.BlockHashKeyValue:   tx.BlockHash,
		"transactionIndex":            tx.TransactionIndex,
		"from":                        tx.From,
		"to":                          tx.To,
		"value":                       tx.Value,
		"gas":                         tx.Gas,
		"gasPrice":                    tx.GasPrice,
		"maxFeePerGas":                tx.MaxFeePerGas,
		"maxPriorityFeePerGas":        tx.MaxPriorityFeePerGas,
		"input":                       tx.Input,
		"nonce":                       tx.Nonce,
		"type":                        tx.Type,
		"chainId":                     tx.ChainID,
		"v":                           tx.V,
		"r":                           tx.R,
		"s":                           tx.S,
		"cumulativeGasUsed":           tx.CumulativeGasUsed,
		"effectiveGasPrice":           tx.EffectiveGasPrice,
		"status":                      tx.Status,
	}
}

func buildReceiptMap(receipts []*types.TransactionReceipt) map[string]*types.TransactionReceipt {
	m := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			m[receipt.TransactionHash] = receipt
		}
	}
	return m
}

func buildLogDocs(txs []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) []map[string]any {
	var docs []map[string]any
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		receipt, ok := receiptMap[tx.Hash]
		if !ok || receipt == nil {
			continue
		}
		for i := range receipt.Logs {
			docs = append(docs, buildLogMap(&receipt.Logs[i]))
		}
	}
	return docs
}

func buildLogMap(logEntry *types.Log) map[string]any {
	logBlockNum, _ := utils.HexToInt(logEntry.BlockNumber)
	return map[string]any{
		constants.AddressKeyValue:         logEntry.Address,
		"topics":                          logEntry.Topics,
		"data":                            logEntry.Data,
		constants.BlockNumberKeyValue:     logBlockNum,
		constants.TransactionHashKeyValue: logEntry.TransactionHash,
		"transactionIndex":                logEntry.TransactionIndex,
		constants.BlockHashKeyValue:       logEntry.BlockHash,
		"logIndex":                        logEntry.LogIndex,
		"removed":                         fmt.Sprintf("%v", logEntry.Removed),
	}
}

func buildALEDocs(txs []*types.Transaction, blockInt int64) ([]map[string]any, []string) {
	var docs []map[string]any
	var parentRefs []string
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		for i := range tx.AccessList {
			docs = append(docs, buildALEMap(&tx.AccessList[i], blockInt))
			parentRefs = append(parentRefs, tx.Hash)
		}
	}
	return docs, parentRefs
}

func buildALEMap(ale *types.AccessListEntry, blockNumber int64) map[string]any {
	return map[string]any{
		constants.AddressKeyValue:     ale.Address,
		constants.BlockNumberKeyValue: blockNumber,
		"storageKeys":                 ale.StorageKeys,
	}
}
