package testutils

// TODO: Once pkg/chains/evm/adapter.go is removed, the import cycle
// (testutils → chains/evm → testutils via evm test files) is broken. At that
// point this file should be replaced with a thin wrapper calling
// evm.NewConverter(nil).Convert(ctx, &evm.BlockBundle{...}, nil) to eliminate
// the data-map duplication with evm/converter.go.

import (
	"fmt"
	"strconv"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

func BuildEVMGroups(
	collections chains.Collections,
	block *types.Block,
	txs []*types.Transaction,
	receipts []*types.TransactionReceipt,
) ([]chains.DocumentGroup, string, error) {
	if block == nil {
		return nil, "", fmt.Errorf("nil block")
	}

	blockInt, err := utils.HexToInt(block.Number)
	if err != nil {
		return nil, "", fmt.Errorf("parse block number: %w", err)
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
	aleDocs, aleParentRefs := buildALEDocs(txs, blockInt)

	groups := []chains.DocumentGroup{
		{Collection: blockCol, Docs: []map[string]any{blockData}},
	}
	if len(txDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: txCol, Docs: txDocs})
	}
	if len(logDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: logCol, Docs: logDocs})
	}
	if len(aleDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection: aleCol,
			Docs:       aleDocs,
			ParentRef:  aleParentRefs,
		})
	}

	return groups, sigCol, nil
}

func buildBlockMap(block *types.Block, blockInt int64) map[string]any {
	return map[string]any{
		"hash":                     block.Hash,
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
		"hash":                        tx.Hash,
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
		"address":                     logEntry.Address,
		"topics":                      logEntry.Topics,
		"data":                        logEntry.Data,
		constants.BlockNumberKeyValue: logBlockNum,
		"transactionHash":             logEntry.TransactionHash,
		"transactionIndex":            logEntry.TransactionIndex,
		constants.BlockHashKeyValue:   logEntry.BlockHash,
		"logIndex":                    logEntry.LogIndex,
		"removed":                     fmt.Sprintf("%v", logEntry.Removed),
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
		"address":                     ale.Address,
		constants.BlockNumberKeyValue: blockNumber,
		"storageKeys":                 ale.StorageKeys,
	}
}
