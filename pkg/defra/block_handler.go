package defra

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

// blockDB abstracts the DB operations used by BlockHandler for testability.
type blockDB interface {
	NewBlindWriteTxn() (client.Txn, error)
	InitContext(ctx context.Context, txn client.Txn) context.Context
	ExecRequest(ctx context.Context, request string, opts ...options.Enumerable[options.ExecRequestOptions]) *client.RequestResult
}

// retryBackoff returns an exponential backoff duration capped at 8 seconds.
func retryBackoff(attempt int) time.Duration {
	d := 500 * time.Millisecond //nolint:mnd
	for range attempt {
		d *= 2 //nolint:mnd
	}
	if d > 8*time.Second { //nolint:mnd
		d = 8 * time.Second //nolint:mnd
	}
	return d
}

// BlockCreationResult holds the result of creating a block, including all docIDs.
type BlockCreationResult struct {
	BlockID          string
	BlockNumber      int64
	TransactionIDs   []string
	LogIDs           []string
	AccessListIDs    []string
	BlockSignatureID string
}

// DocIDTrackerInterface defines the interface for tracking docIDs.
type DocIDTrackerInterface interface {
	TrackBlock(ctx context.Context, blockNumber int64, result *BlockCreationResult) error
}

// BlockHandler manages the creation and storage of blocks, transactions, and logs in DefraDB.
type BlockHandler struct {
	db            blockDB                    // DB interface (from defraNode.DB).
	maxDocsPerTxn int                        // Threshold for single-txn vs batched block creation.
	docIDTracker  DocIDTrackerInterface      // Optional tracker for docIDs.
	collections   *constants.CollectionNames // Chain-specific collection names.

	// Injectable functions for testability (set to defaults in NewBlockHandler).
	signBlockFn      func(ctx context.Context, collector *node.BlockCIDCollector) (*node.BlockSignature, error)
	verifyBlockSigFn func(sig *node.BlockSignature, cids []cid.Cid) (bool, error)
	collectDocCIDsFn func(ctx context.Context, docIDs []string) ([]cid.Cid, error)
	maxCIDRetries    int
	retryBackoffFn   func(int) time.Duration

	// // Document throughput metrics
	// metricsWindowStart  time.Time
	// docsCreatedInWindow int
}

// logEntry holds a log and its associated transaction ID for batched processing.
type logEntry struct {
	log  *types.Log
	txID string
}

// aleEntry holds an access list entry and its associated transaction ID for batched processing.
type aleEntry struct {
	ale         *types.AccessListEntry
	txID        string
	blockNumber int64
}

// NewBlockHandler creates a BlockHandler that uses direct DB calls.
// maxDocsPerTxn is the threshold for single-txn vs batched block creation.
func NewBlockHandler(defraNode *node.Node, maxDocsPerTxn int, collections *constants.CollectionNames) (*BlockHandler, error) {
	if defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "NewBlockHandler",
			"defraNode is nil", "", nil)
	}
	if maxDocsPerTxn <= 0 {
		maxDocsPerTxn = 1000 //nolint:mnd
	}
	if collections == nil {
		collections = constants.NewCollectionNames(constants.DefaultCollectionPrefix)
	}
	return &BlockHandler{
		db:               defraNode.DB,
		maxDocsPerTxn:    maxDocsPerTxn,
		collections:      collections,
		signBlockFn:      node.SignBlock,
		verifyBlockSigFn: node.VerifyBlockSignatureCIDs,
		collectDocCIDsFn: node.CollectDocumentCIDs,
		maxCIDRetries:    15, //nolint:mnd
		retryBackoffFn:   retryBackoff,
	}, nil
}

// SetDocIDTracker sets the tracker for recording docIDs at insert time.
func (h *BlockHandler) SetDocIDTracker(tracker DocIDTrackerInterface) {
	h.docIDTracker = tracker
}

// CreateBlockBatch creates a block with all its transactions, logs, and access list entries.
func (h *BlockHandler) CreateBlockBatch(ctx context.Context, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) (string, error) {
	if h.db == nil {
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
// This ensures all documents arrive via P2P together, and the host can listen for.
// BlockSignature events to create attestations.
func (h *BlockHandler) createBlockSingleTransaction(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	txn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transaction", err)
	}
	ctx = h.db.InitContext(ctx, txn)

	collector := node.NewBlockCIDCollector()
	ctx = node.ContextWithBlockSigning(ctx, collector)

	cols, err := h.getSingleTxnCollections(ctx, txn)
	if err != nil {
		txn.Discard()
		return "", err
	}

	blockID, txHashToID, logDocs, aleDocs, err := h.buildAndCreateSingleTxnDocs(ctx, txn, block, blockInt, transactions, receiptMap, cols)
	if err != nil {
		txn.Discard()
		return "", err
	}

	blockSigDocID := h.buildAndCreateSingleTxnSignature(ctx, block, blockInt, collector, cols.blockSig)

	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to commit", err)
	}

	h.trackSingleTxnDocIDs(ctx, blockInt, blockID, blockSigDocID, txHashToID, logDocs, aleDocs)

	return blockID, nil
}

// singleTxnCollections holds all collections needed for a single-transaction block write.
type singleTxnCollections struct {
	block    client.Collection
	tx       client.Collection
	log      client.Collection
	ale      client.Collection
	blockSig client.Collection
}

// getSingleTxnCollections fetches all required collections within a transaction.
func (h *BlockHandler) getSingleTxnCollections(ctx context.Context, txn client.Txn) (*singleTxnCollections, error) {
	colBlock, err := txn.GetCollectionByName(ctx, h.collections.Block)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get block collection", err)
	}
	colTx, err := txn.GetCollectionByName(ctx, h.collections.Transaction)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get tx collection", err)
	}
	colLog, err := txn.GetCollectionByName(ctx, h.collections.Log)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get log collection", err)
	}
	colALE, err := txn.GetCollectionByName(ctx, h.collections.AccessListEntry)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get ALE collection", err)
	}
	colBlockSig, err := txn.GetCollectionByName(ctx, h.collections.BlockSignature)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get block signature collection", err)
	}
	return &singleTxnCollections{block: colBlock, tx: colTx, log: colLog, ale: colALE, blockSig: colBlockSig}, nil
}

// buildAndCreateSingleTxnDocs builds and creates all block documents within the transaction.
func (h *BlockHandler) buildAndCreateSingleTxnDocs(ctx context.Context, txn client.Txn, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt, cols *singleTxnCollections) (string, map[string]string, []*client.Document, []*client.Document, error) {
	blockDoc, err := h.buildBlockDocument(ctx, block, blockInt, cols.block)
	if err != nil {
		return "", nil, nil, nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build block document", err)
	}
	blockID := blockDoc.ID().String()

	if err := cols.block.Create(ctx, blockDoc); err != nil {
		if errors.IsErrAlreadyExists(err) {
			return "", nil, nil, nil, fmt.Errorf("block already exists") //nolint: err113
		}
		return "", nil, nil, nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", err.Error(), err)
	}

	txHashToID, err := h.createSingleTxnTransactions(ctx, txn, transactions, blockID, cols.tx)
	if err != nil {
		return "", nil, nil, nil, err
	}

	logDocs, err := h.createSingleTxnLogs(ctx, transactions, receiptMap, txHashToID, blockID, cols.log)
	if err != nil {
		return "", nil, nil, nil, err
	}

	aleDocs, err := h.createSingleTxnALEs(ctx, transactions, txHashToID, blockInt, cols.ale)
	if err != nil {
		return "", nil, nil, nil, err
	}

	return blockID, txHashToID, logDocs, aleDocs, nil
}

// createSingleTxnTransactions builds and creates transaction documents.
func (h *BlockHandler) createSingleTxnTransactions(ctx context.Context, _ client.Txn, transactions []*types.Transaction, blockID string, colTx client.Collection) (map[string]string, error) {
	txHashToID := make(map[string]string)
	if len(transactions) == 0 {
		return txHashToID, nil
	}

	txDocs := make([]*client.Document, 0, len(transactions))
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, colTx)
		if err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build tx document", err)
		}
		txDocs = append(txDocs, txDoc)
		txHashToID[tx.Hash] = txDoc.ID().String()
	}

	if len(txDocs) > 0 {
		if err := colTx.CreateMany(ctx, txDocs); err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transactions", err)
		}
	}

	return txHashToID, nil
}

// createSingleTxnLogs builds and creates log documents.
func (h *BlockHandler) createSingleTxnLogs(ctx context.Context, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt, txHashToID map[string]string, blockID string, colLog client.Collection) ([]*client.Document, error) {
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
				return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build log document", err)
			}
			logDocs = append(logDocs, logDoc)
		}
	}

	if len(logDocs) > 0 {
		if err := colLog.CreateMany(ctx, logDocs); err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create logs", err)
		}
	}

	return logDocs, nil
}

// createSingleTxnALEs builds and creates access list entry documents.
func (h *BlockHandler) createSingleTxnALEs(ctx context.Context, transactions []*types.Transaction, txHashToID map[string]string, blockInt int64, colALE client.Collection) ([]*client.Document, error) {
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
				return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build ALE document", err)
			}
			aleDocs = append(aleDocs, aleDoc)
		}
	}

	if len(aleDocs) > 0 {
		if err := colALE.CreateMany(ctx, aleDocs); err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create ALEs", err)
		}
	}

	return aleDocs, nil
}

// buildAndCreateSingleTxnSignature creates the block signature within the transaction.
func (h *BlockHandler) buildAndCreateSingleTxnSignature(ctx context.Context, block *types.Block, blockInt int64, collector *node.BlockCIDCollector, colBlockSig client.Collection) string {
	collectedCIDs := collector.GetCIDs()

	blockSig, err := h.signBlockFn(ctx, collector)
	if err != nil {
		logger.Sugar.Warnf("Failed to create block signature for block %d: %v", blockInt, err)
		return ""
	}
	if blockSig == nil {
		return ""
	}

	valid, verifyErr := h.verifyBlockSigFn(blockSig, collectedCIDs)
	if verifyErr != nil {
		logger.Sugar.Warnf("Block %d: block signature verification error: %v", blockInt, verifyErr)
	} else if !valid {
		logger.Sugar.Warnf("Block %d: block signature verification FAILED", blockInt)
	}

	sortedCIDs := node.SortedCIDStrings(collectedCIDs)
	blockSigDoc, err := h.buildBlockSignatureDocument(ctx, blockSig, block.Hash, blockInt, colBlockSig, sortedCIDs)
	if err != nil {
		logger.Sugar.Warnf("Block %d: failed to build block signature document: %v", blockInt, err)
		return ""
	}

	if err := colBlockSig.Create(ctx, blockSigDoc); err != nil {
		logger.Sugar.Warnf("Block %d: failed to create block signature document: %v", blockInt, err)
		return ""
	}

	blockSigDocID := blockSigDoc.ID().String()
	expectedDocs := 1 + len(collectedCIDs)
	logger.Sugar.Debugf("Block %d: block sig created, %d CIDs (expected ~%d), merkle: %x, verified: %v",
		blockInt, blockSig.CIDCount, expectedDocs, blockSig.MerkleRoot[:8], valid) //nolint:mnd

	return blockSigDocID
}

// trackSingleTxnDocIDs records all document IDs for pruning.
func (h *BlockHandler) trackSingleTxnDocIDs(ctx context.Context, blockInt int64, blockID, blockSigDocID string, txHashToID map[string]string, logDocs, aleDocs []*client.Document) {
	if h.docIDTracker == nil {
		return
	}

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

// buildBlockDocument creates a client.Document for a block.
func (h *BlockHandler) buildBlockDocument(ctx context.Context, block *types.Block, blockInt int64, col client.Collection) (*client.Document, error) {
	data := map[string]any{
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
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildTransactionDocument creates a client.Document for a transaction.
func (h *BlockHandler) buildTransactionDocument(ctx context.Context, tx *types.Transaction, blockID string, col client.Collection) (*client.Document, error) {
	txBlockNum, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
	data := map[string]any{
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
		"_blockID":                    blockID,
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildLogDocument creates a client.Document for a log.
func (h *BlockHandler) buildLogDocument(ctx context.Context, log *types.Log, blockID, txID string, col client.Collection) (*client.Document, error) {
	logBlockNum, _ := utils.HexToInt(log.BlockNumber)
	data := map[string]any{
		"address":                     log.Address,
		"topics":                      log.Topics,
		"data":                        log.Data,
		constants.BlockNumberKeyValue: logBlockNum,
		"transactionHash":             log.TransactionHash,
		"transactionIndex":            log.TransactionIndex,
		constants.BlockHashKeyValue:   log.BlockHash,
		"logIndex":                    log.LogIndex,
		"removed":                     fmt.Sprintf("%v", log.Removed),
		"_transactionID":              txID,
		"_blockID":                    blockID,
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildALEDocument creates a client.Document for an access list entry.
func (h *BlockHandler) buildALEDocument(ctx context.Context, ale *types.AccessListEntry, txID string, blockNumber int64, col client.Collection) (*client.Document, error) {
	data := map[string]any{
		"address":                     ale.Address,
		constants.BlockNumberKeyValue: blockNumber,
		"storageKeys":                 ale.StorageKeys,
		"_transactionID":              txID,
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// buildBlockSignatureDocument creates a client.Document for a block signature.
func (h *BlockHandler) buildBlockSignatureDocument(ctx context.Context, blockSig *node.BlockSignature, blockHash string, blockNumber int64, col client.Collection, sortedCIDStrings []string) (*client.Document, error) {
	data := map[string]any{
		constants.BlockNumberKeyValue: blockNumber,
		constants.BlockHashKeyValue:   blockHash,
		"merkleRoot":                  hex.EncodeToString(blockSig.MerkleRoot),
		"cidCount":                    blockSig.CIDCount,
		"cids":                        sortedCIDStrings,
		"signatureType":               blockSig.Header.Type,
		"signatureIdentity":           string(blockSig.Header.Identity),
		"signatureValue":              hex.EncodeToString(blockSig.Value),
		"createdAt":                   time.Now().UTC().Format(time.RFC3339),
	}
	return client.NewDocFromMap(ctx, data, col.Version())
}

// CreateBlockSignatureForExistingBlock creates a block signature for a block that already exists.
func (h *BlockHandler) CreateBlockSignatureForExistingBlock(
	ctx context.Context,
	blockNumber int64,
	blockHash string,
	block *types.Block,
	transactions []*types.Transaction,
	receipts []*types.TransactionReceipt,
) (string, error) {
	if h.db == nil {
		return "", fmt.Errorf("defraNode is nil") //nolint: err113
	}

	allDocIDs, err := h.collectExistingBlockDocIDs(ctx, blockNumber, block, transactions, receipts)
	if err != nil {
		return "", err
	}

	if err := h.waitForCIDs(ctx, blockNumber, allDocIDs); err != nil {
		return "", err
	}

	return h.signAndStoreExistingBlockSignature(ctx, blockNumber, blockHash, allDocIDs)
}

// collectExistingBlockDocIDs builds all documents in memory to compute their deterministic IDs.
func (h *BlockHandler) collectExistingBlockDocIDs(ctx context.Context, blockNumber int64, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) ([]string, error) {
	tmpTxn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err) //nolint: err113
	}
	tmpCtx := h.db.InitContext(ctx, tmpTxn)
	defer tmpTxn.Discard()

	cols, err := h.getExistingBlockCollections(tmpCtx, tmpTxn)
	if err != nil {
		return nil, err
	}

	blockDoc, err := h.buildBlockDocument(tmpCtx, block, blockNumber, cols.block)
	if err != nil {
		return nil, fmt.Errorf("failed to build block document: %w", err) //nolint: err113
	}
	blockID := blockDoc.ID().String()
	allDocIDs := []string{blockID}

	receiptMap := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	txHashToID := h.collectTxDocIDs(tmpCtx, transactions, blockID, cols.tx, &allDocIDs)
	h.collectLogDocIDs(tmpCtx, transactions, receiptMap, txHashToID, blockID, cols.log, &allDocIDs)
	h.collectALEDocIDs(tmpCtx, transactions, txHashToID, blockNumber, cols.ale, &allDocIDs)

	return allDocIDs, nil
}

// existingBlockCollections holds collections needed for existing block doc ID collection.
type existingBlockCollections struct {
	block client.Collection
	tx    client.Collection
	log   client.Collection
	ale   client.Collection
}

// getExistingBlockCollections fetches all collections needed for doc ID computation.
func (h *BlockHandler) getExistingBlockCollections(ctx context.Context, txn client.Txn) (*existingBlockCollections, error) {
	colBlock, err := txn.GetCollectionByName(ctx, h.collections.Block)
	if err != nil {
		return nil, fmt.Errorf("failed to get block collection: %w", err) //nolint: err113
	}
	colTx, err := txn.GetCollectionByName(ctx, h.collections.Transaction)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction collection: %w", err) //nolint: err113
	}
	colLog, err := txn.GetCollectionByName(ctx, h.collections.Log)
	if err != nil {
		return nil, fmt.Errorf("failed to get log collection: %w", err) //nolint: err113
	}
	colALE, err := txn.GetCollectionByName(ctx, h.collections.AccessListEntry)
	if err != nil {
		return nil, fmt.Errorf("failed to get ALE collection: %w", err) //nolint: err113
	}
	return &existingBlockCollections{block: colBlock, tx: colTx, log: colLog, ale: colALE}, nil
}

// collectTxDocIDs builds transaction documents and appends their IDs to allDocIDs.
func (h *BlockHandler) collectTxDocIDs(ctx context.Context, transactions []*types.Transaction, blockID string, colTx client.Collection, allDocIDs *[]string) map[string]string {
	txHashToID := make(map[string]string)
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, colTx)
		if err != nil {
			continue
		}
		txID := txDoc.ID().String()
		txHashToID[tx.Hash] = txID
		*allDocIDs = append(*allDocIDs, txID)
	}
	return txHashToID
}

// collectLogDocIDs builds log documents and appends their IDs to allDocIDs.
func (h *BlockHandler) collectLogDocIDs(ctx context.Context, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt, txHashToID map[string]string, blockID string, colLog client.Collection, allDocIDs *[]string) {
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
				continue
			}
			*allDocIDs = append(*allDocIDs, logDoc.ID().String())
		}
	}
}

// collectALEDocIDs builds ALE documents and appends their IDs to allDocIDs.
func (h *BlockHandler) collectALEDocIDs(ctx context.Context, transactions []*types.Transaction, txHashToID map[string]string, blockNumber int64, colALE client.Collection, allDocIDs *[]string) {
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range tx.AccessList {
			aleDoc, err := h.buildALEDocument(ctx, &tx.AccessList[i], txID, blockNumber, colALE)
			if err != nil {
				continue
			}
			*allDocIDs = append(*allDocIDs, aleDoc.ID().String())
		}
	}
}

// waitForCIDs retries until CIDs are available for all documents (P2P data may still be arriving).
func (h *BlockHandler) waitForCIDs(ctx context.Context, blockNumber int64, allDocIDs []string) error {
	maxRetries := h.maxCIDRetries
	var lastCIDCount int
	var lastErr error

	for attempt := range maxRetries {
		cidTxn, err := h.db.NewBlindWriteTxn()
		if err != nil {
			lastErr = err
			if attempt < maxRetries-1 {
				time.Sleep(h.retryBackoffFn(attempt))
			}
			continue
		}
		cidCtx := h.db.InitContext(ctx, cidTxn)
		cids, err := h.collectDocCIDsFn(cidCtx, allDocIDs)
		cidTxn.Discard()

		if err != nil {
			lastErr = err
			if attempt < maxRetries-1 {
				time.Sleep(h.retryBackoffFn(attempt))
			}
			continue
		}

		lastCIDCount = len(cids)
		if len(cids) >= len(allDocIDs) {
			return nil
		}

		lastErr = fmt.Errorf("got %d CIDs for %d docs", len(cids), len(allDocIDs))
		if attempt < maxRetries-1 {
			logger.Sugar.Debugf("Block %d: waiting for P2P data (%d/%d CIDs, attempt %d/%d)",
				blockNumber, len(cids), len(allDocIDs), attempt+1, maxRetries)
			time.Sleep(h.retryBackoffFn(attempt))
		}
	}

	if lastCIDCount == 0 {
		return fmt.Errorf("no CIDs found for block %d after %d retries (%d docs): %w", //nolint: err113
			blockNumber, maxRetries, len(allDocIDs), lastErr)
	}

	return nil
}

// signAndStoreExistingBlockSignature collects CIDs, signs, and stores the block signature.
func (h *BlockHandler) signAndStoreExistingBlockSignature(ctx context.Context, blockNumber int64, blockHash string, allDocIDs []string) (string, error) {
	sigTxn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		return "", fmt.Errorf("failed to create signing transaction: %w", err) //nolint: err113
	}
	sigCtx := h.db.InitContext(ctx, sigTxn)

	cids, err := h.collectDocCIDsFn(sigCtx, allDocIDs)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to collect CIDs for signing: %w", err) //nolint: err113
	}

	collector := node.NewBlockCIDCollector()
	for _, c := range cids {
		collector.Add(c)
	}

	blockSig, err := h.signBlockFn(sigCtx, collector)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to sign block: %w", err) //nolint: err113
	}
	if blockSig == nil {
		sigTxn.Discard()
		return "", fmt.Errorf("signing returned nil (no identity?)") //nolint: err113
	}

	colBlockSig, err := sigTxn.GetCollectionByName(sigCtx, h.collections.BlockSignature)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to get block signature collection: %w", err) //nolint: err113
	}

	sortedCIDs := node.SortedCIDStrings(cids)
	blockSigDoc, err := h.buildBlockSignatureDocument(sigCtx, blockSig, blockHash, blockNumber, colBlockSig, sortedCIDs)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to build block signature document: %w", err) //nolint: err113
	}

	if err := colBlockSig.Create(sigCtx, blockSigDoc); err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to create block signature document: %w", err) //nolint: err113
	}

	if err := sigTxn.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit block signature: %w", err) //nolint: err113
	}

	docID := blockSigDoc.ID().String()
	logger.Sugar.Infof("Block %d: block sig for existing block (%d docs, %d CIDs, identity: %s...)",
		blockNumber, len(allDocIDs), len(cids), truncate(string(blockSig.Header.Identity), 16)) //nolint:mnd

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
// createBlockBatched creates all documents for a block using batched transactions.
func (h *BlockHandler) createBlockBatched(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	collector := node.NewBlockCIDCollector()
	ctx = node.ContextWithBlockSigning(ctx, collector)

	blockID, ctx, err := h.createBlockDocument(ctx, block, blockInt)
	if err != nil {
		return "", err
	}

	txHashToID, batchErrors := h.batchCreateTransactions(ctx, blockInt, transactions, blockID)
	allTxIDs := make([]string, 0, len(txHashToID))
	for _, id := range txHashToID {
		allTxIDs = append(allTxIDs, id)
	}

	allLogIDs, logErrors := h.batchCreateLogs(ctx, blockInt, transactions, receiptMap, blockID, txHashToID)
	batchErrors = append(batchErrors, logErrors...)

	allALEIDs, aleErrors := h.batchCreateALEs(ctx, blockInt, transactions, txHashToID)
	batchErrors = append(batchErrors, aleErrors...)

	blockSigDocID := h.createBlockSignature(ctx, block, blockInt, collector)

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

// createBlockDocument creates the block document in its own transaction.
func (h *BlockHandler) createBlockDocument(ctx context.Context, block *types.Block, blockInt int64) (string, context.Context, error) {
	txn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		return "", ctx, errors.NewQueryFailed("defra", "createBlockBatched", "failed to create transaction", err) //nolint: err113
	}
	ctx = h.db.InitContext(ctx, txn)

	colBlock, err := txn.GetCollectionByName(ctx, h.collections.Block)
	if err != nil {
		txn.Discard()
		return "", ctx, errors.NewQueryFailed("defra", "createBlockBatched", "failed to get block collection", err) //nolint: err113
	}

	blockDoc, err := h.buildBlockDocument(ctx, block, blockInt, colBlock)
	if err != nil {
		txn.Discard()
		return "", ctx, errors.NewQueryFailed("defra", "createBlockBatched", "failed to build block document", err)
	}
	blockID := blockDoc.ID().String()

	if err := colBlock.Create(ctx, blockDoc); err != nil {
		txn.Discard()
		if errors.IsErrAlreadyExists(err) {
			return "", ctx, fmt.Errorf("block already exists") //nolint: err113
		}
		return "", ctx, errors.NewQueryFailed("defra", "createBlockBatched", "failed to create block", err)
	}

	if err := txn.Commit(); err != nil {
		return "", ctx, errors.NewQueryFailed("defra", "createBlockBatched", "failed to commit block", err)
	}

	return blockID, ctx, nil
}

// batchCreateTransactions creates transaction documents in batches.
func (h *BlockHandler) batchCreateTransactions(ctx context.Context, blockInt int64, transactions []*types.Transaction, blockID string) (map[string]string, []error) {
	txHashToID := make(map[string]string)
	var batchErrors []error

	for i := 0; i < len(transactions); i += h.maxDocsPerTxn {
		end := min(i+h.maxDocsPerTxn, len(transactions))
		batch := transactions[i:end]
		if len(batch) == 0 {
			continue
		}

		txn, err := h.db.NewBlindWriteTxn()
		if err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("create txn for tx batch: %w", err))
			continue
		}
		ctx = h.db.InitContext(ctx, txn)

		colTx, err := txn.GetCollectionByName(ctx, h.collections.Transaction)
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
		}

		if len(txDocs) > 0 {
			if err := colTx.CreateMany(ctx, txDocs); err != nil {
				txn.Discard()
				if !errors.IsErrAlreadyExists(err) {
					batchErrors = append(batchErrors, fmt.Errorf("create tx batch: %w", err))
				} else {
					logger.Sugar.Debugf("Block %d: tx batch already exists via P2P, skipping", blockInt)
				}
				continue
			}
		}

		if err := txn.Commit(); err != nil {
			batchErrors = append(batchErrors, fmt.Errorf("commit tx batch: %w", err))
		}
	}

	return txHashToID, batchErrors
}

// batchCreateLogs creates log documents in batches.
// batchCreateLogs creates log documents in batches.
func (h *BlockHandler) batchCreateLogs(ctx context.Context, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt, blockID string, txHashToID map[string]string) ([]string, []error) {
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

	var allLogIDs []string
	var batchErrors []error
	for i := 0; i < len(allLogs); i += h.maxDocsPerTxn {
		end := min(i+h.maxDocsPerTxn, len(allLogs))
		batch := allLogs[i:end]
		if len(batch) == 0 {
			continue
		}
		ids, err := h.createLogBatch(ctx, blockInt, blockID, batch)
		allLogIDs = append(allLogIDs, ids...)
		if err != nil {
			batchErrors = append(batchErrors, err)
		}
	}

	return allLogIDs, batchErrors
}

// createLogBatch creates a single batch of log documents in one transaction.
func (h *BlockHandler) createLogBatch(ctx context.Context, blockInt int64, blockID string, batch []logEntry) ([]string, error) {
	txn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		return nil, fmt.Errorf("create txn for log batch: %w", err)
	}
	ctx = h.db.InitContext(ctx, txn)

	colLog, err := txn.GetCollectionByName(ctx, h.collections.Log)
	if err != nil {
		txn.Discard()
		return nil, fmt.Errorf("get log collection: %w", err)
	}

	var ids []string
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
		ids = append(ids, logDoc.ID().String())
	}

	if len(logDocs) > 0 {
		if err := colLog.CreateMany(ctx, logDocs); err != nil {
			txn.Discard()
			if errors.IsErrAlreadyExists(err) {
				logger.Sugar.Debugf("Block %d: log batch already exists via P2P, skipping", blockInt)
				return nil, nil
			}
			return nil, fmt.Errorf("create log batch: %w", err)
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit log batch: %w", err)
	}

	return ids, nil
}

// batchCreateALEs creates access list entry documents in batches.
// batchCreateALEs creates access list entry documents in batches.
func (h *BlockHandler) batchCreateALEs(ctx context.Context, blockInt int64, transactions []*types.Transaction, txHashToID map[string]string) ([]string, []error) {
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

	var allALEIDs []string
	var batchErrors []error
	for i := 0; i < len(allALEs); i += h.maxDocsPerTxn {
		end := min(i+h.maxDocsPerTxn, len(allALEs))
		ids, err := h.createALEBatch(ctx, blockInt, allALEs[i:end])
		allALEIDs = append(allALEIDs, ids...)
		if err != nil {
			batchErrors = append(batchErrors, err)
		}
	}

	return allALEIDs, batchErrors
}

// createALEBatch creates a single batch of ALE documents in one transaction.
func (h *BlockHandler) createALEBatch(ctx context.Context, blockInt int64, batch []aleEntry) ([]string, error) {
	txn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		return nil, fmt.Errorf("create txn for ALE batch: %w", err)
	}
	ctx = h.db.InitContext(ctx, txn)

	colALE, err := txn.GetCollectionByName(ctx, h.collections.AccessListEntry)
	if err != nil {
		txn.Discard()
		return nil, fmt.Errorf("get ALE collection: %w", err)
	}

	var ids []string
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
		ids = append(ids, aleDoc.ID().String())
	}

	if len(aleDocs) > 0 {
		if err := colALE.CreateMany(ctx, aleDocs); err != nil {
			txn.Discard()
			if errors.IsErrAlreadyExists(err) {
				logger.Sugar.Debugf("Block %d: ALE batch already exists via P2P, skipping", blockInt)
				return nil, nil
			}
			return nil, fmt.Errorf("create ALE batch: %w", err)
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit ALE batch: %w", err)
	}

	return ids, nil
}

// createBlockSignature creates the block signature document in its own transaction.
func (h *BlockHandler) createBlockSignature(ctx context.Context, block *types.Block, blockInt int64, collector *node.BlockCIDCollector) string {
	collectedCIDs := collector.GetCIDs()

	sigTxn, err := h.db.NewBlindWriteTxn()
	if err != nil {
		logger.Sugar.Warnf("Block %d: failed to create txn for block signature: %v", blockInt, err)
		return ""
	}

	sigCtx := h.db.InitContext(ctx, sigTxn)

	blockSig, err := h.signBlockFn(sigCtx, collector)
	if err != nil {
		sigTxn.Discard()
		logger.Sugar.Warnf("Failed to create block signature for block %d: %v", blockInt, err)
		return ""
	}
	if blockSig == nil {
		sigTxn.Discard()
		return ""
	}

	valid, verifyErr := h.verifyBlockSigFn(blockSig, collectedCIDs)
	if verifyErr != nil {
		logger.Sugar.Warnf("Block %d: block signature verification error: %v", blockInt, verifyErr)
	} else if !valid {
		logger.Sugar.Warnf("Block %d: block signature verification FAILED", blockInt)
	}

	colBlockSig, err := sigTxn.GetCollectionByName(sigCtx, h.collections.BlockSignature)
	if err != nil {
		sigTxn.Discard()
		logger.Sugar.Warnf("Block %d: failed to get block signature collection: %v", blockInt, err)
		return ""
	}

	sortedCIDs := node.SortedCIDStrings(collectedCIDs)
	blockSigDoc, err := h.buildBlockSignatureDocument(sigCtx, blockSig, block.Hash, blockInt, colBlockSig, sortedCIDs)
	if err != nil {
		sigTxn.Discard()
		logger.Sugar.Warnf("Block %d: failed to build block signature document: %v", blockInt, err)
		return ""
	}

	if err := colBlockSig.Create(sigCtx, blockSigDoc); err != nil {
		sigTxn.Discard()
		logger.Sugar.Warnf("Block %d: failed to create block signature document: %v", blockInt, err)
		return ""
	}

	if err := sigTxn.Commit(); err != nil {
		logger.Sugar.Warnf("Block %d: failed to commit block signature: %v", blockInt, err)
		return ""
	}

	blockSigDocID := blockSigDoc.ID().String()
	logger.Sugar.Debugf("Block %d (batched): block sig created, %d CIDs, merkle: %x, verified: %v",
		blockInt, blockSig.CIDCount, blockSig.MerkleRoot[:8], valid) //nolint:mnd

	return blockSigDocID
}

// GetHighestBlockNumber returns the highest block number stored in DefraDB.
func (h *BlockHandler) GetHighestBlockNumber(ctx context.Context) (int64, error) {
	query := `query {` + h.collections.Block + ` (order: {number: DESC}, limit: 1) { number }}`

	result := h.db.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, errors.NewQueryFailed("defra", "GetHighestBlockNumber", query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", h.collections.Block, "no data")
	}

	var block map[string]any
	switch arr := data[h.collections.Block].(type) {
	case []any:
		if len(arr) == 0 {
			return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", h.collections.Block, "no blocks")
		}
		var ok bool
		block, ok = arr[0].(map[string]any)
		if !ok {
			return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", h.collections.Block, "invalid format")
		}
	case []map[string]any:
		if len(arr) == 0 {
			return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", h.collections.Block, "no blocks")
		}
		block = arr[0]
	default:
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", h.collections.Block, "no blocks")
	}

	switch v := block[constants.NumberFieldValue].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	}

	return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", h.collections.Block, "invalid number type")
}
