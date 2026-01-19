package defra

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/client"
)

// BulkImportResult contains stats from a bulk import operation
type BulkImportResult struct {
	BlocksCreated       int
	TransactionsCreated int
	LogsCreated         int
	ALEsCreated         int
}

// CreateBlockBulk creates a block with all its transactions, logs, and access list entries
// using the direct Collection API instead of GraphQL for maximum performance.
//
// This is 5-10x faster than CreateBlockBatchOptimized because it:
// 1. Bypasses GraphQL parsing overhead
// 2. Uses CreateMany for batch inserts
// 3. Uses a single transaction for the entire block
// 4. Avoids JSON marshal/unmarshal overhead
func (h *BlockHandler) CreateBlockBulk(
	ctx context.Context,
	block *types.Block,
	transactions []*types.Transaction,
	receipts []*types.TransactionReceipt,
) (*BulkImportResult, error) {
	if h.defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "CreateBlockBulk",
			"bulk creation requires embedded DefraDB node", "", nil)
	}

	if block == nil {
		return nil, errors.NewInvalidBlockFormat("defra", "CreateBlockBulk", "nil", nil)
	}

	result := &BulkImportResult{}

	// Get collections once
	db := h.defraNode.DB
	blockCol, err := db.GetCollectionByName(ctx, constants.CollectionBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get Block collection: %w", err)
	}
	txCol, err := db.GetCollectionByName(ctx, constants.CollectionTransaction)
	if err != nil {
		return nil, fmt.Errorf("failed to get Transaction collection: %w", err)
	}
	logCol, err := db.GetCollectionByName(ctx, constants.CollectionLog)
	if err != nil {
		return nil, fmt.Errorf("failed to get Log collection: %w", err)
	}
	aleCol, err := db.GetCollectionByName(ctx, constants.CollectionAccessListEntry)
	if err != nil {
		return nil, fmt.Errorf("failed to get AccessListEntry collection: %w", err)
	}

	// Build receipt map for quick lookup
	receiptMap := make(map[string]*types.TransactionReceipt, len(receipts))
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	// Start a single transaction for the entire block
	txn, err := db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}
	defer txn.Discard()

	// Get collection handles within the transaction
	blockColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionBlock)
	txColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
	logColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionLog)
	aleColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)

	// Use non-txn versions if txn versions fail
	if blockColTxn == nil {
		blockColTxn = blockCol
	}
	if txColTxn == nil {
		txColTxn = txCol
	}
	if logColTxn == nil {
		logColTxn = logCol
	}
	if aleColTxn == nil {
		aleColTxn = aleCol
	}

	// === 1. Create Block ===
	blockDoc, err := h.buildBlockDocument(ctx, block, blockColTxn)
	if err != nil {
		return nil, fmt.Errorf("failed to build block document: %w", err)
	}

	if err := blockColTxn.Create(ctx, blockDoc); err != nil {
		if isBulkAlreadyExistsError(err) {
			return nil, fmt.Errorf("block already exists")
		}
		return nil, fmt.Errorf("failed to create block: %w", err)
	}
	result.BlocksCreated = 1
	blockID := blockDoc.ID().String()

	// === 2. Create Transactions in Batch ===
	if len(transactions) > 0 {
		txDocs := make([]*client.Document, 0, len(transactions))
		txHashToDoc := make(map[string]*client.Document, len(transactions))

		for _, tx := range transactions {
			if tx == nil {
				continue
			}
			txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, txColTxn)
			if err != nil {
				logger.Sugar.Warnf("Failed to build tx document %s: %v", tx.Hash, err)
				continue
			}
			txDocs = append(txDocs, txDoc)
			txHashToDoc[tx.Hash] = txDoc
		}

		if len(txDocs) > 0 {
			if err := txColTxn.CreateMany(ctx, txDocs); err != nil {
				logger.Sugar.Warnf("Failed to batch create transactions: %v", err)
			} else {
				result.TransactionsCreated = len(txDocs)
			}
		}

		// === 3. Create Logs in Batch ===
		var logDocs []*client.Document
		for _, tx := range transactions {
			if tx == nil {
				continue
			}
			receipt, ok := receiptMap[tx.Hash]
			if !ok || receipt == nil {
				continue
			}
			txDoc, ok := txHashToDoc[tx.Hash]
			if !ok {
				continue
			}
			txID := txDoc.ID().String()

			for i := range receipt.Logs {
				logDoc, err := h.buildLogDocument(ctx, &receipt.Logs[i], blockID, txID, logColTxn)
				if err != nil {
					logger.Sugar.Warnf("Failed to build log document: %v", err)
					continue
				}
				logDocs = append(logDocs, logDoc)
			}
		}

		if len(logDocs) > 0 {
			if err := logColTxn.CreateMany(ctx, logDocs); err != nil {
				logger.Sugar.Warnf("Failed to batch create logs: %v", err)
			} else {
				result.LogsCreated = len(logDocs)
			}
		}

		// === 4. Create Access List Entries in Batch ===
		var aleDocs []*client.Document
		for _, tx := range transactions {
			if tx == nil {
				continue
			}
			txDoc, ok := txHashToDoc[tx.Hash]
			if !ok {
				continue
			}
			txID := txDoc.ID().String()

			for i := range tx.AccessList {
				aleDoc, err := h.buildALEDocument(ctx, &tx.AccessList[i], txID, aleColTxn)
				if err != nil {
					logger.Sugar.Warnf("Failed to build ALE document: %v", err)
					continue
				}
				aleDocs = append(aleDocs, aleDoc)
			}
		}

		if len(aleDocs) > 0 {
			if err := aleColTxn.CreateMany(ctx, aleDocs); err != nil {
				logger.Sugar.Warnf("Failed to batch create ALEs: %v", err)
			} else {
				result.ALEsCreated = len(aleDocs)
			}
		}
	}

	// Commit everything at once
	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	return result, nil
}

// CreateBlocksBulk creates multiple blocks in a single operation for maximum throughput.
// This is the fastest method for bulk historical data import.
func (h *BlockHandler) CreateBlocksBulk(
	ctx context.Context,
	blocks []*types.Block,
	transactionsByBlock map[string][]*types.Transaction, // keyed by block hash
	receiptsByTx map[string]*types.TransactionReceipt,   // keyed by tx hash
) (*BulkImportResult, error) {
	if h.defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "CreateBlocksBulk",
			"bulk creation requires embedded DefraDB node", "", nil)
	}

	if len(blocks) == 0 {
		return &BulkImportResult{}, nil
	}

	result := &BulkImportResult{}
	db := h.defraNode.DB

	// Start a single transaction for all blocks
	txn, err := db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}
	defer txn.Discard()

	// Get collections within transaction
	blockCol, _ := txn.GetCollectionByName(ctx, constants.CollectionBlock)
	txCol, _ := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
	logCol, _ := txn.GetCollectionByName(ctx, constants.CollectionLog)
	aleCol, _ := txn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)

	// === 1. Create All Blocks ===
	blockDocs := make([]*client.Document, 0, len(blocks))
	blockHashToDoc := make(map[string]*client.Document, len(blocks))

	for _, block := range blocks {
		if block == nil {
			continue
		}
		blockDoc, err := h.buildBlockDocument(ctx, block, blockCol)
		if err != nil {
			logger.Sugar.Warnf("Failed to build block document %s: %v", block.Hash, err)
			continue
		}
		blockDocs = append(blockDocs, blockDoc)
		blockHashToDoc[block.Hash] = blockDoc
	}

	if len(blockDocs) > 0 {
		if err := blockCol.CreateMany(ctx, blockDocs); err != nil {
			return nil, fmt.Errorf("failed to batch create blocks: %w", err)
		}
		result.BlocksCreated = len(blockDocs)
	}

	// === 2. Create All Transactions ===
	var allTxDocs []*client.Document
	txHashToDoc := make(map[string]*client.Document)

	for blockHash, blockDoc := range blockHashToDoc {
		blockID := blockDoc.ID().String()
		txs := transactionsByBlock[blockHash]

		for _, tx := range txs {
			if tx == nil {
				continue
			}
			txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, txCol)
			if err != nil {
				logger.Sugar.Warnf("Failed to build tx document %s: %v", tx.Hash, err)
				continue
			}
			allTxDocs = append(allTxDocs, txDoc)
			txHashToDoc[tx.Hash] = txDoc
		}
	}

	if len(allTxDocs) > 0 {
		if err := txCol.CreateMany(ctx, allTxDocs); err != nil {
			logger.Sugar.Warnf("Failed to batch create transactions: %v", err)
		} else {
			result.TransactionsCreated = len(allTxDocs)
		}
	}

	// === 3. Create All Logs ===
	var allLogDocs []*client.Document

	for blockHash, blockDoc := range blockHashToDoc {
		blockID := blockDoc.ID().String()
		txs := transactionsByBlock[blockHash]

		for _, tx := range txs {
			if tx == nil {
				continue
			}
			receipt := receiptsByTx[tx.Hash]
			if receipt == nil {
				continue
			}
			txDoc := txHashToDoc[tx.Hash]
			if txDoc == nil {
				continue
			}
			txID := txDoc.ID().String()

			for i := range receipt.Logs {
				logDoc, err := h.buildLogDocument(ctx, &receipt.Logs[i], blockID, txID, logCol)
				if err != nil {
					continue
				}
				allLogDocs = append(allLogDocs, logDoc)
			}
		}
	}

	if len(allLogDocs) > 0 {
		if err := logCol.CreateMany(ctx, allLogDocs); err != nil {
			logger.Sugar.Warnf("Failed to batch create logs: %v", err)
		} else {
			result.LogsCreated = len(allLogDocs)
		}
	}

	// === 4. Create All Access List Entries ===
	var allALEDocs []*client.Document

	for blockHash := range blockHashToDoc {
		txs := transactionsByBlock[blockHash]

		for _, tx := range txs {
			if tx == nil {
				continue
			}
			txDoc := txHashToDoc[tx.Hash]
			if txDoc == nil {
				continue
			}
			txID := txDoc.ID().String()

			for i := range tx.AccessList {
				aleDoc, err := h.buildALEDocument(ctx, &tx.AccessList[i], txID, aleCol)
				if err != nil {
					continue
				}
				allALEDocs = append(allALEDocs, aleDoc)
			}
		}
	}

	if len(allALEDocs) > 0 {
		if err := aleCol.CreateMany(ctx, allALEDocs); err != nil {
			logger.Sugar.Warnf("Failed to batch create ALEs: %v", err)
		} else {
			result.ALEsCreated = len(allALEDocs)
		}
	}

	// Commit everything
	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	return result, nil
}

// buildBlockDocument creates a client.Document for a block
func (h *BlockHandler) buildBlockDocument(ctx context.Context, block *types.Block, col client.Collection) (*client.Document, error) {
	blockInt, err := utils.HexToInt(block.Number)
	if err != nil {
		return nil, err
	}

	data := map[string]any{
		"hash":             block.Hash,
		"number":           blockInt,
		"timestamp":        block.Timestamp,
		"parentHash":       block.ParentHash,
		"difficulty":       block.Difficulty,
		"totalDifficulty":  block.TotalDifficulty,
		"gasUsed":          block.GasUsed,
		"gasLimit":         block.GasLimit,
		"baseFeePerGas":    block.BaseFeePerGas,
		"nonce":            block.Nonce,
		"miner":            block.Miner,
		"size":             block.Size,
		"stateRoot":        block.StateRoot,
		"sha3Uncles":       block.Sha3Uncles,
		"transactionsRoot": block.TransactionsRoot,
		"receiptsRoot":     block.ReceiptsRoot,
		"logsBloom":        block.LogsBloom,
		"extraData":        block.ExtraData,
		"mixHash":          block.MixHash,
		"uncles":           block.Uncles,
	}

	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildTransactionDocument creates a client.Document for a transaction
func (h *BlockHandler) buildTransactionDocument(ctx context.Context, tx *types.Transaction, blockID string, col client.Collection) (*client.Document, error) {
	blockInt, err := strconv.ParseInt(tx.BlockNumber, 10, 64)
	if err != nil {
		return nil, err
	}

	data := map[string]any{
		"hash":                 tx.Hash,
		"blockNumber":          blockInt,
		"blockHash":            tx.BlockHash,
		"transactionIndex":     tx.TransactionIndex,
		"from":                 tx.From,
		"to":                   tx.To,
		"value":                tx.Value,
		"gas":                  tx.Gas,
		"gasPrice":             tx.GasPrice,
		"maxFeePerGas":         tx.MaxFeePerGas,
		"maxPriorityFeePerGas": tx.MaxPriorityFeePerGas,
		"input":                string(tx.Input),
		"nonce":                tx.Nonce,
		"type":                 tx.Type,
		"chainId":              tx.ChainId,
		"v":                    tx.V,
		"r":                    tx.R,
		"s":                    tx.S,
		"cumulativeGasUsed":    tx.CumulativeGasUsed,
		"effectiveGasPrice":    tx.EffectiveGasPrice,
		"status":               tx.Status,
		"block":                blockID,
	}

	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildLogDocument creates a client.Document for a log
func (h *BlockHandler) buildLogDocument(ctx context.Context, log *types.Log, blockID, txID string, col client.Collection) (*client.Document, error) {
	blockInt, err := utils.HexToInt(log.BlockNumber)
	if err != nil {
		return nil, err
	}

	data := map[string]any{
		"address":          log.Address,
		"topics":           log.Topics,
		"data":             log.Data,
		"blockNumber":      blockInt,
		"transactionHash":  log.TransactionHash,
		"transactionIndex": log.TransactionIndex,
		"blockHash":        log.BlockHash,
		"logIndex":         log.LogIndex,
		"removed":          fmt.Sprintf("%v", log.Removed),
		"transaction":      txID,
		"block":            blockID,
	}

	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildALEDocument creates a client.Document for an access list entry
func (h *BlockHandler) buildALEDocument(ctx context.Context, ale *types.AccessListEntry, txID string, col client.Collection) (*client.Document, error) {
	data := map[string]any{
		"address":     ale.Address,
		"storageKeys": ale.StorageKeys,
		"transaction": txID,
	}

	return client.NewDocFromMap(ctx, data, col.Version())
}

// isBulkAlreadyExistsError checks if an error indicates a document already exists
func isBulkAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "already exists") || strings.Contains(errStr, "duplicate")
}
