package defra

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/node"
)

// retryBackoff returns an exponential backoff duration capped at 8 seconds.
func retryBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond
	for range attempt {
		d *= 2
	}
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

// BlockCreationResult holds the result of creating a block, including all docIDs
type BlockCreationResult struct {
	BlockID          string
	BlockNumber      int64
	TransactionIDs   []string
	LogIDs           []string
	AccessListIDs    []string
	BlockSignatureID string
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
// Block and BlockSignature are created as separate documents in the same transaction.
// This ensures all documents arrive via P2P together, and the host can listen for
// BlockSignature events to create attestations.
func (h *BlockHandler) createBlockSingleTransaction(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	txn, err := h.defraNode.DB.NewBlindWriteTxn()
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transaction", err)
	}
	ctx = h.defraNode.DB.InitContext(ctx, txn)

	// Enable block signing mode - collect CIDs instead of signing each document
	collector := node.NewBlockCIDCollector()
	ctx = node.ContextWithBlockSigning(ctx, collector)

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
	colBlockSig, err := txn.GetCollectionByName(ctx, constants.CollectionBlockSignature)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get block signature collection", err)
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
		if errors.IsErrAlreadyExists(err) {
			return "", fmt.Errorf("block already exists")
		}
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", err.Error(), err)
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

	// Sign the block's CIDs collected during document creation
	// The block is now included in the merkle tree (created above)
	collectedCIDs := collector.GetCIDs()
	expectedDocs := 1 + len(transactions) + len(logDocs) + len(aleDocs)

	var blockSigDocID string
	blockSig, err := node.SignBlock(ctx, collector)
	if err != nil {
		logger.Sugar.Warnf("Failed to create block signature for block %d: %v", blockInt, err)
	} else if blockSig != nil {
		valid, verifyErr := node.VerifyBlockSignatureCIDs(blockSig, collectedCIDs)
		if verifyErr != nil {
			logger.Sugar.Warnf("Block %d: block signature verification error: %v", blockInt, verifyErr)
		} else if !valid {
			logger.Sugar.Warnf("Block %d: block signature verification FAILED", blockInt)
		}

		// Create a separate BlockSignature document (not embedded in block)
		sortedCIDs := node.SortedCIDStrings(collectedCIDs)
		blockSigDoc, err := h.buildBlockSignatureDocument(ctx, blockSig, block.Hash, blockInt, colBlockSig, sortedCIDs)
		if err != nil {
			logger.Sugar.Warnf("Block %d: failed to build block signature document: %v", blockInt, err)
		} else {
			if err := colBlockSig.Create(ctx, blockSigDoc); err != nil {
				logger.Sugar.Warnf("Block %d: failed to create block signature document: %v", blockInt, err)
			} else {
				blockSigDocID = blockSigDoc.ID().String()
				logger.Sugar.Debugf("Block %d: block sig created, %d CIDs (expected ~%d), merkle: %x, verified: %v",
					blockInt, blockSig.CIDCount, expectedDocs, blockSig.MerkleRoot[:8], valid)
			}
		}
	}

	// Commit everything at once (block, txs, logs, ALEs, and BlockSignature)
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
			BlockSignatureID: blockSigDocID,
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

// buildBlockSignatureDocument creates a client.Document for a block signature
func (h *BlockHandler) buildBlockSignatureDocument(ctx context.Context, blockSig *node.BlockSignature, blockHash string, blockNumber int64, col client.Collection, sortedCIDStrings []string) (*client.Document, error) {
	data := map[string]any{
		"blockNumber":       blockNumber,
		"blockHash":         blockHash,
		"merkleRoot":        hex.EncodeToString(blockSig.MerkleRoot),
		"cidCount":          blockSig.CIDCount,
		"cids":              sortedCIDStrings,
		"signatureType":     blockSig.Header.Type,
		"signatureIdentity": string(blockSig.Header.Identity),
		"signatureValue":    hex.EncodeToString(blockSig.Value),
		"createdAt":         time.Now().UTC().Format(time.RFC3339),
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// CreateBlockSignatureForExistingBlock creates a BlockSignature for a block that already
// exists in DefraDB (received via P2P from another indexer).
func (h *BlockHandler) CreateBlockSignatureForExistingBlock(
	ctx context.Context,
	blockNumber int64,
	blockHash string,
	block *types.Block,
	transactions []*types.Transaction,
	receipts []*types.TransactionReceipt,
) (string, error) {
	if h.defraNode == nil {
		return "", fmt.Errorf("defraNode is nil")
	}

	// Build all documents in memory to compute deterministic docIDs.
	// We need collection versions, so use a temporary transaction.
	tmpTxn, err := h.defraNode.DB.NewBlindWriteTxn()
	if err != nil {
		return "", fmt.Errorf("failed to create transaction: %w", err)
	}
	tmpCtx := h.defraNode.DB.InitContext(ctx, tmpTxn)

	colBlock, err := tmpTxn.GetCollectionByName(tmpCtx, constants.CollectionBlock)
	if err != nil {
		tmpTxn.Discard()
		return "", fmt.Errorf("failed to get block collection: %w", err)
	}
	colTx, err := tmpTxn.GetCollectionByName(tmpCtx, constants.CollectionTransaction)
	if err != nil {
		tmpTxn.Discard()
		return "", fmt.Errorf("failed to get transaction collection: %w", err)
	}
	colLog, err := tmpTxn.GetCollectionByName(tmpCtx, constants.CollectionLog)
	if err != nil {
		tmpTxn.Discard()
		return "", fmt.Errorf("failed to get log collection: %w", err)
	}
	colALE, err := tmpTxn.GetCollectionByName(tmpCtx, constants.CollectionAccessListEntry)
	if err != nil {
		tmpTxn.Discard()
		return "", fmt.Errorf("failed to get ALE collection: %w", err)
	}

	// Build block document to get its deterministic docID
	blockDoc, err := h.buildBlockDocument(tmpCtx, block, blockNumber, colBlock)
	if err != nil {
		tmpTxn.Discard()
		return "", fmt.Errorf("failed to build block document: %w", err)
	}
	blockID := blockDoc.ID().String()
	allDocIDs := []string{blockID}

	// Build receipt map for log lookup
	receiptMap := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	// Build transaction documents
	txHashToID := make(map[string]string)
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocument(tmpCtx, tx, blockID, colTx)
		if err != nil {
			continue
		}
		txID := txDoc.ID().String()
		txHashToID[tx.Hash] = txID
		allDocIDs = append(allDocIDs, txID)
	}

	// Build log documents
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
			logDoc, err := h.buildLogDocument(tmpCtx, &receipt.Logs[i], blockID, txID, colLog)
			if err != nil {
				continue
			}
			allDocIDs = append(allDocIDs, logDoc.ID().String())
		}
	}

	// Build ALE documents
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range tx.AccessList {
			aleDoc, err := h.buildALEDocument(tmpCtx, &tx.AccessList[i], txID, blockNumber, colALE)
			if err != nil {
				continue
			}
			allDocIDs = append(allDocIDs, aleDoc.ID().String())
		}
	}

	tmpTxn.Discard()

	// Collect CIDs from headstore with retry (P2P data may still be arriving)
	const maxRetries = 15
	var lastCIDCount int
	var lastErr error

	for attempt := range maxRetries {
		cidTxn, err := h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			lastErr = err
			if attempt < maxRetries-1 {
				time.Sleep(retryBackoff(attempt))
			}
			continue
		}
		cidCtx := h.defraNode.DB.InitContext(ctx, cidTxn)
		cids, err := node.CollectDocumentCIDs(cidCtx, allDocIDs)
		cidTxn.Discard()

		if err != nil {
			lastErr = err
			if attempt < maxRetries-1 {
				time.Sleep(retryBackoff(attempt))
			}
			continue
		}

		lastCIDCount = len(cids)
		if len(cids) >= len(allDocIDs) {
			break // Got at least one CID per document
		}

		lastErr = fmt.Errorf("got %d CIDs for %d docs", len(cids), len(allDocIDs))
		if attempt < maxRetries-1 {
			logger.Sugar.Debugf("Block %d: waiting for P2P data (%d/%d CIDs, attempt %d/%d)",
				blockNumber, len(cids), len(allDocIDs), attempt+1, maxRetries)
			time.Sleep(retryBackoff(attempt))
		}
	}

	if lastCIDCount == 0 {
		return "", fmt.Errorf("no CIDs found for block %d after %d retries (%d docs): %w",
			blockNumber, maxRetries, len(allDocIDs), lastErr)
	}

	// Final CID collection + signing in one transaction
	sigTxn, err := h.defraNode.DB.NewBlindWriteTxn()
	if err != nil {
		return "", fmt.Errorf("failed to create signing transaction: %w", err)
	}
	sigCtx := h.defraNode.DB.InitContext(ctx, sigTxn)

	cids, err := node.CollectDocumentCIDs(sigCtx, allDocIDs)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to collect CIDs for signing: %w", err)
	}

	collector := node.NewBlockCIDCollector()
	for _, c := range cids {
		collector.Add(c)
	}

	blockSig, err := node.SignBlock(sigCtx, collector)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to sign block: %w", err)
	}
	if blockSig == nil {
		sigTxn.Discard()
		return "", fmt.Errorf("signing returned nil (no identity?)")
	}

	// Create the BlockSignature document and commit
	colBlockSig, err := sigTxn.GetCollectionByName(sigCtx, constants.CollectionBlockSignature)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to get block signature collection: %w", err)
	}

	sortedCIDs := node.SortedCIDStrings(cids)
	blockSigDoc, err := h.buildBlockSignatureDocument(sigCtx, blockSig, blockHash, blockNumber, colBlockSig, sortedCIDs)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to build block signature document: %w", err)
	}

	if err := colBlockSig.Create(sigCtx, blockSigDoc); err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to create block signature document: %w", err)
	}

	if err := sigTxn.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit block signature: %w", err)
	}

	docID := blockSigDoc.ID().String()
	logger.Sugar.Infof("Block %d: block sig for existing block (%d docs, %d CIDs, identity: %s...)",
		blockNumber, len(allDocIDs), len(cids), truncate(string(blockSig.Header.Identity), 16))

	return docID, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// createBlockBatched creates the block using multiple transactions for large blocks.
// This is the fallback for blocks exceeding MaxDocsPerTransaction.
func (h *BlockHandler) createBlockBatched(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	// Enable block signing mode for the entire block
	collector := node.NewBlockCIDCollector()
	ctx = node.ContextWithBlockSigning(ctx, collector)

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
		if errors.IsErrAlreadyExists(err) {
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
	var blockSigDocID string
	var batchErrors []error // track non-"already exists" batch failures

	for i := 0; i < len(transactions); i += batchSize {
		end := min(i+batchSize, len(transactions))

		batch := transactions[i:end]
		if len(batch) == 0 {
			continue
		}

		txn, err = h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("create txn for tx batch: %w", err))
			continue
		}
		ctx = h.defraNode.DB.InitContext(ctx, txn)

		colTx, err := txn.GetCollectionByName(ctx, constants.CollectionTransaction)
		if err != nil {
			txn.Discard()
			batchErrors = append(batchErrors, fmt.Errorf("get tx collection: %w", err))
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
				if errors.IsErrAlreadyExists(err) {
					logger.Sugar.Debugf("Block %d: tx batch already exists via P2P, skipping", blockInt)
				} else {
					batchErrors = append(batchErrors, fmt.Errorf("create tx batch: %w", err))
				}
				continue
			}
		}

		if err := txn.Commit(); err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("commit tx batch: %w", err))
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
			batchErrors = append(batchErrors, fmt.Errorf("create txn for log batch: %w", err))
			continue
		}
		ctx = h.defraNode.DB.InitContext(ctx, txn)

		colLog, err := txn.GetCollectionByName(ctx, constants.CollectionLog)
		if err != nil {
			txn.Discard()
			batchErrors = append(batchErrors, fmt.Errorf("get log collection: %w", err))
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
				if errors.IsErrAlreadyExists(err) {
					logger.Sugar.Debugf("Block %d: log batch already exists via P2P, skipping", blockInt)
				} else {
					batchErrors = append(batchErrors, fmt.Errorf("create log batch: %w", err))
				}
				continue
			}
		}

		if err := txn.Commit(); err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("commit log batch: %w", err))
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

	for i := 0; i < len(allALEs); i += batchSize {
		end := min(i+batchSize, len(allALEs))

		batch := allALEs[i:end]

		txn, err = h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("create txn for ALE batch: %w", err))
			continue
		}
		ctx = h.defraNode.DB.InitContext(ctx, txn)

		colALE, err := txn.GetCollectionByName(ctx, constants.CollectionAccessListEntry)
		if err != nil {
			txn.Discard()
			batchErrors = append(batchErrors, fmt.Errorf("get ALE collection: %w", err))
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
				if errors.IsErrAlreadyExists(err) {
					logger.Sugar.Debugf("Block %d: ALE batch already exists via P2P, skipping", blockInt)
				} else {
					batchErrors = append(batchErrors, fmt.Errorf("create ALE batch: %w", err))
				}
				continue
			}
		}

		if err := txn.Commit(); err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("commit ALE batch: %w", err))
			continue
		}
	}

	// Create BlockSignature in its own transaction (not bundled with ALE batches).
	// This ensures it's always created even if ALE batches fail with "already exists".
	{
		collectedCIDs := collector.GetCIDs()

		sigTxn, err := h.defraNode.DB.NewBlindWriteTxn()
		if err != nil {
			logger.Sugar.Warnf("Block %d: failed to create txn for block signature: %v", blockInt, err)
		} else {
			sigCtx := h.defraNode.DB.InitContext(ctx, sigTxn)

			blockSig, err := node.SignBlock(sigCtx, collector)
			if err != nil {
				sigTxn.Discard()
				logger.Sugar.Warnf("Failed to create block signature for block %d: %v", blockInt, err)
			} else if blockSig != nil {
				valid, verifyErr := node.VerifyBlockSignatureCIDs(blockSig, collectedCIDs)
				if verifyErr != nil {
					logger.Sugar.Warnf("Block %d: block signature verification error: %v", blockInt, verifyErr)
				} else if !valid {
					logger.Sugar.Warnf("Block %d: block signature verification FAILED", blockInt)
				}

				colBlockSig, err := sigTxn.GetCollectionByName(sigCtx, constants.CollectionBlockSignature)
				if err != nil {
					sigTxn.Discard()
					logger.Sugar.Warnf("Block %d: failed to get block signature collection: %v", blockInt, err)
				} else {
					sortedCIDs := node.SortedCIDStrings(collectedCIDs)
					blockSigDoc, err := h.buildBlockSignatureDocument(sigCtx, blockSig, block.Hash, blockInt, colBlockSig, sortedCIDs)
					if err != nil {
						sigTxn.Discard()
						logger.Sugar.Warnf("Block %d: failed to build block signature document: %v", blockInt, err)
					} else if err := colBlockSig.Create(sigCtx, blockSigDoc); err != nil {
						sigTxn.Discard()
						logger.Sugar.Warnf("Block %d: failed to create block signature document: %v", blockInt, err)
					} else if err := sigTxn.Commit(); err != nil {
						logger.Sugar.Warnf("Block %d: failed to commit block signature: %v", blockInt, err)
					} else {
						blockSigDocID = blockSigDoc.ID().String()
						logger.Sugar.Debugf("Block %d (batched): block sig created, %d CIDs, merkle: %x, verified: %v",
							blockInt, blockSig.CIDCount, blockSig.MerkleRoot[:8], valid)
					}
				}
			} else {
				sigTxn.Discard()
			}
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
			BlockSignatureID: blockSigDocID,
		}

		if err := h.docIDTracker.TrackBlock(ctx, blockInt, result); err != nil {
			logger.Sugar.Warnf("Failed to track docIDs for block %d: %v", blockInt, err)
		}
	}

	if len(batchErrors) > 0 {
		return blockID, fmt.Errorf("block %d partially indexed with %d batch errors (first: %w)", blockInt, len(batchErrors), batchErrors[0])
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

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "no data")
	}

	blockArray, ok := data[constants.CollectionBlock].([]any)
	if !ok || len(blockArray) == 0 {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "no blocks")
	}

	block, ok := blockArray[0].(map[string]any)
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
