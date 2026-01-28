package defra

import (
	"bytes"
	"context"
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
// maxDocsPerTxn is the threshold for single-txn vs batched block creation (default 256 if <= 0).
func NewBlockHandlerWithNode(defraNode *node.Node, maxDocsPerTxn ...int) (*BlockHandler, error) {
	if defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "NewBlockHandlerWithNode",
			"defraNode is nil", "", nil)
	}
	maxDocs := 256
	if len(maxDocsPerTxn) > 0 && maxDocsPerTxn[0] > 0 {
		maxDocs = maxDocsPerTxn[0]
	}
	return &BlockHandler{
		defraNode:     defraNode,
		client:        nil,
		defraURL:      "",
		maxDocsPerTxn: maxDocs,
	}, nil
}

func (h *BlockHandler) CreateBlock(ctx context.Context, block *types.Block) (string, error) {
	// Input validation
	if block == nil {
		return "", errors.NewInvalidBlockFormat("defra", "CreateBlock", fmt.Sprintf("%v", block), nil)
	}

	// Create block data matching new Arbitrum schema
	blockData := map[string]interface{}{
		"baseFeePerGas": block.BaseFeePerGas,
		"difficulty":    block.Difficulty,
		"extraData":     block.ExtraData,
		"gasLimit":      block.GasLimit,
		"gasUsed":       block.GasUsed,
		"hash":          block.Hash,
		"l1BlockNumber": block.L1BlockNumber,
		"logsBloom":     block.LogsBloom,
		"mixHash":       block.MixHash,
		"nonce":         block.Nonce,
		"number":        block.Number, // int type
		"parentHash":    block.ParentHash,
		"receiptsRoot":  block.ReceiptsRoot,
		"sendCount":     block.SendCount,
		"sendRoot":      block.SendRoot,
		"sha3Uncles":    block.Sha3Uncles,
		"size":          block.Size,
		"stateRoot":     block.StateRoot,
		"timestamp":     block.Timestamp,
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

	// Create transaction data matching new Arbitrum schema
	txData := map[string]interface{}{
		// Transaction fields
		"blockHash":        tx.BlockHash,
		"blockNumber":      tx.BlockNumber, // int type
		"from":             tx.From,
		"gas":              tx.Gas,
		"gasPrice":         tx.GasPrice,
		"hash":             tx.Hash,
		"input":            tx.Input,
		"nonce":            tx.Nonce,
		"to":               tx.To,
		"transactionIndex": tx.TransactionIndex,
		"value":            tx.Value,
		"type":             tx.Type,
		"chainId":          tx.ChainId,
		"v":                tx.V,
		"r":                tx.R,
		"s":                tx.S,
		// Receipt fields
		"contractAddress":   tx.ContractAddress,
		"cumulativeGasUsed": tx.CumulativeGasUsed,
		"effectiveGasPrice": tx.EffectiveGasPrice,
		"gasUsed":           tx.GasUsed,
		"gasUsedForL1":      tx.GasUsedForL1,
		"l1BlockNumber":     tx.L1BlockNumber,
		"status":            tx.Status,
		"timeboosted":       tx.Timeboosted,
		"logsBloom":         tx.LogsBloom,
		"block":             block_id, // Relationship field matches schema
	}
	docID, err := h.PostToCollection(ctx, constants.CollectionTransaction, txData)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "CreateTransaction", fmt.Sprintf("%v", txData), err)
	}

	return docID, nil
}

func (h *BlockHandler) CreateLog(ctx context.Context, log *types.Log, block_id, tx_Id string) (string, error) {
	if log == nil {
		return "", errors.NewInvalidInputFormat("defra", "CreateLog", constants.CollectionLog, nil)
	}
	if tx_Id == "" {
		return "", errors.NewInvalidInputFormat("defra", "CreateLog", "tx_Id", nil)
	}

	// Create log data matching new Arbitrum schema
	logData := map[string]interface{}{
		"address":          log.Address,
		"topics":           log.Topics,
		"data":             log.Data,
		"blockNumber":      log.BlockNumber, // int type
		"transactionHash":  log.TransactionHash,
		"transactionIndex": log.TransactionIndex,
		"blockHash":        log.BlockHash,
		"logIndex":         log.LogIndex,
		"removed":          log.Removed,      // bool type
		"transaction":      tx_Id,            // Relationship field matches schema
	}
	docID, err := h.PostToCollection(ctx, constants.CollectionLog, logData)
	if err != nil {
		logger.Sugar.Errorf("Failed to create log (txHash=%s, logIndex=%v): %v", log.TransactionHash, log.LogIndex, err)
		return "", err
	}

	return docID, nil
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
	respData, ok := rawResponse["data"].(map[string]interface{})
	if !ok {
		return "", errors.NewQueryFailed("defra", "PostToCollection", fmt.Sprintf("%v", mutation), nil)
	}

	// Get document ID
	createField := fmt.Sprintf("create_%s", collection)
	createData, ok := respData[createField]
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

// CreateBlockBatch creates a block with all its transactions and logs.
// Updated for new Arbitrum schema (no AccessListEntry)
func (h *BlockHandler) CreateBlockBatch(ctx context.Context, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) (string, error) {
	if h.defraNode == nil {
		return "", errors.NewConfigurationError("defra", "CreateBlockBatch",
			"batch creation requires embedded DefraDB node", "", nil)
	}

	if block == nil {
		return "", errors.NewInvalidBlockFormat("defra", "CreateBlockBatch", "nil", nil)
	}

	// block.Number is already an int in the new schema
	blockInt := int64(block.Number)

	receiptMap := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			receiptMap[receipt.TransactionHash] = receipt
		}
	}

	totalLogs := 0
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		if receipt, ok := receiptMap[tx.Hash]; ok && receipt != nil {
			totalLogs += len(receipt.Logs)
		}
	}
	totalDocs := 1 + len(transactions) + totalLogs

	if totalDocs <= h.maxDocsPerTxn {
		return h.createBlockSingleTransaction(ctx, block, blockInt, transactions, receipts, receiptMap, totalDocs)
	}

	return h.createBlockBatched(ctx, block, blockInt, transactions, receipts, receiptMap)
}

// createBlockSingleTransaction creates the entire block in a single DB transaction.
// This is optimal for small-to-medium blocks as it minimizes commit overhead.
func (h *BlockHandler) createBlockSingleTransaction(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receipts []*types.TransactionReceipt, receiptMap map[string]*types.TransactionReceipt, totalDocs int) (string, error) {
	// Start single transaction for everything
	txn, err := h.defraNode.DB.NewTxn(false)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to create transaction", err)
	}

	blockMutation := h.buildBlockMutation(block, blockInt)
	result := txn.ExecRequest(ctx, blockMutation)
	if len(result.GQL.Errors) > 0 {
		txn.Discard()
		errMsg := result.GQL.Errors[0].Error()
		if strings.Contains(errMsg, "already exists") {
			return "", fmt.Errorf("block already exists")
		}
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", errMsg, result.GQL.Errors[0])
	}

	blockID, err := h.extractDocID(result.GQL.Data, "create_"+constants.CollectionBlock)
	if err != nil || blockID == "" {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to get block ID", err)
	}

	txHashToID := make(map[string]string)
	if len(transactions) > 0 {
		txMutation, txInfos := h.buildBatchedTransactionMutation(transactions, blockID, 0)
		if txMutation != "" {
			result = txn.ExecRequest(ctx, txMutation)
			if len(result.GQL.Errors) > 0 {
				txn.Discard()
				return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", result.GQL.Errors[0].Error(), result.GQL.Errors[0])
			}

			for _, txInfo := range txInfos {
				docID := h.extractDocIDFromBatchedResponse(result.GQL.Data, txInfo.alias)
				if docID != "" {
					txHashToID[txInfo.hash] = docID
				}
			}
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

	if len(allLogs) > 0 {
		logMutation := h.buildBatchedLogMutation(allLogs, blockID, 0)
		if logMutation != "" {
			result = txn.ExecRequest(ctx, logMutation)
			if len(result.GQL.Errors) > 0 {
				txn.Discard()
				return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", result.GQL.Errors[0].Error(), result.GQL.Errors[0])
			}
		}
	}

	// Commit everything at once
	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockSingleTransaction", "failed to commit", err)
	}

	return blockID, nil
}

// createBlockBatched creates the block using multiple transactions for large blocks.
// This is the fallback for blocks exceeding MaxDocsPerTransaction.
func (h *BlockHandler) createBlockBatched(ctx context.Context, block *types.Block, blockInt int64, transactions []*types.Transaction, receipts []*types.TransactionReceipt, receiptMap map[string]*types.TransactionReceipt) (string, error) {
	txn, err := h.defraNode.DB.NewTxn(false)
	if err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to create transaction", err)
	}

	blockMutation := h.buildBlockMutation(block, blockInt)
	result := txn.ExecRequest(ctx, blockMutation)
	if len(result.GQL.Errors) > 0 {
		txn.Discard()
		errMsg := result.GQL.Errors[0].Error()
		return "", errors.NewQueryFailed("defra", "createBlockBatched", errMsg, result.GQL.Errors[0])
	}

	blockID, err := h.extractDocID(result.GQL.Data, "create_"+constants.CollectionBlock)
	if err != nil || blockID == "" {
		txn.Discard()
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to get block ID", err)
	}

	if err := txn.Commit(); err != nil {
		return "", errors.NewQueryFailed("defra", "createBlockBatched", "failed to commit block", err)
	}

	batchSize := 64 // Batch size for large blocks that exceed single-txn threshold
	txHashToID := make(map[string]string)
	txCount := 0

	for i := 0; i < len(transactions); i += batchSize {
		end := i + batchSize
		if end > len(transactions) {
			end = len(transactions)
		}

		batch := transactions[i:end]
		if len(batch) == 0 {
			continue
		}

		batchedMutation, txInfos := h.buildBatchedTransactionMutation(batch, blockID, i)
		if batchedMutation == "" {
			continue
		}

		txn, err = h.defraNode.DB.NewTxn(false)
		if err != nil {
			logger.Sugar.Warnf("Failed to create txn for tx batch: %v", err)
			continue
		}

		result := txn.ExecRequest(ctx, batchedMutation)
		if len(result.GQL.Errors) > 0 {
			txn.Discard()
			logger.Sugar.Warnf("Batch tx mutation error: %v", result.GQL.Errors[0])
			continue
		}

		if err := txn.Commit(); err != nil {
			logger.Sugar.Warnf("Failed to commit tx batch: %v", err)
			continue
		}

		for _, txInfo := range txInfos {
			docID := h.extractDocIDFromBatchedResponse(result.GQL.Data, txInfo.alias)
			if docID != "" {
				txHashToID[txInfo.hash] = docID
				txCount++
			}
		}
	}

	// Phase 3: Create Logs in batches
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

	logCount := 0
	for i := 0; i < len(allLogs); i += batchSize {
		end := i + batchSize
		if end > len(allLogs) {
			end = len(allLogs)
		}

		batch := allLogs[i:end]
		if len(batch) == 0 {
			continue
		}

		batchedMutation := h.buildBatchedLogMutation(batch, blockID, i)
		if batchedMutation == "" {
			continue
		}

		txn, err = h.defraNode.DB.NewTxn(false)
		if err != nil {
			logger.Sugar.Warnf("Failed to create txn for log batch: %v", err)
			continue
		}

		result := txn.ExecRequest(ctx, batchedMutation)
		if len(result.GQL.Errors) > 0 {
			txn.Discard()
			logger.Sugar.Warnf("Batch log mutation error: %v", result.GQL.Errors[0])
			continue
		}

		if err := txn.Commit(); err != nil {
			logger.Sugar.Warnf("Failed to commit log batch: %v", err)
			continue
		}

		logCount += len(batch)
	}

	return blockID, nil
}

// txAliasInfo holds the alias and hash for a transaction in a batched mutation
type txAliasInfo struct {
	alias string
	hash  string
}

// buildBatchedTransactionMutation creates a single GraphQL mutation for multiple transactions
// Updated for new Arbitrum schema
func (h *BlockHandler) buildBatchedTransactionMutation(txs []*types.Transaction, blockID string, startIdx int) (string, []txAliasInfo) {
	var sb strings.Builder
	sb.Grow(len(txs) * 1536)
	sb.WriteString("mutation {\n")

	var txInfos []txAliasInfo
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		alias := fmt.Sprintf("tx%d", startIdx+i)
		txInfos = append(txInfos, txAliasInfo{alias: alias, hash: tx.Hash})

		sb.WriteString(alias)
		sb.WriteString(`: create_`)
		sb.WriteString(constants.CollectionTransaction)
		sb.WriteString(`(input: { hash: "`)
		sb.WriteString(tx.Hash)
		sb.WriteString(`", blockNumber: `)
		sb.WriteString(strconv.Itoa(tx.BlockNumber)) // int type now
		sb.WriteString(`, blockHash: "`)
		sb.WriteString(tx.BlockHash)
		sb.WriteString(`", transactionIndex: `)
		sb.WriteString(strconv.Itoa(tx.TransactionIndex))
		sb.WriteString(`, from: "`)
		sb.WriteString(tx.From)
		sb.WriteString(`", to: "`)
		sb.WriteString(tx.To)
		sb.WriteString(`", value: "`)
		sb.WriteString(tx.Value)
		sb.WriteString(`", gas: "`)
		sb.WriteString(tx.Gas)
		sb.WriteString(`", gasPrice: "`)
		sb.WriteString(tx.GasPrice)
		sb.WriteString(`", input: "`)
		sb.WriteString(tx.Input)
		sb.WriteString(`", nonce: "`)
		sb.WriteString(tx.Nonce)
		sb.WriteString(`", type: "`)
		sb.WriteString(tx.Type)
		sb.WriteString(`", chainId: "`)
		sb.WriteString(tx.ChainId)
		sb.WriteString(`", v: "`)
		sb.WriteString(tx.V)
		sb.WriteString(`", r: "`)
		sb.WriteString(tx.R)
		sb.WriteString(`", s: "`)
		sb.WriteString(tx.S)
		sb.WriteString(`", contractAddress: "`)
		sb.WriteString(tx.ContractAddress)
		sb.WriteString(`", cumulativeGasUsed: "`)
		sb.WriteString(tx.CumulativeGasUsed)
		sb.WriteString(`", effectiveGasPrice: "`)
		sb.WriteString(tx.EffectiveGasPrice)
		sb.WriteString(`", gasUsed: "`)
		sb.WriteString(tx.GasUsed)
		sb.WriteString(`", gasUsedForL1: "`)
		sb.WriteString(tx.GasUsedForL1)
		sb.WriteString(`", l1BlockNumber: "`)
		sb.WriteString(tx.L1BlockNumber)
		sb.WriteString(`", status: "`)
		sb.WriteString(tx.Status) // string type now
		sb.WriteString(`", timeboosted: `)
		sb.WriteString(strconv.FormatBool(tx.Timeboosted))
		sb.WriteString(`, logsBloom: "`)
		sb.WriteString(tx.LogsBloom)
		sb.WriteString(`", block: "`)
		sb.WriteString(blockID)
		sb.WriteString(`" }) { _docID }`)
		sb.WriteString("\n")
	}

	sb.WriteString("}")

	if len(txInfos) == 0 {
		return "", nil
	}
	return sb.String(), txInfos
}

// buildBatchedLogMutation creates a single GraphQL mutation for multiple logs
// Updated for new Arbitrum schema
func (h *BlockHandler) buildBatchedLogMutation(logs []logEntry, blockID string, startIdx int) string {
	var sb strings.Builder
	sb.Grow(len(logs) * 1024)
	sb.WriteString("mutation {\n")

	count := 0
	for i, entry := range logs {
		if entry.log == nil {
			continue
		}
		alias := fmt.Sprintf("log%d", startIdx+i)

		sb.WriteString(alias)
		sb.WriteString(`: create_`)
		sb.WriteString(constants.CollectionLog)
		sb.WriteString(`(input: { address: "`)
		sb.WriteString(entry.log.Address)
		sb.WriteString(`", topics: `)
		sb.WriteString(h.formatStringArray(entry.log.Topics))
		sb.WriteString(`, data: "`)
		sb.WriteString(entry.log.Data)
		sb.WriteString(`", blockNumber: `)
		sb.WriteString(strconv.Itoa(entry.log.BlockNumber)) // int type now
		sb.WriteString(`, transactionHash: "`)
		sb.WriteString(entry.log.TransactionHash)
		sb.WriteString(`", transactionIndex: `)
		sb.WriteString(strconv.Itoa(entry.log.TransactionIndex))
		sb.WriteString(`, blockHash: "`)
		sb.WriteString(entry.log.BlockHash)
		sb.WriteString(`", logIndex: `)
		sb.WriteString(strconv.Itoa(entry.log.LogIndex))
		sb.WriteString(`, removed: `)
		sb.WriteString(strconv.FormatBool(entry.log.Removed)) // bool type now
		sb.WriteString(`, transaction: "`)
		sb.WriteString(entry.txID)
		sb.WriteString(`" }) { _docID }`)
		sb.WriteString("\n")
		count++
	}

	sb.WriteString("}")

	if count == 0 {
		return ""
	}
	return sb.String()
}

// extractDocIDFromBatchedResponse extracts a doc ID from a batched mutation response by alias
func (h *BlockHandler) extractDocIDFromBatchedResponse(data any, alias string) string {
	dataMap, ok := data.(map[string]any)
	if !ok {
		return ""
	}

	aliasData, ok := dataMap[alias]
	if !ok {
		return ""
	}

	switch v := aliasData.(type) {
	case map[string]any:
		if docID, ok := v["_docID"].(string); ok {
			return docID
		}
	case []map[string]interface{}:
		// DefraDB returns this type for batched mutations
		if len(v) > 0 {
			if docID, ok := v[0]["_docID"].(string); ok {
				return docID
			}
		}
	case []any:
		if len(v) > 0 {
			if item, ok := v[0].(map[string]any); ok {
				if docID, ok := item["_docID"].(string); ok {
					return docID
				}
			}
		}
	}
	return ""
}

// buildBlockMutation creates a GraphQL mutation for a block using strings.Builder for efficiency
// Updated for new Arbitrum schema
func (h *BlockHandler) buildBlockMutation(block *types.Block, blockInt int64) string {
	var sb strings.Builder
	sb.Grow(2048) // Pre-allocate for typical block mutation size

	sb.WriteString(`mutation { create_`)
	sb.WriteString(constants.CollectionBlock)
	sb.WriteString(`(input: { hash: "`)
	sb.WriteString(block.Hash)
	sb.WriteString(`", number: `)
	sb.WriteString(strconv.FormatInt(blockInt, 10))
	sb.WriteString(`, timestamp: "`)
	sb.WriteString(block.Timestamp)
	sb.WriteString(`", parentHash: "`)
	sb.WriteString(block.ParentHash)
	sb.WriteString(`", difficulty: "`)
	sb.WriteString(block.Difficulty)
	sb.WriteString(`", gasUsed: "`)
	sb.WriteString(block.GasUsed)
	sb.WriteString(`", gasLimit: "`)
	sb.WriteString(block.GasLimit)
	sb.WriteString(`", baseFeePerGas: "`)
	sb.WriteString(block.BaseFeePerGas)
	sb.WriteString(`", nonce: "`)
	sb.WriteString(block.Nonce)
	sb.WriteString(`", size: "`)
	sb.WriteString(block.Size)
	sb.WriteString(`", stateRoot: "`)
	sb.WriteString(block.StateRoot)
	sb.WriteString(`", sha3Uncles: "`)
	sb.WriteString(block.Sha3Uncles)
	sb.WriteString(`", receiptsRoot: "`)
	sb.WriteString(block.ReceiptsRoot)
	sb.WriteString(`", logsBloom: "`)
	sb.WriteString(block.LogsBloom)
	sb.WriteString(`", extraData: "`)
	sb.WriteString(block.ExtraData)
	sb.WriteString(`", mixHash: "`)
	sb.WriteString(block.MixHash)
	// Arbitrum-specific fields
	sb.WriteString(`", l1BlockNumber: "`)
	sb.WriteString(block.L1BlockNumber)
	sb.WriteString(`", sendCount: "`)
	sb.WriteString(block.SendCount)
	sb.WriteString(`", sendRoot: "`)
	sb.WriteString(block.SendRoot)
	sb.WriteString(`" }) { _docID } }`)

	return sb.String()
}

// formatStringArray formats a string slice as a GraphQL array
func (h *BlockHandler) formatStringArray(arr []string) string {
	if len(arr) == 0 {
		return "[]"
	}
	jsonBytes, _ := json.Marshal(arr)
	return string(jsonBytes)
}

// extractDocID extracts the document ID from a GraphQL response
func (h *BlockHandler) extractDocID(data any, fieldName string) (string, error) {
	if data == nil {
		return "", fmt.Errorf("nil data")
	}

	dataMap, ok := data.(map[string]any)
	if !ok {
		return "", fmt.Errorf("data is not a map")
	}

	field, ok := dataMap[fieldName]
	if !ok {
		return "", fmt.Errorf("field %s not found", fieldName)
	}

	switch v := field.(type) {
	case []any:
		if len(v) > 0 {
			if item, ok := v[0].(map[string]any); ok {
				if docID, ok := item["_docID"].(string); ok {
					return docID, nil
				}
			}
		}
	case []map[string]any:
		if len(v) > 0 {
			if docID, ok := v[0]["_docID"].(string); ok {
				return docID, nil
			}
		}
	case map[string]any:
		if docID, ok := v["_docID"].(string); ok {
			return docID, nil
		}
	}

	return "", fmt.Errorf("could not extract docID from %v", field)
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

	// Extract number field (handle both string and integer since schema now uses int)
	numberValue := block["number"]
	switch v := numberValue.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		// Fallback for string parsing
		if num, err := strconv.ParseInt(v, 10, 64); err == nil {
			return num, nil
		}
		logger.Sugar.Errorf("failed to parse number string: %v", v)
	default:
		logger.Sugar.Errorf("unexpected number type: %T", numberValue)
	}
	return 0, errors.NewDocumentNotFound("defra", "GetHighestBlockNumber", constants.CollectionBlock, fmt.Sprintf("%v", numberValue))
}