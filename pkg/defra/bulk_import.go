package defra

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

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

// cachedCollections holds pre-fetched collection references to avoid repeated lookups
type cachedCollections struct {
	block       client.Collection
	transaction client.Collection
	log         client.Collection
	ale         client.Collection
	mu          sync.RWMutex
	initialized bool
}

var collectionCache = &cachedCollections{}

// getCollections returns cached collections or fetches them once
func (h *BlockHandler) getCollections(ctx context.Context) (client.Collection, client.Collection, client.Collection, client.Collection, error) {
	collectionCache.mu.RLock()
	if collectionCache.initialized {
		defer collectionCache.mu.RUnlock()
		return collectionCache.block, collectionCache.transaction, collectionCache.log, collectionCache.ale, nil
	}
	collectionCache.mu.RUnlock()

	// Need to initialize
	collectionCache.mu.Lock()
	defer collectionCache.mu.Unlock()

	// Double-check after acquiring write lock
	if collectionCache.initialized {
		return collectionCache.block, collectionCache.transaction, collectionCache.log, collectionCache.ale, nil
	}

	db := h.defraNode.DB

	blockCol, err := db.GetCollectionByName(ctx, constants.CollectionBlock)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to get Block collection: %w", err)
	}

	txCol, err := db.GetCollectionByName(ctx, constants.CollectionTransaction)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to get Transaction collection: %w", err)
	}

	logCol, err := db.GetCollectionByName(ctx, constants.CollectionLog)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to get Log collection: %w", err)
	}

	aleCol, err := db.GetCollectionByName(ctx, constants.CollectionAccessListEntry)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to get AccessListEntry collection: %w", err)
	}

	collectionCache.block = blockCol
	collectionCache.transaction = txCol
	collectionCache.log = logCol
	collectionCache.ale = aleCol
	collectionCache.initialized = true

	return blockCol, txCol, logCol, aleCol, nil
}

// CreateBlockBulk creates a block with all its transactions, logs, and access list entries
// using the direct Collection API instead of GraphQL for maximum performance.
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

	// Get cached collections
	blockCol, txCol, logCol, aleCol, err := h.getCollections(ctx)
	if err != nil {
		return nil, err
	}

	// Build receipt map for quick lookup
	receiptMap := make(map[string]*types.TransactionReceipt, len(receipts))
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	// Start a single transaction for the entire block
	db := h.defraNode.DB
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

	// Use cached versions if txn versions fail
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
	blockDoc, err := h.buildBlockDocumentFast(ctx, block, blockColTxn)
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
			txDoc, err := h.buildTransactionDocumentFast(ctx, tx, blockID, txColTxn)
			if err != nil {
				continue // Skip bad documents silently for speed
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
		// Pre-allocate with estimated capacity
		estimatedLogs := len(transactions) * 3 // Average ~3 logs per tx
		logDocs := make([]*client.Document, 0, estimatedLogs)

		for _, tx := range transactions {
			if tx == nil {
				continue
			}
			receipt := receiptMap[tx.Hash]
			if receipt == nil {
				continue
			}
			txDoc := txHashToDoc[tx.Hash]
			if txDoc == nil {
				continue
			}
			txID := txDoc.ID().String()

			for i := range receipt.Logs {
				logDoc, err := h.buildLogDocumentFast(ctx, &receipt.Logs[i], blockID, txID, logColTxn)
				if err != nil {
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
			if tx == nil || len(tx.AccessList) == 0 {
				continue
			}
			txDoc := txHashToDoc[tx.Hash]
			if txDoc == nil {
				continue
			}
			txID := txDoc.ID().String()

			for i := range tx.AccessList {
				aleDoc, err := h.buildALEDocumentFast(ctx, &tx.AccessList[i], txID, aleColTxn)
				if err != nil {
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

// CreateMultiBlockBulk creates multiple blocks, handling large batches by chunking.
// Falls back to single-block processing if transaction is too large.
func (h *BlockHandler) CreateMultiBlockBulk(
	ctx context.Context,
	blocks []*types.Block,
	txsByBlockHash map[string][]*types.Transaction,
	receiptsByTxHash map[string]*types.TransactionReceipt,
) (*BulkImportResult, error) {
	if h.defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "CreateMultiBlockBulk",
			"bulk creation requires embedded DefraDB node", "", nil)
	}

	if len(blocks) == 0 {
		return &BulkImportResult{}, nil
	}

	// Process blocks one at a time to avoid transaction size limits
	// This is more reliable than trying to batch and failing
	totalResult := &BulkImportResult{}

	for _, block := range blocks {
		if block == nil {
			continue
		}

		txs := txsByBlockHash[block.Hash]
		receipts := make([]*types.TransactionReceipt, 0, len(txs))
		for _, tx := range txs {
			if r := receiptsByTxHash[tx.Hash]; r != nil {
				receipts = append(receipts, r)
			}
		}

		result, err := h.createBlockWithChunkedLogs(ctx, block, txs, receipts)
		if err != nil {
			if isBulkAlreadyExistsError(err) {
				continue
			}
			logger.Sugar.Warnf("Failed to import block %s: %v", block.Number, err)
			continue
		}

		totalResult.BlocksCreated += result.BlocksCreated
		totalResult.TransactionsCreated += result.TransactionsCreated
		totalResult.LogsCreated += result.LogsCreated
		totalResult.ALEsCreated += result.ALEsCreated
	}

	return totalResult, nil
}

// createBlockWithChunkedLogs creates a block, chunking logs if needed to avoid txn size limits
func (h *BlockHandler) createBlockWithChunkedLogs(
	ctx context.Context,
	block *types.Block,
	transactions []*types.Transaction,
	receipts []*types.TransactionReceipt,
) (*BulkImportResult, error) {
	result := &BulkImportResult{}
	db := h.defraNode.DB

	// Get cached collections
	blockCol, txCol, logCol, aleCol, err := h.getCollections(ctx)
	if err != nil {
		return nil, err
	}

	// Build receipt map
	receiptMap := make(map[string]*types.TransactionReceipt, len(receipts))
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	// Count total logs to determine chunking strategy
	totalLogs := 0
	for _, receipt := range receipts {
		if receipt != nil {
			totalLogs += len(receipt.Logs)
		}
	}

	// If small enough, do it all in one transaction
	const maxLogsPerTxn = 500
	if totalLogs <= maxLogsPerTxn {
		return h.createBlockSingleTxn(ctx, block, transactions, receiptMap, blockCol, txCol, logCol, aleCol)
	}

	// Large block: create block and txs first, then chunk the logs
	txn, err := db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	blockColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionBlock)
	txColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
	if blockColTxn == nil {
		blockColTxn = blockCol
	}
	if txColTxn == nil {
		txColTxn = txCol
	}

	// Create block
	blockDoc, err := h.buildBlockDocumentFast(ctx, block, blockColTxn)
	if err != nil {
		txn.Discard()
		return nil, err
	}
	if err := blockColTxn.Create(ctx, blockDoc); err != nil {
		txn.Discard()
		return nil, err
	}
	blockID := blockDoc.ID().String()
	result.BlocksCreated = 1

	// Create transactions
	txHashToID := make(map[string]string, len(transactions))
	txDocs := make([]*client.Document, 0, len(transactions))
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocumentFast(ctx, tx, blockID, txColTxn)
		if err != nil {
			continue
		}
		txDocs = append(txDocs, txDoc)
		txHashToID[tx.Hash] = txDoc.ID().String()
	}

	if len(txDocs) > 0 {
		if err := txColTxn.CreateMany(ctx, txDocs); err != nil {
			txn.Discard()
			return nil, fmt.Errorf("failed to create transactions: %w", err)
		}
		result.TransactionsCreated = len(txDocs)
	}

	// Commit block and transactions
	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit block/txs: %w", err)
	}

	// Now create logs in chunks with separate transactions
	var allLogs []logEntry
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		receipt := receiptMap[tx.Hash]
		if receipt == nil {
			continue
		}
		txID := txHashToID[tx.Hash]
		for i := range receipt.Logs {
			allLogs = append(allLogs, logEntry{
				log:     &receipt.Logs[i],
				blockID: blockID,
				txID:    txID,
			})
		}
	}

	// Process logs in chunks
	for i := 0; i < len(allLogs); i += maxLogsPerTxn {
		end := i + maxLogsPerTxn
		if end > len(allLogs) {
			end = len(allLogs)
		}
		chunk := allLogs[i:end]

		logTxn, err := db.NewTxn(false)
		if err != nil {
			continue
		}

		logColTxn, _ := logTxn.GetCollectionByName(ctx, constants.CollectionLog)
		if logColTxn == nil {
			logColTxn = logCol
		}

		logDocs := make([]*client.Document, 0, len(chunk))
		for _, entry := range chunk {
			logDoc, err := h.buildLogDocumentFast(ctx, entry.log, entry.blockID, entry.txID, logColTxn)
			if err != nil {
				continue
			}
			logDocs = append(logDocs, logDoc)
		}

		if len(logDocs) > 0 {
			if err := logColTxn.CreateMany(ctx, logDocs); err != nil {
				logTxn.Discard()
				continue
			}
		}

		if err := logTxn.Commit(); err != nil {
			continue
		}
		result.LogsCreated += len(logDocs)
	}

	// Create ALEs (usually small, single transaction)
	var aleEntries []aleEntry
	for _, tx := range transactions {
		if tx == nil || len(tx.AccessList) == 0 {
			continue
		}
		txID := txHashToID[tx.Hash]
		for i := range tx.AccessList {
			aleEntries = append(aleEntries, aleEntry{
				ale:  &tx.AccessList[i],
				txID: txID,
			})
		}
	}

	if len(aleEntries) > 0 {
		aleTxn, err := db.NewTxn(false)
		if err == nil {
			aleColTxn, _ := aleTxn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)
			if aleColTxn == nil {
				aleColTxn = aleCol
			}

			aleDocs := make([]*client.Document, 0, len(aleEntries))
			for _, entry := range aleEntries {
				aleDoc, err := h.buildALEDocumentFast(ctx, entry.ale, entry.txID, aleColTxn)
				if err != nil {
					continue
				}
				aleDocs = append(aleDocs, aleDoc)
			}

			if len(aleDocs) > 0 {
				if err := aleColTxn.CreateMany(ctx, aleDocs); err == nil {
					if err := aleTxn.Commit(); err == nil {
						result.ALEsCreated = len(aleDocs)
					}
				} else {
					aleTxn.Discard()
				}
			} else {
				aleTxn.Discard()
			}
		}
	}

	return result, nil
}

// createBlockSingleTxn creates a block with all data in a single transaction (for small blocks)
func (h *BlockHandler) createBlockSingleTxn(
	ctx context.Context,
	block *types.Block,
	transactions []*types.Transaction,
	receiptMap map[string]*types.TransactionReceipt,
	blockCol, txCol, logCol, aleCol client.Collection,
) (*BulkImportResult, error) {
	result := &BulkImportResult{}
	db := h.defraNode.DB

	txn, err := db.NewTxn(false)
	if err != nil {
		return nil, err
	}
	defer txn.Discard()

	blockColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionBlock)
	txColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
	logColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionLog)
	aleColTxn, _ := txn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)

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

	// Create block
	blockDoc, err := h.buildBlockDocumentFast(ctx, block, blockColTxn)
	if err != nil {
		return nil, err
	}
	if err := blockColTxn.Create(ctx, blockDoc); err != nil {
		return nil, err
	}
	blockID := blockDoc.ID().String()
	result.BlocksCreated = 1

	// Create transactions
	txHashToID := make(map[string]string, len(transactions))
	txDocs := make([]*client.Document, 0, len(transactions))
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocumentFast(ctx, tx, blockID, txColTxn)
		if err != nil {
			continue
		}
		txDocs = append(txDocs, txDoc)
		txHashToID[tx.Hash] = txDoc.ID().String()
	}

	if len(txDocs) > 0 {
		if err := txColTxn.CreateMany(ctx, txDocs); err == nil {
			result.TransactionsCreated = len(txDocs)
		}
	}

	// Create logs
	var logDocs []*client.Document
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		receipt := receiptMap[tx.Hash]
		if receipt == nil {
			continue
		}
		txID := txHashToID[tx.Hash]
		for i := range receipt.Logs {
			logDoc, err := h.buildLogDocumentFast(ctx, &receipt.Logs[i], blockID, txID, logColTxn)
			if err != nil {
				continue
			}
			logDocs = append(logDocs, logDoc)
		}
	}

	if len(logDocs) > 0 {
		if err := logColTxn.CreateMany(ctx, logDocs); err == nil {
			result.LogsCreated = len(logDocs)
		}
	}

	// Create ALEs
	var aleDocs []*client.Document
	for _, tx := range transactions {
		if tx == nil || len(tx.AccessList) == 0 {
			continue
		}
		txID := txHashToID[tx.Hash]
		for i := range tx.AccessList {
			aleDoc, err := h.buildALEDocumentFast(ctx, &tx.AccessList[i], txID, aleColTxn)
			if err != nil {
				continue
			}
			aleDocs = append(aleDocs, aleDoc)
		}
	}

	if len(aleDocs) > 0 {
		if err := aleColTxn.CreateMany(ctx, aleDocs); err == nil {
			result.ALEsCreated = len(aleDocs)
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, err
	}

	return result, nil
}

type logEntry struct {
	log     *types.Log
	blockID string
	txID    string
}

type aleEntry struct {
	ale  *types.AccessListEntry
	txID string
}

// buildBlockDocumentFast creates a client.Document for a block (optimized)
func (h *BlockHandler) buildBlockDocumentFast(ctx context.Context, block *types.Block, col client.Collection) (*client.Document, error) {
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

// buildTransactionDocumentFast creates a client.Document for a transaction (optimized)
func (h *BlockHandler) buildTransactionDocumentFast(ctx context.Context, tx *types.Transaction, blockID string, col client.Collection) (*client.Document, error) {
	blockInt, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)

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

// buildLogDocumentFast creates a client.Document for a log (optimized)
func (h *BlockHandler) buildLogDocumentFast(ctx context.Context, log *types.Log, blockID, txID string, col client.Collection) (*client.Document, error) {
	blockInt, _ := utils.HexToInt(log.BlockNumber)

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

// buildALEDocumentFast creates a client.Document for an access list entry (optimized)
func (h *BlockHandler) buildALEDocumentFast(ctx context.Context, ale *types.AccessListEntry, txID string, col client.Collection) (*client.Document, error) {
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

// Legacy aliases for backwards compatibility
func (h *BlockHandler) buildBlockDocument(ctx context.Context, block *types.Block, col client.Collection) (*client.Document, error) {
	return h.buildBlockDocumentFast(ctx, block, col)
}

func (h *BlockHandler) buildTransactionDocument(ctx context.Context, tx *types.Transaction, blockID string, col client.Collection) (*client.Document, error) {
	return h.buildTransactionDocumentFast(ctx, tx, blockID, col)
}

func (h *BlockHandler) buildLogDocument(ctx context.Context, log *types.Log, blockID, txID string, col client.Collection) (*client.Document, error) {
	return h.buildLogDocumentFast(ctx, log, blockID, txID, col)
}

func (h *BlockHandler) buildALEDocument(ctx context.Context, ale *types.AccessListEntry, txID string, col client.Collection) (*client.Document, error) {
	return h.buildALEDocumentFast(ctx, ale, txID, col)
}
