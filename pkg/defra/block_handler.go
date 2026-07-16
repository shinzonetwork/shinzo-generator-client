package defra

import (
	"context"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
)

var errNoIdentity = stderrors.New("no identity available for signing") //nolint:gochecknoglobals

// blockDB abstracts the DB operations used by BlockHandler for testability.
type blockDB interface {
	NewTxn(readOnly bool) (client.Txn, error)
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
	db              blockDB                    // DB interface (from defraNode.DB).
	maxDocsPerTxn   int                        // Threshold for single-txn vs batched block creation.
	maxTxBatchSize  int                        // Per-collection batch size for transactions (0 = use maxDocsPerTxn).
	maxLogBatchSize int                        // Per-collection batch size for logs (0 = use maxDocsPerTxn).
	maxALEBatchSize int                        // Per-collection batch size for ALEs (0 = use maxDocsPerTxn).
	docIDTracker    DocIDTrackerInterface      // Optional tracker for docIDs.
	collections     *constants.CollectionNames // Chain-specific collection names.
	nodeIdentity    identity.Identity          // Node identity for signing.

	// Injectable functions for testability (set to defaults in NewBlockHandler).
	signBatchFn      func(ctx context.Context, collector *node.BatchCIDCollector) (*node.BatchSignature, error)
	verifyBatchSigFn func(sig *node.BatchSignature, cids []cid.Cid) (bool, error)
	collectDocCIDsFn func(ctx context.Context, docIDs []string) ([]cid.Cid, error)
	blockExistsFn    func(ctx context.Context, blockNumber int64) (bool, error)
	maxCIDRetries    int
	retryBackoffFn   func(int) time.Duration
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
	h := &BlockHandler{
		db:             defraNode.DB,
		maxDocsPerTxn:  maxDocsPerTxn,
		collections:    collections,
		maxCIDRetries:  15, //nolint:mnd
		retryBackoffFn: retryBackoff,
	}
	h.signBatchFn = h.defaultSignBatch
	h.verifyBatchSigFn = node.VerifyBatchSignature
	h.collectDocCIDsFn = h.defaultCollectDocCIDs
	h.blockExistsFn = h.defaultBlockExists
	return h, nil
}

// SetBatchSizes sets per-collection batch sizes for transactions, logs, and ALEs.
// A value of 0 means "use maxDocsPerTxn" for that collection.
func (h *BlockHandler) SetBatchSizes(txDocs, logDocs, aleDocs int) {
	h.maxTxBatchSize = txDocs
	h.maxLogBatchSize = logDocs
	h.maxALEBatchSize = aleDocs
}

func (h *BlockHandler) txBatchSize() int {
	if h.maxTxBatchSize > 0 {
		return h.maxTxBatchSize
	}
	return h.maxDocsPerTxn
}

func (h *BlockHandler) logBatchSize() int {
	if h.maxLogBatchSize > 0 {
		return h.maxLogBatchSize
	}
	return h.maxDocsPerTxn
}

func (h *BlockHandler) aleBatchSize() int {
	if h.maxALEBatchSize > 0 {
		return h.maxALEBatchSize
	}
	return h.maxDocsPerTxn
}

func (h *BlockHandler) defaultBlockExists(ctx context.Context, blockNumber int64) (bool, error) {
	query := `query { ` + h.collections.Block + `(filter: {number: {_eq: ` + strconv.FormatInt(blockNumber, 10) + `}}) { _docID } }`
	result := h.db.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return false, fmt.Errorf("block exists check failed: %w", result.GQL.Errors[0])
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return false, nil
	}
	switch results := data[h.collections.Block].(type) {
	case []any:
		return len(results) > 0, nil
	case []map[string]any:
		return len(results) > 0, nil
	}
	return false, nil
}

// SetNodeIdentity sets the node identity used for block signing.
func (h *BlockHandler) SetNodeIdentity(id identity.Identity) {
	h.nodeIdentity = id
}

// defaultSignBatch signs the collected CIDs using the node identity.
func (h *BlockHandler) defaultSignBatch(ctx context.Context, collector *node.BatchCIDCollector) (*node.BatchSignature, error) {
	nodeIdent := h.nodeIdentity
	if nodeIdent == nil {
		id, ok := defracontext.IdentityFrom(ctx)
		if !ok {
			return nil, errNoIdentity
		}
		nodeIdent = id
	}
	fullIdent, ok := nodeIdent.(identity.FullIdentity)
	if !ok {
		return nil, fmt.Errorf("identity is not a FullIdentity") //nolint:err113
	}

	cids := collector.GetCIDs()
	merkleRoot := node.ComputeMerkleRoot(cids)

	sigValue, err := fullIdent.PrivateKey().Sign(merkleRoot)
	if err != nil {
		return nil, fmt.Errorf("sign merkle root: %w", err)
	}

	var sigType string
	switch fullIdent.PrivateKey().Type() { //nolint:exhaustive
	case crypto.KeyTypeSecp256k1:
		sigType = "ES256K"
	case crypto.KeyTypeEd25519:
		sigType = "EdDSA"
	default:
		return nil, fmt.Errorf("unsupported key type: %v", fullIdent.PrivateKey().Type()) //nolint:err113
	}

	sig := &node.BatchSignature{}
	sig.Header.Type = sigType
	sig.Header.Identity = []byte(fullIdent.PublicKey().String())
	sig.Value = sigValue
	sig.MerkleRoot = merkleRoot
	sig.CIDCount = len(cids)
	return sig, nil
}

// buildDocIDJSONArray builds a JSON array string from docIDs for use in GQL filters.
func buildDocIDJSONArray(docIDs []string) string {
	var idsJSON strings.Builder
	idsJSON.WriteString(`[`)
	for i, id := range docIDs {
		if i > 0 {
			idsJSON.WriteString(",")
		}
		idsJSON.WriteString(`"` + id + `"`)
	}
	idsJSON.WriteString(`]`)
	return idsJSON.String()
}

// extractCIDsFromCollection queries a single collection and returns all CIDs found.
func (h *BlockHandler) extractCIDsFromCollection(ctx context.Context, colName, idsJSON string) []cid.Cid {
	query := `query { ` + colName + `(filter: {_docID: {_in: ` + idsJSON + `}}) { _version { cid } } }`
	result := h.db.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil
	}
	var docMaps []map[string]any
	switch v := data[colName].(type) {
	case []any:
		for _, d := range v {
			if m, ok := d.(map[string]any); ok {
				docMaps = append(docMaps, m)
			}
		}
	case []map[string]any:
		docMaps = v
	}
	var cids []cid.Cid
	for _, docMap := range docMaps {
		var versions []map[string]any
		switch v := docMap["_version"].(type) {
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					versions = append(versions, m)
				}
			}
		case []map[string]any:
			versions = v
		}
		for _, vMap := range versions {
			cidStr, _ := vMap["cid"].(string)
			if cidStr == "" {
				continue
			}
			c, err := cid.Decode(cidStr)
			if err == nil {
				cids = append(cids, c)
			}
		}
	}
	return cids
}

// defaultCollectDocCIDs queries each collection via GQL to retrieve CIDs for the given docIDs.
func (h *BlockHandler) defaultCollectDocCIDs(ctx context.Context, docIDs []string) ([]cid.Cid, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}

	idsJSON := buildDocIDJSONArray(docIDs)

	colNames := []string{
		h.collections.Block,
		h.collections.Transaction,
		h.collections.Log,
		h.collections.AccessListEntry,
	}

	var allCIDs []cid.Cid
	for _, colName := range colNames {
		allCIDs = append(allCIDs, h.extractCIDsFromCollection(ctx, colName, idsJSON)...)
	}
	return allCIDs, nil
}

// sortedCIDStrings returns a sorted slice of CID strings.
func sortedCIDStrings(cids []cid.Cid) []string {
	out := make([]string, len(cids))
	for i, c := range cids {
		out[i] = c.String()
	}
	sort.Strings(out)
	return out
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

	exists, err := h.blockExistsFn(ctx, blockInt)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "CreateBlockBatch", "block exists check failed", err)
	}
	if exists {
		return "", fmt.Errorf("block already exists") //nolint: err113
	}

	receiptMap := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	totalLogs := 0
	totalALEs := 0
	missingReceipts := 0
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		if receipt, ok := receiptMap[tx.Hash]; ok && receipt != nil {
			totalLogs += len(receipt.Logs)
		} else {
			missingReceipts++
		}
		totalALEs += len(tx.AccessList)
	}
	if missingReceipts > 0 {
		logger.Sugar.Warnf("Block %d: %d transactions have no receipt; their logs will not be indexed", blockInt, missingReceipts)
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
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transaction", err)
	}

	collector := node.NewBatchCIDCollector()
	ctx = node.ContextWithBatchSigning(ctx, collector)

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

	// Capture the signed CID count before the signature document is written: writing it feeds the
	// same collector, so a later read would be one too high.
	signedCIDCount := len(collector.GetCIDs())
	blockSigDocID := h.buildAndCreateSingleTxnSignature(ctx, block, blockInt, collector, cols.blockSig)

	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to commit", err)
	}

	if blockSigDocID != "" {
		logger.Sugar.Infof("Block %d: signed (%d CIDs)", blockInt, signedCIDCount)
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
	if err := cols.block.AddDocument(ctx, blockDoc); err != nil {
		if errors.IsErrAlreadyExists(err) {
			return "", nil, nil, nil, fmt.Errorf("block already exists") //nolint: err113
		}
		return "", nil, nil, nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", err.Error(), err)
	}

	blockID := blockDoc.ID().String()

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
	txHashes := make([]string, 0, len(transactions))
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, colTx)
		if err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to build tx document", err)
		}
		txDocs = append(txDocs, txDoc)
		txHashes = append(txHashes, tx.Hash)
	}

	if len(txDocs) > 0 {
		if err := colTx.AddManyDocuments(ctx, txDocs); err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transactions", err)
		}
		for i, doc := range txDocs {
			txHashToID[txHashes[i]] = doc.ID().String()
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
		if err := colLog.AddManyDocuments(ctx, logDocs); err != nil {
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
		if err := colALE.AddManyDocuments(ctx, aleDocs); err != nil {
			return nil, errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create ALEs", err)
		}
	}

	return aleDocs, nil
}

// buildAndCreateSingleTxnSignature signs the block over the CIDs written in this transaction and
// adds the signature document. It returns the signature document id, or empty when the block is
// not signed (the block and its documents are still committed by the caller). A signature that
// fails self-verification is not stored.
func (h *BlockHandler) buildAndCreateSingleTxnSignature(ctx context.Context, block *types.Block, blockInt int64, collector *node.BatchCIDCollector, colBlockSig client.Collection) string {
	collectedCIDs := collector.GetCIDs()

	blockSig, err := h.signBatchFn(ctx, collector)
	if err != nil {
		logger.Sugar.Warnf("Block %d: signing failed: %v", blockInt, err)
		return ""
	}
	if blockSig == nil {
		logger.Sugar.Warnf("Block %d: not signing, no identity available", blockInt)
		return ""
	}

	// The signature must verify over the CIDs it attests. A failure means signing produced an
	// inconsistent signature, so it is not stored.
	if valid, verifyErr := h.verifyBatchSigFn(blockSig, collectedCIDs); verifyErr != nil {
		logger.Sugar.Warnf("Block %d: not signing, verify error: %v", blockInt, verifyErr)
		return ""
	} else if !valid {
		logger.Sugar.Warnf("Block %d: not signing, signature failed self-verification", blockInt)
		return ""
	}

	sortedCIDs := sortedCIDStrings(collectedCIDs)
	blockSigDoc, err := h.buildBlockSignatureDocument(ctx, blockSig, block.Hash, blockInt, colBlockSig, sortedCIDs)
	if err != nil {
		logger.Sugar.Warnf("Block %d: failed to build block signature document: %v", blockInt, err)
		return ""
	}

	if err := colBlockSig.AddDocument(ctx, blockSigDoc); err != nil {
		logger.Sugar.Warnf("Block %d: failed to create block signature document: %v", blockInt, err)
		return ""
	}

	return blockSigDoc.ID().String()
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
func (h *BlockHandler) buildBlockSignatureDocument(ctx context.Context, blockSig *node.BatchSignature, blockHash string, blockNumber int64, col client.Collection, sortedCIDStrings []string) (*client.Document, error) {
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
	_ *types.Block,
	_ []*types.Transaction,
	_ []*types.TransactionReceipt,
) (string, error) {
	if h.db == nil {
		return "", fmt.Errorf("defraNode is nil") //nolint: err113
	}

	allDocIDs, err := h.collectExistingBlockDocIDs(ctx, blockNumber)
	if err != nil {
		return "", err
	}

	cids, err := h.waitForCIDs(ctx, blockNumber, allDocIDs)
	if err != nil {
		return "", err
	}

	return h.signBlockOverCIDs(ctx, blockNumber, blockHash, len(allDocIDs), cids)
}

// collectExistingBlockDocIDs queries the DB for all docIDs associated with the given block number.
func (h *BlockHandler) collectExistingBlockDocIDs(ctx context.Context, blockNumber int64) ([]string, error) {
	var allDocIDs []string
	blockNumStr := strconv.FormatInt(blockNumber, 10)

	blockDocIDs, err := h.queryDocIDsByBlockNumber(ctx, h.collections.Block, "number", blockNumStr)
	if err != nil {
		return nil, fmt.Errorf("query block docIDs: %w", err)
	}
	allDocIDs = append(allDocIDs, blockDocIDs...)

	txDocIDs, err := h.queryDocIDsByBlockNumber(ctx, h.collections.Transaction, "blockNumber", blockNumStr)
	if err != nil {
		return nil, fmt.Errorf("query tx docIDs: %w", err)
	}
	allDocIDs = append(allDocIDs, txDocIDs...)

	logDocIDs, err := h.queryDocIDsByBlockNumber(ctx, h.collections.Log, "blockNumber", blockNumStr)
	if err != nil {
		return nil, fmt.Errorf("query log docIDs: %w", err)
	}
	allDocIDs = append(allDocIDs, logDocIDs...)

	aleDocIDs, err := h.queryDocIDsByBlockNumber(ctx, h.collections.AccessListEntry, "blockNumber", blockNumStr)
	if err != nil {
		return nil, fmt.Errorf("query ale docIDs: %w", err)
	}
	allDocIDs = append(allDocIDs, aleDocIDs...)

	return allDocIDs, nil
}

// queryDocIDsByBlockNumber queries a collection for docIDs filtered by a block number field.
func (h *BlockHandler) queryDocIDsByBlockNumber(ctx context.Context, colName, filterField, value string) ([]string, error) {
	query := `query { ` + colName + `(filter: {` + filterField + `: {_eq: ` + value + `}}) { _docID } }`
	result := h.db.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil, result.GQL.Errors[0]
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil, nil
	}
	var docIDs []string
	switch results := data[colName].(type) {
	case []any:
		for _, r := range results {
			if m, ok := r.(map[string]any); ok {
				if id, ok := m["_docID"].(string); ok {
					docIDs = append(docIDs, id)
				}
			}
		}
	case []map[string]any:
		for _, m := range results {
			if id, ok := m["_docID"].(string); ok {
				docIDs = append(docIDs, id)
			}
		}
	}
	return docIDs, nil
}

// waitForCIDs collects the CIDs for allDocIDs, retrying while they are still arriving (P2P data
// can lag). It returns the CIDs only once every document has one; partial coverage is an error so
// a signature is never made over a subset of the block.
func (h *BlockHandler) waitForCIDs(ctx context.Context, blockNumber int64, allDocIDs []string) ([]cid.Cid, error) {
	maxRetries := h.maxCIDRetries
	var lastCIDCount int
	var lastErr error

	for attempt := range maxRetries {
		cids, err := h.collectDocCIDsFn(ctx, allDocIDs)
		if err != nil {
			lastErr = err
			logger.Sugar.Warnf("Block %d: CID query failed (attempt %d/%d): %v", blockNumber, attempt+1, maxRetries, err)
			if attempt < maxRetries-1 {
				time.Sleep(h.retryBackoffFn(attempt))
			}
			continue
		}

		lastCIDCount = len(cids)
		if len(cids) >= len(allDocIDs) {
			return cids, nil
		}

		lastErr = fmt.Errorf("got %d CIDs for %d docs", len(cids), len(allDocIDs)) //nolint:err113
		if attempt < maxRetries-1 {
			logger.Sugar.Debugf("Block %d: waiting for P2P data (%d/%d CIDs, attempt %d/%d)",
				blockNumber, len(cids), len(allDocIDs), attempt+1, maxRetries)
			time.Sleep(h.retryBackoffFn(attempt))
		}
	}

	if lastCIDCount == 0 {
		return nil, fmt.Errorf("no CIDs found for block %d after %d retries (%d docs): %w", //nolint:err113
			blockNumber, maxRetries, len(allDocIDs), lastErr)
	}
	return nil, fmt.Errorf("incomplete CID coverage for block %d after %d retries (%d/%d docs): %w", //nolint:err113
		blockNumber, maxRetries, lastCIDCount, len(allDocIDs), lastErr)
}

// signBlockOverCIDs signs the block over cids and stores the signature, returning its document id.
// docCount is the number of documents the CIDs cover and is used only for logging.
func (h *BlockHandler) signBlockOverCIDs(ctx context.Context, blockNumber int64, blockHash string, docCount int, cids []cid.Cid) (string, error) {
	collector := node.NewBatchCIDCollector()
	for _, c := range cids {
		collector.Add(c)
	}

	blockSig, err := h.signBatchFn(ctx, collector)
	if err != nil {
		return "", fmt.Errorf("failed to sign block: %w", err) //nolint: err113
	}
	if blockSig == nil {
		return "", fmt.Errorf("signing returned nil (no identity?)") //nolint: err113
	}

	// The signature must verify over the CIDs it is about to attest. A failure means signing
	// produced an inconsistent signature, so it is not stored.
	if valid, verifyErr := h.verifyBatchSigFn(blockSig, cids); verifyErr != nil {
		return "", fmt.Errorf("verify block signature: %w", verifyErr) //nolint: err113
	} else if !valid {
		return "", fmt.Errorf("block signature failed self-verification") //nolint: err113
	}

	sigTxn, err := h.db.NewTxn(false)
	if err != nil {
		return "", fmt.Errorf("failed to create signing transaction: %w", err) //nolint: err113
	}

	colBlockSig, err := sigTxn.GetCollectionByName(ctx, h.collections.BlockSignature)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to get block signature collection: %w", err) //nolint: err113
	}

	sortedCIDs := sortedCIDStrings(cids)
	blockSigDoc, err := h.buildBlockSignatureDocument(ctx, blockSig, blockHash, blockNumber, colBlockSig, sortedCIDs)
	if err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to build block signature document: %w", err) //nolint: err113
	}

	if err := colBlockSig.AddDocument(ctx, blockSigDoc); err != nil {
		sigTxn.Discard()
		return "", fmt.Errorf("failed to create block signature document: %w", err) //nolint: err113
	}

	if err := sigTxn.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit block signature: %w", err) //nolint: err113
	}

	docID := blockSigDoc.ID().String()
	logger.Sugar.Infof("Block %d: signed (%d docs, %d CIDs, identity: %s...)",
		blockNumber, docCount, len(cids), truncate(string(blockSig.Header.Identity), 16)) //nolint:mnd

	return docID, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// expectedBlockDocCount returns how many documents a fully written block contains: one Block,
// one per transaction, one per log, and one per access-list entry. A shortfall against the
// written set means a batch was dropped, so the block must not be signed.
func expectedBlockDocCount(transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) int {
	txs, logs, ales := 0, 0, 0
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txs++
		if receipt, ok := receiptMap[tx.Hash]; ok && receipt != nil {
			logs += len(receipt.Logs)
		}
		ales += len(tx.AccessList)
	}
	return 1 + txs + logs + ales
}

// createBlockBatched creates all documents for a block using batched transactions. It is the
// path for blocks exceeding maxDocsPerTxn.
func (h *BlockHandler) createBlockBatched(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	// A batch collector in the context puts DefraDB in batch-signing mode: per-block signing is
	// skipped and the block is signed once, below, over the CIDs it commits. The collector's
	// contents are not used for the signature.
	collector := node.NewBatchCIDCollector()
	ctx = node.ContextWithBatchSigning(ctx, collector)

	blockID, err := h.createBlockDocument(ctx, block, blockInt)
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

	allDocIDs := make([]string, 0, 1+len(allTxIDs)+len(allLogIDs)+len(allALEIDs))
	allDocIDs = append(allDocIDs, blockID)
	allDocIDs = append(allDocIDs, allTxIDs...)
	allDocIDs = append(allDocIDs, allLogIDs...)
	allDocIDs = append(allDocIDs, allALEIDs...)

	for _, be := range batchErrors {
		logger.Sugar.Warnf("Block %d: batch write error: %v", blockInt, be)
	}

	// Sign only a fully written block, over the CIDs the DB committed. A conflict can drop a
	// batch and leave the set short; such a block stays unsigned rather than attesting a merkle
	// root over documents it does not hold.
	blockSigDocID := ""
	expectedDocs := expectedBlockDocCount(transactions, receiptMap)
	if len(batchErrors) == 0 && len(allDocIDs) == expectedDocs {
		if cids, err := h.waitForCIDs(ctx, blockInt, allDocIDs); err != nil {
			logger.Sugar.Warnf("Block %d: not signing, incomplete CID coverage: %v", blockInt, err)
		} else if sigID, sigErr := h.signBlockOverCIDs(ctx, blockInt, block.Hash, len(allDocIDs), cids); sigErr != nil {
			logger.Sugar.Warnf("Block %d: signing failed: %v", blockInt, sigErr)
		} else {
			blockSigDocID = sigID
		}
	} else {
		logger.Sugar.Warnf("Block %d: not signing partial block: %d/%d docs (txs %d, logs %d, ALEs %d), %d batch errors",
			blockInt, len(allDocIDs), expectedDocs, len(allTxIDs), len(allLogIDs), len(allALEIDs), len(batchErrors))
	}

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
func (h *BlockHandler) createBlockDocument(ctx context.Context, block *types.Block, blockInt int64) (string, error) {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return "", err
	}

	colBlock, err := txn.GetCollectionByName(ctx, h.collections.Block)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to get block collection", err) //nolint: err113
	}

	blockDoc, err := h.buildBlockDocument(ctx, block, blockInt, colBlock)
	if err != nil {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to build block document", err)
	}

	if err := colBlock.AddDocument(ctx, blockDoc); err != nil {
		txn.Discard()
		if errors.IsErrAlreadyExists(err) {
			return "", fmt.Errorf("block already exists") //nolint: err113
		}
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to create block", err)
	}

	blockID := blockDoc.ID().String()

	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to commit block", err)
	}

	return blockID, nil
}

const (
	// maxBatchRetries caps how many times a batch write is attempted when it hits a transaction
	// conflict. Document creates share the /seq/doc sequence, so concurrent writers collide; a
	// retry rebuilds and re-commits the batch.
	maxBatchRetries = 3
	// batchConflictRetryDelay is the base delay between batch retries; the wait grows linearly with
	// the attempt (50ms, 100ms, ...).
	batchConflictRetryDelay = 50 * time.Millisecond
)

// writeBatchWithRetry runs write, retrying on a transaction conflict up to maxBatchRetries with
// backoff. kind names the batch for the retry log.
func (h *BlockHandler) writeBatchWithRetry(blockInt int64, kind string, write func() error) error {
	for attempt := range maxBatchRetries {
		err := write()
		if err == nil || !errors.IsErrTransactionConflict(err) {
			return err
		}
		if attempt == maxBatchRetries-1 {
			logger.Sugar.Warnf("Block %d: %s batch still conflicting after %d attempts, giving up", blockInt, kind, maxBatchRetries)
			return err
		}
		logger.Sugar.Infof("Block %d: %s batch conflict, retrying (attempt %d/%d)", blockInt, kind, attempt+1, maxBatchRetries)
		time.Sleep(time.Duration(attempt+1) * batchConflictRetryDelay)
	}
	return nil
}

// batchCreateTransactions creates transaction documents in batches, retrying a batch on conflict.
func (h *BlockHandler) batchCreateTransactions(ctx context.Context, blockInt int64, transactions []*types.Transaction, blockID string) (map[string]string, []error) {
	txHashToID := make(map[string]string)
	var batchErrors []error

	txBS := h.txBatchSize()
	for i := 0; i < len(transactions); i += txBS {
		end := min(i+txBS, len(transactions))
		batch := transactions[i:end]
		if len(batch) == 0 {
			continue
		}

		var ids map[string]string
		err := h.writeBatchWithRetry(blockInt, "transaction", func() error {
			var e error
			ids, e = h.createTransactionBatch(ctx, blockInt, batch, blockID)
			return e
		})
		maps.Copy(txHashToID, ids)
		if err != nil {
			batchErrors = append(batchErrors, err)
		}
	}

	return txHashToID, batchErrors
}

// createTransactionBatch writes one batch of transaction documents in a single transaction and
// returns the hash-to-docID map for the batch.
func (h *BlockHandler) createTransactionBatch(ctx context.Context, blockInt int64, batch []*types.Transaction, blockID string) (map[string]string, error) {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("create txn for tx batch: %w", err)
	}
	colTx, err := txn.GetCollectionByName(ctx, h.collections.Transaction)
	if err != nil {
		txn.Discard()
		return nil, fmt.Errorf("get tx collection: %w", err)
	}

	txDocs := make([]*client.Document, 0, len(batch))
	txHashes := make([]string, 0, len(batch))
	for _, tx := range batch {
		if tx == nil {
			continue
		}
		txDoc, err := h.buildTransactionDocument(ctx, tx, blockID, colTx)
		if err != nil {
			logger.Sugar.Warnf("Block %d: failed to build tx document %s, skipping: %v", blockInt, tx.Hash, err)
			continue
		}
		txDocs = append(txDocs, txDoc)
		txHashes = append(txHashes, tx.Hash)
	}

	ids := make(map[string]string, len(txDocs))
	if len(txDocs) > 0 {
		if err := colTx.AddManyDocuments(ctx, txDocs); err != nil {
			txn.Discard()
			if errors.IsErrAlreadyExists(err) {
				logger.Sugar.Warnf("Block %d: transaction batch already exists, skipping %d txs; their logs and access-list entries are skipped too", blockInt, len(txDocs))
				return ids, nil
			}
			return nil, fmt.Errorf("create tx batch: %w", err)
		}
		for i, doc := range txDocs {
			ids[txHashes[i]] = doc.ID().String()
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx batch: %w", err)
	}

	return ids, nil
}

// batchCreateLogs creates log documents in batches.
func (h *BlockHandler) batchCreateLogs(ctx context.Context, blockInt int64, transactions []*types.Transaction, receiptMap map[string]*types.TransactionReceipt, blockID string, txHashToID map[string]string) ([]string, []error) {
	var allLogs []logEntry
	skippedLogs := 0
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
			skippedLogs += len(receipt.Logs)
			continue
		}
		for i := range receipt.Logs {
			allLogs = append(allLogs, logEntry{log: &receipt.Logs[i], txID: txID})
		}
	}
	if skippedLogs > 0 {
		logger.Sugar.Warnf("Block %d: skipping %d logs whose transaction was not written", blockInt, skippedLogs)
	}

	var allLogIDs []string
	var batchErrors []error
	logBS := h.logBatchSize()
	for i := 0; i < len(allLogs); i += logBS {
		end := min(i+logBS, len(allLogs))
		batch := allLogs[i:end]
		if len(batch) == 0 {
			continue
		}
		var ids []string
		err := h.writeBatchWithRetry(blockInt, "log", func() error {
			var e error
			ids, e = h.createLogBatch(ctx, blockInt, blockID, batch)
			return e
		})
		allLogIDs = append(allLogIDs, ids...)
		if err != nil {
			batchErrors = append(batchErrors, err)
		}
	}

	return allLogIDs, batchErrors
}

// createLogBatch creates a single batch of log documents in one transaction.
func (h *BlockHandler) createLogBatch(ctx context.Context, blockInt int64, blockID string, batch []logEntry) ([]string, error) {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("create txn for log batch: %w", err)
	}
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
			logger.Sugar.Warnf("Block %d: failed to build log document, skipping: %v", blockInt, err)
			continue
		}
		logDocs = append(logDocs, logDoc)
	}

	if len(logDocs) > 0 {
		if err := colLog.AddManyDocuments(ctx, logDocs); err != nil {
			txn.Discard()
			if errors.IsErrAlreadyExists(err) {
				logger.Sugar.Warnf("Block %d: log batch already exists, skipping %d logs", blockInt, len(logDocs))
				return nil, nil
			}
			return nil, fmt.Errorf("create log batch: %w", err)
		}
		for _, doc := range logDocs {
			ids = append(ids, doc.ID().String())
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit log batch: %w", err)
	}

	return ids, nil
}

// batchCreateALEs creates access list entry documents in batches.
func (h *BlockHandler) batchCreateALEs(ctx context.Context, blockInt int64, transactions []*types.Transaction, txHashToID map[string]string) ([]string, []error) {
	var allALEs []aleEntry
	skippedALEs := 0
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			skippedALEs += len(tx.AccessList)
			continue
		}
		for i := range tx.AccessList {
			allALEs = append(allALEs, aleEntry{ale: &tx.AccessList[i], txID: txID, blockNumber: blockInt})
		}
	}
	if skippedALEs > 0 {
		logger.Sugar.Warnf("Block %d: skipping %d access-list entries whose transaction was not written", blockInt, skippedALEs)
	}

	var allALEIDs []string
	var batchErrors []error
	aleBS := h.aleBatchSize()
	for i := 0; i < len(allALEs); i += aleBS {
		end := min(i+aleBS, len(allALEs))
		batch := allALEs[i:end]
		var ids []string
		err := h.writeBatchWithRetry(blockInt, "access-list", func() error {
			var e error
			ids, e = h.createALEBatch(ctx, blockInt, batch)
			return e
		})
		allALEIDs = append(allALEIDs, ids...)
		if err != nil {
			batchErrors = append(batchErrors, err)
		}
	}

	return allALEIDs, batchErrors
}

// createALEBatch creates a single batch of ALE documents in one transaction.
func (h *BlockHandler) createALEBatch(ctx context.Context, blockInt int64, batch []aleEntry) ([]string, error) {
	txn, err := h.db.NewTxn(false)
	if err != nil {
		return nil, fmt.Errorf("create txn for ALE batch: %w", err)
	}
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
			logger.Sugar.Warnf("Block %d: failed to build access-list document, skipping: %v", blockInt, err)
			continue
		}
		aleDocs = append(aleDocs, aleDoc)
	}

	if len(aleDocs) > 0 {
		if err := colALE.AddManyDocuments(ctx, aleDocs); err != nil {
			txn.Discard()
			if errors.IsErrAlreadyExists(err) {
				logger.Sugar.Warnf("Block %d: access-list batch already exists, skipping %d entries", blockInt, len(aleDocs))
				return nil, nil
			}
			return nil, fmt.Errorf("create ALE batch: %w", err)
		}
		for _, doc := range aleDocs {
			ids = append(ids, doc.ID().String())
		}
	}

	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit ALE batch: %w", err)
	}

	return ids, nil
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
