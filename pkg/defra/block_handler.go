package defra

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type BlockHandler struct {
	defraURL      string
	client        *http.Client
	defraNode     *node.Node // Direct access to embedded DefraDB (nil if using HTTP)
	maxDocsPerTxn int        // Threshold for single-txn vs batched block creation

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

func NewBlockHandler(url string) (*BlockHandler, error) {
	if url == "" {
		return nil, errors.NewConfigurationError("defra", "NewBlockHandler",
			"url parameter is empty", url, nil)
	}
	return &BlockHandler{
		defraURL: strings.Replace(fmt.Sprintf("%s/api/v0/graphql", url), "127.0.0.1", "localhost", 1),
		client: &http.Client{
			Timeout: 30 * time.Second, // Add 30-second timeout to prevent hanging
		},
		defraNode: nil,
	}, nil
}

// NewBlockHandlerWithNode creates a BlockHandler that uses direct DB calls for better performance.
// maxDocsPerTxn is the threshold for single-txn vs batched block creation.
func NewBlockHandlerWithNode(defraNode *node.Node, maxDocsPerTxn int) (*BlockHandler, error) {
	if defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "NewBlockHandlerWithNode",
			"defraNode is nil", "", nil)
	}
	if maxDocsPerTxn <= 0 {
		maxDocsPerTxn = 1000
	}
	return &BlockHandler{
		defraNode:     defraNode,
		client:        nil,
		defraURL:      "",
		maxDocsPerTxn: maxDocsPerTxn,
	}, nil
}

func (h *BlockHandler) CreateBlock(ctx context.Context, block *types.Block) (string, error) {
	// Input validation
	if block == nil {
		return "", errors.NewInvalidBlockFormat("defra", "CreateBlock", fmt.Sprintf("%v", block), nil)
	}

	// Data conversion
	blockInt, err := utils.HexToInt(block.Number)
	if err != nil {
		return "", err // Already properly wrapped
	}

	// Create block data
	blockData := map[string]interface{}{
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
	// Post block data to collection endpoint
	docID, err := h.PostToCollection(ctx, constants.CollectionBlock, blockData)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "CreateBlock", fmt.Sprintf("%v", blockData), err)
	}

	return docID, nil
}

func (h *BlockHandler) CreateTransaction(ctx context.Context, tx *types.Transaction, block_id string) (string, error) {
	// Function input validation
	if tx == nil {
		return "", errors.NewInvalidInputFormat("defra", "CreateTransaction", "tx", nil)
	}

	blockInt, err := strconv.ParseInt(tx.BlockNumber, 10, 64)
	if err != nil {
		return "", errors.NewParsingFailed("defra", "CreateTransaction", "block number", err)
	}

	txData := map[string]interface{}{
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
		"nonce":                fmt.Sprintf("%v", tx.Nonce),
		"type":                 tx.Type,
		"chainId":              tx.ChainId,
		"v":                    tx.V,
		"r":                    tx.R,
		"s":                    tx.S,
		"cumulativeGasUsed":    tx.CumulativeGasUsed,
		"effectiveGasPrice":    tx.EffectiveGasPrice,
		"status":               tx.Status,
		"block":                block_id,
	}
	docID, err := h.PostToCollection(ctx, constants.CollectionTransaction, txData)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "CreateTransaction", fmt.Sprintf("%v", txData), err)
	}

	return docID, nil
}

func (h *BlockHandler) CreateAccessListEntry(ctx context.Context, accessListEntry *types.AccessListEntry, tx_Id string, blockNumber int64) (string, error) {
	if accessListEntry == nil {
		logger.Sugar.Error("CreateAccessListEntry: AccessListEntry is nil")
		return "", errors.NewInvalidInputFormat("defra", "CreateAccessListEntry", constants.CollectionAccessListEntry, nil)
	}
	if tx_Id == "" {
		logger.Sugar.Error("CreateAccessListEntry: tx_Id is empty")
		return "", errors.NewInvalidInputFormat("defra", "CreateAccessListEntry", "tx_Id", nil)
	}
	ALEData := map[string]interface{}{
		"address":     accessListEntry.Address,
		"blockNumber": blockNumber,
		"storageKeys": accessListEntry.StorageKeys,
		"transaction": tx_Id,
	}
	docID, err := h.PostToCollection(ctx, constants.CollectionAccessListEntry, ALEData)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "CreateAccessListEntry", fmt.Sprintf("%v", ALEData), err)
	}
	return docID, nil
}

func (h *BlockHandler) CreateLog(ctx context.Context, log *types.Log, block_id, tx_Id string) (string, error) {
	blockInt, err := utils.HexToInt(log.BlockNumber)
	if err != nil {
		return "", errors.NewParsingFailed("defra", "CreateLog", fmt.Sprintf("block number: %s", log.BlockNumber), err)
	}
	if log == nil {
		return "", errors.NewInvalidInputFormat("defra", "CreateLog", constants.CollectionLog, nil)
	}
	if block_id == "" {
		return "", errors.NewInvalidInputFormat("defra", "CreateLog", "block_id", nil)
	}
	if tx_Id == "" {
		return "", errors.NewInvalidInputFormat("defra", "CreateLog", "tx_Id", nil)
	}

	logData := map[string]interface{}{
		"address":          log.Address,
		"topics":           log.Topics,
		"data":             log.Data,
		"blockNumber":      blockInt,
		"transactionHash":  log.TransactionHash,
		"transactionIndex": log.TransactionIndex,
		"blockHash":        log.BlockHash,
		"logIndex":         log.LogIndex,
		"removed":          fmt.Sprintf("%v", log.Removed),
		"transaction":      tx_Id,
		"block":            block_id,
	}
	docID, err := h.PostToCollection(ctx, constants.CollectionLog, logData)
	if err != nil {
		logger.Sugar.Errorf("Failed to create log (txHash=%s, logIndex=%v): %v", log.TransactionHash, log.LogIndex, err)
		return "", err
	}

	return docID, nil
}

func (h *BlockHandler) UpdateTransactionRelationships(ctx context.Context, blockId string, txHash string) (string, error) {

	if blockId == "" {
		return "", errors.NewInvalidInputFormat("defra", "UpdateTransactionRelationships", "blockId", nil)
	}
	if txHash == "" {
		return "", errors.NewInvalidInputFormat("defra", "UpdateTransactionRelationships", "txHash", nil)
	}

	// Update transaction with block relationship
	mutation := types.Request{Query: fmt.Sprintf(`mutation {
		update_Transaction(filter: {hash: {_eq: %q}}, input: {block: %q}) {
			_docID
		}
	}`, txHash, blockId)}

	resp, err := h.SendToGraphql(ctx, mutation)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "UpdateTransactionRelationships", fmt.Sprintf("%v", mutation), err)
	}
	docId, err := h.parseGraphQLResponse(resp, "update_Transaction")
	if docId == "" {
		return "", errors.NewQueryFailed("defra", "UpdateTransactionRelationships", fmt.Sprintf("%v", mutation), nil)
	}
	return docId, nil

}

func (h *BlockHandler) UpdateLogRelationships(ctx context.Context, blockId string, txId string, txHash string, logIndex string) (string, error) {

	if blockId == "" {
		return "", errors.NewInvalidInputFormat("defra", "UpdateLogRelationships", "blockId", nil)
	}
	if txId == "" {
		return "", errors.NewInvalidInputFormat("defra", "UpdateLogRelationships", "txId", nil)
	}
	if txHash == "" {
		return "", errors.NewInvalidInputFormat("defra", "UpdateLogRelationships", "txHash", nil)
	}
	if logIndex == "" {
		return "", errors.NewInvalidInputFormat("defra", "UpdateLogRelationships", "logIndex", nil)
	}

	// Update log with block and transaction relationships
	mutation := types.Request{Query: fmt.Sprintf(`mutation {
		update_Log(filter: {logIndex: {_eq: %q}, transactionHash: {_eq: %q}}, input: {
			block: %q,
			transaction: %q
		}) {
			_docID
		}
	}`, logIndex, txHash, blockId, txId)}

	resp, err := h.SendToGraphql(ctx, mutation)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "UpdateLogRelationships", fmt.Sprintf("%v", mutation), err)
	}
	docId, err := h.parseGraphQLResponse(resp, "update_Log")
	if docId == "" {
		return "", errors.NewQueryFailed("defra", "UpdateLogRelationships", fmt.Sprintf("%v", mutation), nil)
	}
	return docId, nil
}

func (h *BlockHandler) PostToCollection(ctx context.Context, collection string, data map[string]interface{}) (string, error) {
	if collection == "" {
		return "", errors.NewInvalidInputFormat("defra", "PostToCollection", "collection", nil)
	}
	if data == nil {
		return "", errors.NewInvalidInputFormat("defra", "PostToCollection", "data", nil)
	}

	// Convert data to GraphQL input format
	var inputFields []string
	for key, value := range data {
		switch v := value.(type) {
		case string:
			inputFields = append(inputFields, fmt.Sprintf("%s: %q", key, v))
		case bool:
			inputFields = append(inputFields, fmt.Sprintf("%s: %v", key, v))
		case int, int64:
			inputFields = append(inputFields, fmt.Sprintf("%s: %d", key, v))
		case []string:
			jsonBytes, err := json.Marshal(v)
			if err != nil {
				logger.Sugar.Errorf("failed to marshal field ", key, "err: ", err)
				return "", errors.NewParsingFailed("defra", "PostToCollection", fmt.Sprintf("%v", key), err)
			}
			inputFields = append(inputFields, fmt.Sprintf("%s: %s", key, string(jsonBytes)))
		default:
			inputFields = append(inputFields, fmt.Sprintf("%s: %q", key, fmt.Sprint(v)))
		}
	}

	// Create mutation
	mutation := types.Request{
		Type: "POST",
		Query: fmt.Sprintf(`mutation {
		create_%s(input: { %s }) {
			_docID
		}
	}`, collection, strings.Join(inputFields, ", "))}

	// Send mutation
	resp, err := h.SendToGraphql(ctx, mutation)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "PostToCollection", fmt.Sprintf("%v", mutation), err)
	}

	// Parse response - handle both single object and array formats
	var rawResponse map[string]interface{}
	if err := json.Unmarshal(resp, &rawResponse); err != nil {
		return "", errors.NewQueryFailed("defra", "PostToCollection", fmt.Sprintf("%v", mutation), err)
	}

	// Check for GraphQL errors first
	if graphqlErrors, hasErrors := rawResponse["errors"].([]interface{}); hasErrors && len(graphqlErrors) > 0 {
		if errorMap, ok := graphqlErrors[0].(map[string]interface{}); ok {
			if message, ok := errorMap["message"].(string); ok {
				// Handle duplicate document error gracefully
				if strings.Contains(message, "already exists") {
					if strings.Contains(message, "DocID: ") {
						parts := strings.Split(message, "DocID: ")
						if len(parts) > 1 {
							docID := strings.TrimSpace(parts[1])
							return docID, nil
						}
					}
					return "", errors.NewQueryFailed("defra", "PostToCollection", "document already exists", nil)
				}
				return "", errors.NewQueryFailed("defra", "PostToCollection", message, nil)
			}
		}
	}

	// Extract data field
	data, ok := rawResponse["data"].(map[string]interface{})
	if !ok {
		return "", errors.NewQueryFailed("defra", "PostToCollection", fmt.Sprintf("%v", mutation), nil)
	}

	// Get document ID
	createField := fmt.Sprintf("create_%s", collection)
	createData, ok := data[createField]
	if !ok {
		return "", errors.NewQueryFailed("defra", "PostToCollection", fmt.Sprintf("%v", mutation), nil)
	}

	// Handle both single object and array responses
	switch v := createData.(type) {
	case map[string]interface{}:
		// Single object response
		if docID, ok := v["_docID"].(string); ok {
			return docID, nil
		}
	case []interface{}:
		// Array response
		if len(v) > 0 {
			if item, ok := v[0].(map[string]interface{}); ok {
				if docID, ok := item["_docID"].(string); ok {
					return docID, nil
				}
			}
		}
	}

	return "", errors.NewQueryFailed("defra", "PostToCollection", fmt.Sprintf("%v", mutation), nil)
}

func (h *BlockHandler) SendToGraphql(ctx context.Context, req types.Request) ([]byte, error) {
	if req.Query == "" {
		return nil, errors.NewInvalidInputFormat("defra", "SendToGraphql", "req.Query", nil)
	}

	if h.defraNode != nil {
		return h.sendToGraphqlDirect(ctx, req)
	}

	return h.sendToGraphqlHTTP(ctx, req)
}

// sendToGraphqlDirect executes GraphQL directly on the embedded DefraDB node
func (h *BlockHandler) sendToGraphqlDirect(ctx context.Context, req types.Request) ([]byte, error) {
	result := h.defraNode.DB.ExecRequest(ctx, req.Query)
	gqlResult := result.GQL

	response := map[string]interface{}{
		"data": gqlResult.Data,
	}

	if len(gqlResult.Errors) > 0 {
		errList := make([]map[string]interface{}, len(gqlResult.Errors))
		for i, err := range gqlResult.Errors {
			errList[i] = map[string]interface{}{
				"message": err.Error(),
			}
		}
		response["errors"] = errList
	}

	respBody, err := json.Marshal(response)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "sendToGraphqlDirect", fmt.Sprintf("%v", req), err)
	}

	return respBody, nil
}

// sendToGraphqlHTTP executes GraphQL via HTTP
func (h *BlockHandler) sendToGraphqlHTTP(ctx context.Context, req types.Request) ([]byte, error) {
	type RequestJSON struct {
		Query string `json:"query"`
	}

	// Create request body
	body := RequestJSON{req.Query}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		logger.Sugar.Errorf("failed to marshal request body: ", err)
	}

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, req.Type, h.defraURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "sendToGraphqlHTTP", fmt.Sprintf("%v", req), err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "sendToGraphqlHTTP", fmt.Sprintf("%v", req), err)
	}

	defer resp.Body.Close()
	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.NewQueryFailed("defra", "sendToGraphqlHTTP", fmt.Sprintf("%v", req), err)
	}

	return respBody, nil
}

// parseGraphQLResponse is a helper function to parse GraphQL responses and extract document IDs
func (h *BlockHandler) parseGraphQLResponse(resp []byte, fieldName string) (string, error) {
	// Parse response
	var response types.Response
	if err := json.Unmarshal(resp, &response); err != nil {
		return "", errors.NewQueryFailed("defra", "parseGraphQLResponse", fmt.Sprintf("%v", resp), err)
	}

	// Get document ID
	items, ok := response.Data[fieldName]
	if !ok {
		return "", errors.NewQueryFailed("defra", "parseGraphQLResponse", fmt.Sprintf("%v", resp), nil)
	}
	if len(items) == 0 {
		return "", errors.NewQueryFailed("defra", "parseGraphQLResponse", fmt.Sprintf("%v", resp), nil)
	}
	return items[0].DocID, nil
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
				logger.Sugar.Debugf("Block %d: batch sig created, %d CIDs (expected ~%d), merkle: %x, verified: %v",
					blockInt, batchSig.CIDCount, expectedDocs, batchSig.MerkleRoot[:8], valid)
			}
		}
	}

	// Commit everything at once (block, txs, logs, ALEs, and BatchSignature)
	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to commit", err)
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
			txHashToID[tx.Hash] = txDoc.ID().String()
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

	return blockID, nil
}

// GetHighestBlockNumber returns the highest block number stored in DefraDB
func (h *BlockHandler) GetHighestBlockNumber(ctx context.Context) (int64, error) {
	query := types.Request{
		Type: "POST",
		Query: `query {` +
			constants.CollectionBlock +
			` (order: {number: DESC}, limit: 1) {
			number
		}	
	}`}

	resp, err := h.SendToGraphql(ctx, query)
	if err != nil {
		return 0, errors.NewQueryFailed("defra", "GetHighestBlockNumber", query.Query, err)
	}
	// Parse response to handle both string and integer number formats
	var rawResponse map[string]interface{}
	if err := json.Unmarshal(resp, &rawResponse); err != nil {
		logger.Sugar.Errorf("failed to decode response: %v", err)
		return 0, errors.NewParsingFailed("defra", "GetHighestBlockNumber", string(resp), err)
	}

	// Extract data field
	data, ok := rawResponse["data"].(map[string]interface{})
	if !ok {
		logger.Sugar.Error("data field not found in response")
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, fmt.Sprintf("%v", data))
	}

	// Extract Block array
	blockArray, ok := data[constants.CollectionBlock].([]interface{})
	if !ok {
		logger.Sugar.Error("Block field not found in response")
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, fmt.Sprintf("%v", data[constants.CollectionBlock]))
	}

	if len(blockArray) == 0 {
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, "blockArray is empty")
	}

	// Extract first block
	block, ok := blockArray[0].(map[string]interface{})
	if !ok {
		logger.Sugar.Error("Invalid block format in response")
		return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, fmt.Sprintf("%v", blockArray))
	}

	// Extract number field (handle both string and integer)
	numberValue := block["number"]
	switch v := numberValue.(type) {
	case string:
		// Try hex conversion first if string starts with 0x
		if strings.HasPrefix(v, "0x") {
			val, err := utils.HexToInt(v)
			if err != nil {
				return 0, errors.NewParsingFailed("defra", "GetHighestBlockNumber", fmt.Sprintf("block number: %s", v), err)
			}
			return val, nil
		}
		if num, err := strconv.ParseInt(v, 10, 64); err == nil {
			return num, nil
		}
		logger.Sugar.Errorf("failed to parse number string: %v", v)
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	default:
		logger.Sugar.Errorf("unexpected number type: %T", numberValue)
	}
	return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, fmt.Sprintf("%v", numberValue))
}
