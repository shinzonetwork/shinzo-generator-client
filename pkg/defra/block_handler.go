package defra

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/node"
)

// BlockCreationResult holds the result of creating a block, including all docIDs
type BlockCreationResult struct {
	BlockID          string
	BlockNumber      int64
	TransactionIDs   []string
	LogIDs           []string
	AccessListIDs    []string
	BatchSignatureID string
}

// DocIDTrackerInterface defines the interface for tracking docIDs
type DocIDTrackerInterface interface {
	TrackBlock(ctx context.Context, blockNumber int64, result *BlockCreationResult) error
}

type BlockHandler struct {
	defraNode     *node.Node            // Direct access to embedded DefraDB
	maxDocsPerTxn int                   // Threshold for single-txn vs batched block creation
	docIDTracker  DocIDTrackerInterface // Optional tracker for docIDs

	// Document throughput metrics
	metricsWindowStart  time.Time
	docsCreatedInWindow int
}

// logEntry holds a log and its associated transaction ID for batched processing
type logEntry struct {
	log  *types.Log
	txID string
}

// aleEntry holds an access list entry and its associated transaction ID for batched processing
type aleEntry struct {
	ale         *types.AccessListEntry
	txID        string
	blockNumber int64
}

// NewBlockHandler creates a BlockHandler that uses direct DB calls.
// maxDocsPerTxn is the threshold for single-txn vs batched block creation.
func NewBlockHandler(defraNode *node.Node, maxDocsPerTxn int) (*BlockHandler, error) {
	if defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "NewBlockHandler",
			"defraNode is nil", "", nil)
	}
	if maxDocsPerTxn <= 0 {
		maxDocsPerTxn = 1000
	}
	return &BlockHandler{
		defraNode:     defraNode,
		maxDocsPerTxn: maxDocsPerTxn,
	}, nil
}

// SetDocIDTracker sets the tracker for recording docIDs at insert time
func (h *BlockHandler) SetDocIDTracker(tracker DocIDTrackerInterface) {
	h.docIDTracker = tracker
}

// CreateBlockBatch creates a block with all its transactions, logs, and access list entries.
func (h *BlockHandler) CreateBlockBatch(ctx context.Context, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) (string, error) {
	if h.defraNode == nil {
		return "", errors.NewConfigurationError("defra", "CreateBlockBatch",
			"batch creation requires embedded DefraDB node", "", nil)
	}

	if block == nil {
		return "", errors.NewInvalidBlockFormat("defra", "CreateBlockBatch", "nil", nil)
	}

	blockInt, err := utils.HexToInt(block.Number)
	if err != nil {
		return "", err
	}

	receiptMap := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	totalLogs := 0
	totalALEs := 0
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		if receipt, ok := receiptMap[tx.Hash]; ok && receipt != nil {
			totalLogs += len(receipt.Logs)
		}
		totalALEs += len(tx.AccessList)
	}
	totalDocs := 1 + len(transactions) + totalLogs + totalALEs

	if totalDocs <= h.maxDocsPerTxn {
		return h.createBlockSingleTransaction(ctx, block, blockInt, transactions, receiptMap)
	}

	return h.createBlockBatched(ctx, block, blockInt, transactions, receiptMap)
}

// createBlockSingleTransaction creates the entire block in a single DB transaction.
// Block and BatchSignature are created as separate documents in the same transaction.
// This ensures all documents arrive via P2P together, and the host can listen for
// BatchSignature events to create attestations.
func (h *BlockHandler) createBlockSingleTransaction(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	txn, err := h.defraNode.DB.NewBlindWriteTxn()
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transaction", err)
	}
	ctx = h.defraNode.DB.InitContext(ctx, txn)

	// Enable batch signing mode - collect CIDs instead of signing each document
	collector := node.NewBatchCIDCollector()
	ctx = node.ContextWithBatchSigning(ctx, collector)

	colBlock, err := txn.GetCollectionByName(ctx, constants.CollectionBlock)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get block collection", err)
	}
	colTx, err := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get tx collection", err)
	}
	colLog, err := txn.GetCollectionByName(ctx, constants.CollectionLog)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get log collection", err)
	}
	colALE, err := txn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get ALE collection", err)
	}
	colBatchSig, err := txn.GetCollectionByName(ctx, constants.CollectionBatchSignature)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get batch signature collection", err)
	}

	// Build block document
	blockDoc, err := h.buildBlockDocument(ctx, block, blockInt, colBlock)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build block document", err)
	}
	blockID := blockDoc.ID().String()

	// Create block first (it's now part of the signed content, not just envelope)
	if err := colBlock.Create(ctx, blockDoc); err != nil {
		txn.Discard()
		errMsg := err.Error()
		if strings.Contains(errMsg, "already exists") {
			return "", fmt.Errorf("block already exists")
		}
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", errMsg, err)
	}

	// Create transactions (they reference the block by its deterministic ID)
	txHashToID := make(map[string]string)
	if len(transactions) > 0 {
		txDocs := make([]*client.Document, 0, len(transactions))
		for _, tx := range transactions {
			if tx == nil {
				continue
			}
			txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, colTx)
			if err != nil {
				txn.Discard()
				return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build tx document", err)
			}
			txDocs = append(txDocs, txDoc)
			txHashToID[tx.Hash] = txDoc.ID().String()
		}

		if len(txDocs) > 0 {
			if err := colTx.CreateMany(ctx, txDocs); err != nil {
				txn.Discard()
				return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transactions", err)
			}
		}
	}

	var logDocs []*client.Document
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		receipt, ok := receiptMap[tx.Hash]
		if !ok || receipt == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range receipt.Logs {
			logDoc, err := h.buildLogDocument(ctx, &receipt.Logs[i], blockID, txID, colLog)
			if err != nil {
				txn.Discard()
				return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build log document", err)
			}
			logDocs = append(logDocs, logDoc)
		}
	}

	if len(logDocs) > 0 {
		if err := colLog.CreateMany(ctx, logDocs); err != nil {
			txn.Discard()
			return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create logs", err)
		}
	}

	var aleDocs []*client.Document
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range tx.AccessList {
			aleDoc, err := h.buildALEDocument(ctx, &tx.AccessList[i], txID, blockInt, colALE)
			if err != nil {
				txn.Discard()
				return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build ALE document", err)
			}
			aleDocs = append(aleDocs, aleDoc)
		}
	}

	if len(aleDocs) > 0 {
		if err := colALE.CreateMany(ctx, aleDocs); err != nil {
			txn.Discard()
			return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create ALEs", err)
		}
	}

	// Sign the batch of CIDs collected during document creation
	// The block is now included in the merkle tree (created above)
	collectedCIDs := collector.GetCIDs()
	expectedDocs := 1 + len(transactions) + len(logDocs) + len(aleDocs)

	var batchSigDocID string
	batchSig, err := node.SignBatch(ctx, collector)
	if err != nil {
		logger.Sugar.Warnf("Failed to create batch signature for block %d: %v", blockInt, err)
	} else if batchSig != nil {
		valid, verifyErr := node.VerifyBatchSignature(batchSig, collectedCIDs)
		if verifyErr != nil {
			logger.Sugar.Warnf("Block %d: batch signature verification error: %v", blockInt, verifyErr)
		} else if !valid {
			logger.Sugar.Warnf("Block %d: batch signature verification FAILED", blockInt)
		}

		// Create a separate BatchSignature document (not embedded in block)
		batchSigDoc, err := h.buildBatchSignatureDocument(ctx, batchSig, block.Hash, blockInt, colBatchSig)
		if err != nil {
			logger.Sugar.Warnf("Block %d: failed to build batch signature document: %v", blockInt, err)
		} else {
			if err := colBatchSig.Create(ctx, batchSigDoc); err != nil {
				logger.Sugar.Warnf("Block %d: failed to create batch signature document: %v", blockInt, err)
			} else {
				batchSigDocID = batchSigDoc.ID().String()
				logger.Sugar.Debugf("Block %d: batch sig created, %d CIDs (expected ~%d), merkle: %x, verified: %v",
					blockInt, batchSig.CIDCount, expectedDocs, batchSig.MerkleRoot[:8], valid)
			}
		}
	}

	// Commit everything at once (block, txs, logs, ALEs, and BatchSignature)
	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to commit", err)
	}

	// Track docIDs for pruning
	if h.docIDTracker != nil {
		txIDs := make([]string, 0, len(txHashToID))
		for _, id := range txHashToID {
			txIDs = append(txIDs, id)
		}
		logIDs := make([]string, 0, len(logDocs))
		for _, doc := range logDocs {
			logIDs = append(logIDs, doc.ID().String())
		}
		aleIDs := make([]string, 0, len(aleDocs))
		for _, doc := range aleDocs {
			aleIDs = append(aleIDs, doc.ID().String())
		}

		result := &BlockCreationResult{
			BlockID:          blockID,
			BlockNumber:      blockInt,
			TransactionIDs:   txIDs,
			LogIDs:           logIDs,
			AccessListIDs:    aleIDs,
			BatchSignatureID: batchSigDocID,
		}

		if err := h.docIDTracker.TrackBlock(ctx, blockInt, result); err != nil {
			logger.Sugar.Warnf("Failed to track docIDs for block %d: %v", blockInt, err)
		}
	}

	return blockID, nil
}

// buildBlockDocument creates a client.Document for a block
func (h *BlockHandler) buildBlockDocument(ctx context.Context, block *types.Block, blockInt int64, col client.Collection) (*client.Document, error) {
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
	txBlockNum, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
	data := map[string]any{
		"hash":                 tx.Hash,
		"blockNumber":          txBlockNum,
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
		"_blockID":             blockID,
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildLogDocument creates a client.Document for a log
func (h *BlockHandler) buildLogDocument(ctx context.Context, log *types.Log, blockID, txID string, col client.Collection) (*client.Document, error) {
	logBlockNum, _ := utils.HexToInt(log.BlockNumber)
	data := map[string]any{
		"address":          log.Address,
		"topics":           log.Topics,
		"data":             log.Data,
		"blockNumber":      logBlockNum,
		"transactionHash":  log.TransactionHash,
		"transactionIndex": log.TransactionIndex,
		"blockHash":        log.BlockHash,
		"logIndex":         log.LogIndex,
		"removed":          fmt.Sprintf("%v", log.Removed),
		"_transactionID":   txID,
		"_blockID":         blockID,
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildALEDocument creates a client.Document for an access list entry
func (h *BlockHandler) buildALEDocument(ctx context.Context, ale *types.AccessListEntry, txID string, blockNumber int64, col client.Collection) (*client.Document, error) {
	data := map[string]any{
		"address":        ale.Address,
		"blockNumber":    blockNumber,
		"storageKeys":    ale.StorageKeys,
		"_transactionID": txID,
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildBatchSignatureDocument creates a client.Document for a batch signature
func (h *BlockHandler) buildBatchSignatureDocument(ctx context.Context, batchSig *node.BatchSignature, blockHash string, blockNumber int64, col client.Collection) (*client.Document, error) {
	data := map[string]any{
		"blockNumber":       blockNumber,
		"blockHash":         blockHash,
		"merkleRoot":        hex.EncodeToString(batchSig.MerkleRoot),
		"cidCount":          batchSig.CIDCount,
		"signatureType":     batchSig.Header.Type,
		"signatureIdentity": string(batchSig.Header.Identity),
		"signatureValue":    hex.EncodeToString(batchSig.Value),
		"createdAt":         time.Now().UTC().Format(time.RFC3339),
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// createBlockBatched creates the block using multiple transactions for large blocks.
// This is the fallback for blocks exceeding MaxDocsPerTransaction.
func (h *BlockHandler) createBlockBatched(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	// Enable batch signing mode for the entire block
	collector := node.NewBatchCIDCollector()
	ctx = node.ContextWithBatchSigning(ctx, collector)

	// First batch: Create the block document
	txn, err := h.defraNode.DB.NewBlindWriteTxn()
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to create transaction", err)
	}

	ctx = h.defraNode.DB.InitContext(ctx, txn)

	colBlock, err := txn.GetCollectionByName(ctx, constants.CollectionBlock)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to get block collection", err)
	}

	// Build and create block document first (it's now part of the signed content)
	blockDoc, err := h.buildBlockDocument(ctx, block, blockInt, colBlock)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to build block document", err)
	}
	blockID := blockDoc.ID().String()

	if err := colBlock.Create(ctx, blockDoc); err != nil {
		txn.Discard()
		errMsg := err.Error()
		if strings.Contains(errMsg, "already exists") {
			return "", fmt.Errorf("block already exists")
		}
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to create block", err)
	}

	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to commit block", err)
	}

	batchSize := h.maxDocsPerTxn
	txHashToID := make(map[string]string)

	var allTxIDs []string
	var allLogIDs []string
	var allALEIDs []string
	var batchSigDocID string

	for i := 0; i < len(transactions); i += batchSize {
		end := min(i+batchSize, len(transactions))

		batch := transactions[i:end]
		if len(batch) == 0 {
			continue
		}

		txn, err = h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			logger.Sugar.Warnf("Failed to create txn for tx batch: %v", err)
			continue
		}
		ctx = h.defraNode.DB.InitContext(ctx, txn)

		colTx, err := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
		if err != nil {
			txn.Discard()
			logger.Sugar.Warnf("Failed to get tx collection: %v", err)
			continue
		}

		txDocs := make([]*client.Document, 0, len(batch))
		for _, tx := range batch {
			if tx == nil {
				continue
			}
			txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, colTx)
			if err != nil {
				logger.Sugar.Warnf("Failed to build tx document: %v", err)
				continue
			}
			txDocs = append(txDocs, txDoc)
			txID := txDoc.ID().String()
			txHashToID[tx.Hash] = txID
			allTxIDs = append(allTxIDs, txID)
		}

		if len(txDocs) > 0 {
			if err := colTx.CreateMany(ctx, txDocs); err != nil {
				txn.Discard()
				logger.Sugar.Warnf("Failed to create tx batch: %v", err)
				continue
			}
		}

		if err := txn.Commit(); err != nil {
			logger.Sugar.Warnf("Failed to commit tx batch: %v", err)
			continue
		}
	}

	var allLogs []logEntry
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		receipt, ok := receiptMap[tx.Hash]
		if !ok || receipt == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range receipt.Logs {
			allLogs = append(allLogs, logEntry{log: &receipt.Logs[i], txID: txID})
		}
	}

	for i := 0; i < len(allLogs); i += batchSize {
		end := min(i+batchSize, len(allLogs))

		batch := allLogs[i:end]
		if len(batch) == 0 {
			continue
		}

		txn, err = h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			logger.Sugar.Warnf("Failed to create txn for log batch: %v", err)
			continue
		}
		ctx = h.defraNode.DB.InitContext(ctx, txn)

		colLog, err := txn.GetCollectionByName(ctx, constants.CollectionLog)
		if err != nil {
			txn.Discard()
			logger.Sugar.Warnf("Failed to get log collection: %v", err)
			continue
		}

		logDocs := make([]*client.Document, 0, len(batch))
		for _, entry := range batch {
			if entry.log == nil {
				continue
			}
			logDoc, err := h.buildLogDocument(ctx, entry.log, blockID, entry.txID, colLog)
			if err != nil {
				logger.Sugar.Warnf("Failed to build log document: %v", err)
				continue
			}
			logDocs = append(logDocs, logDoc)
			allLogIDs = append(allLogIDs, logDoc.ID().String())
		}

		if len(logDocs) > 0 {
			if err := colLog.CreateMany(ctx, logDocs); err != nil {
				txn.Discard()
				logger.Sugar.Warnf("Failed to create log batch: %v", err)
				continue
			}
		}

		if err := txn.Commit(); err != nil {
			logger.Sugar.Warnf("Failed to commit log batch: %v", err)
			continue
		}
	}

	var allALEs []aleEntry
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range tx.AccessList {
			allALEs = append(allALEs, aleEntry{ale: &tx.AccessList[i], txID: txID, blockNumber: blockInt})
		}
	}

	totalALEBatches := (len(allALEs) + batchSize - 1) / batchSize
	if totalALEBatches == 0 {
		totalALEBatches = 1
	}

	for i := 0; i < len(allALEs) || i == 0; i += batchSize {
		end := min(i+batchSize, len(allALEs))
		isLastBatch := end >= len(allALEs)

		batch := allALEs[i:end]

		txn, err = h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			logger.Sugar.Warnf("Failed to create txn for ALE batch: %v", err)
			continue
		}
		ctx = h.defraNode.DB.InitContext(ctx, txn)

		if len(batch) > 0 {
			colALE, err := txn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)
			if err != nil {
				txn.Discard()
				logger.Sugar.Warnf("Failed to get ALE collection: %v", err)
				continue
			}

			aleDocs := make([]*client.Document, 0, len(batch))
			for _, entry := range batch {
				if entry.ale == nil {
					continue
				}
				aleDoc, err := h.buildALEDocument(ctx, entry.ale, entry.txID, entry.blockNumber, colALE)
				if err != nil {
					logger.Sugar.Warnf("Failed to build ALE document: %v", err)
					continue
				}
				aleDocs = append(aleDocs, aleDoc)
				allALEIDs = append(allALEIDs, aleDoc.ID().String())
			}

			if len(aleDocs) > 0 {
				if err := colALE.CreateMany(ctx, aleDocs); err != nil {
					txn.Discard()
					logger.Sugar.Warnf("Failed to create ALE batch: %v", err)
					continue
				}
			}
		}

		// On the last batch, create the BatchSignature document
		if isLastBatch {
			collectedCIDs := collector.GetCIDs()

			batchSig, err := node.SignBatch(ctx, collector)
			if err != nil {
				logger.Sugar.Warnf("Failed to create batch signature for block %d: %v", blockInt, err)
			} else if batchSig != nil {
				valid, verifyErr := node.VerifyBatchSignature(batchSig, collectedCIDs)
				if verifyErr != nil {
					logger.Sugar.Warnf("Block %d: batch signature verification error: %v", blockInt, verifyErr)
				} else if !valid {
					logger.Sugar.Warnf("Block %d: batch signature verification FAILED", blockInt)
				}

				colBatchSig, err := txn.GetCollectionByName(ctx, constants.CollectionBatchSignature)
				if err != nil {
					logger.Sugar.Warnf("Block %d: failed to get batch signature collection: %v", blockInt, err)
				} else {
					batchSigDoc, err := h.buildBatchSignatureDocument(ctx, batchSig, block.Hash, blockInt, colBatchSig)
					if err != nil {
						logger.Sugar.Warnf("Block %d: failed to build batch signature document: %v", blockInt, err)
					} else {
						if err := colBatchSig.Create(ctx, batchSigDoc); err != nil {
							logger.Sugar.Warnf("Block %d: failed to create batch signature document: %v", blockInt, err)
						} else {
							batchSigDocID = batchSigDoc.ID().String()
							logger.Sugar.Debugf("Block %d (batched): batch sig created, %d CIDs, merkle: %x, verified: %v",
								blockInt, batchSig.CIDCount, batchSig.MerkleRoot[:8], valid)
						}
					}
				}
			}
		}

		if err := txn.Commit(); err != nil {
			logger.Sugar.Warnf("Failed to commit ALE batch: %v", err)
			continue
		}

		if isLastBatch {
			break
		}
	}

	// Track docIDs for pruning
	if h.docIDTracker != nil {
		result := &BlockCreationResult{
			BlockID:          blockID,
			BlockNumber:      blockInt,
			TransactionIDs:   allTxIDs,
			LogIDs:           allLogIDs,
			AccessListIDs:    allALEIDs,
			BatchSignatureID: batchSigDocID,
		}

		if err := h.docIDTracker.TrackBlock(ctx, blockInt, result); err != nil {
			logger.Sugar.Warnf("Failed to track docIDs for block %d: %v", blockInt, err)
		}
	}

	return blockID, nil
}

// GetHighestBlockNumber returns the highest block number stored in DefraDB
func (h *BlockHandler) GetHighestBlockNumber(ctx context.Context) (int64, error) {
	query := `query {` + constants.CollectionBlock + ` (order: {number: DESC}, limit: 1) { number }}`

	result := h.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, errors.NewQueryFailed("defra", "GetHighestBlockNumber", query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]interface{})
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "no data")
	}

	blockArray, ok := data[constants.CollectionBlock].([]interface{})
	if !ok || len(blockArray) == 0 {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "no blocks")
	}

	block, ok := blockArray[0].(map[string]interface{})
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "invalid format")
	}

	switch v := block["number"].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	}

	return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "invalid number type")
}
