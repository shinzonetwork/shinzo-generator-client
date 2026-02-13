package defra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-app-sdk/pkg/rustffi"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
)

// batchSessionKeyType is a context key for batch session IDs.
type batchSessionKeyType struct{}

var batchSessionCtxKey = batchSessionKeyType{}

// FFIBlockHandler creates blocks in DefraDB via the Rust FFI client.
type FFIBlockHandler struct {
	client *rustffi.Client
}

// NewFFIBlockHandler creates an FFIBlockHandler wrapping a rustffi.Client.
func NewFFIBlockHandler(client *rustffi.Client) *FFIBlockHandler {
	return &FFIBlockHandler{client: client}
}

// newBatchSessionID generates a random 16-byte hex session ID.
func newBatchSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateBlockBatch creates a block with all its transactions, logs, and access list entries
// via GraphQL mutations through the Rust FFI. It uses batch signing to produce a
// BatchSignature document covering all CIDs.
func (h *FFIBlockHandler) CreateBlockBatch(ctx context.Context, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) (string, error) {
	if block == nil {
		return "", errors.NewInvalidBlockFormat("defra", "FFIBlockHandler.CreateBlockBatch", "nil", nil)
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

	// Start a batch session for signing
	sessionID := newBatchSessionID()
	if err := h.client.BatchStart(sessionID); err != nil {
		return "", fmt.Errorf("failed to start batch session: %w", err)
	}
	ctx = context.WithValue(ctx, batchSessionCtxKey, sessionID)

	// Create block
	blockMutation := h.buildBlockMutation(block, blockInt)
	blockID, err := h.execMutation(ctx, blockMutation)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return "", fmt.Errorf("block already exists")
		}
		return "", fmt.Errorf("failed to create block: %w", err)
	}

	// Create transactions
	txHashToID := make(map[string]string)
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txMutation := h.buildTransactionMutation(tx, blockID)
		txDocID, err := h.execMutation(ctx, txMutation)
		if err != nil {
			logger.Sugar.Warnf("Failed to create transaction %s: %v", tx.Hash, err)
			continue
		}
		txHashToID[tx.Hash] = txDocID
	}

	// Create logs
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
			logMutation := h.buildLogMutation(&receipt.Logs[i], blockID, txID)
			if _, err := h.execMutation(ctx, logMutation); err != nil {
				logger.Sugar.Warnf("Failed to create log for tx %s index %d: %v", tx.Hash, i, err)
			}
		}
	}

	// Create access list entries
	for _, tx := range transactions {
		if tx == nil {
			continue
		}
		txID, ok := txHashToID[tx.Hash]
		if !ok {
			continue
		}
		for i := range tx.AccessList {
			aleMutation := h.buildALEMutation(&tx.AccessList[i], txID, blockInt)
			if _, err := h.execMutation(ctx, aleMutation); err != nil {
				logger.Sugar.Warnf("Failed to create ALE for tx %s index %d: %v", tx.Hash, i, err)
			}
		}
	}

	// Sign the batch and create BatchSignature document
	batchSigJSON, err := h.client.BatchSign(sessionID)
	if err != nil {
		logger.Sugar.Warnf("Failed to sign batch for block %d: %v", blockInt, err)
	} else if batchSigJSON != "" {
		batchSigMutation, parseErr := h.buildBatchSignatureMutationFromJSON(batchSigJSON, block.Hash, blockInt)
		if parseErr != nil {
			logger.Sugar.Warnf("Failed to build batch signature mutation for block %d: %v", blockInt, parseErr)
		} else {
			// Execute batch signature mutation without the batch session
			// (BatchSignature is metadata about the batch, not part of the signed content)
			plainCtx := context.Background()
			if _, err := h.execMutation(plainCtx, batchSigMutation); err != nil {
				logger.Sugar.Warnf("Failed to create batch signature doc for block %d: %v", blockInt, err)
			} else {
				logger.Sugar.Debugf("Block %d: FFI batch signature created", blockInt)
			}
		}
	}

	return blockID, nil
}

// GetHighestBlockNumber queries DefraDB for the highest block number.
func (h *FFIBlockHandler) GetHighestBlockNumber(ctx context.Context) (int64, error) {
	query := fmt.Sprintf(`query { %s(order: {number: DESC}, limit: 1) { number } }`, constants.CollectionBlock)
	resp, err := h.client.Query(query)
	if err != nil {
		return 0, errors.NewQueryFailed("defra", "FFIBlockHandler.GetHighestBlockNumber", query, err)
	}

	return parseBlockNumberFromResponse(resp)
}

// execMutation executes a GraphQL mutation, optionally within a batch session.
func (h *FFIBlockHandler) execMutation(ctx context.Context, mutation string) (string, error) {
	sessionID, _ := ctx.Value(batchSessionCtxKey).(string)
	var resp string
	var err error
	if sessionID != "" {
		resp, err = h.client.QueryWithBatch(mutation, sessionID)
	} else {
		resp, err = h.client.Query(mutation)
	}
	if err != nil {
		return "", err
	}

	return extractDocID(resp)
}

// extractDocID parses a GraphQL mutation response to extract the _docID field.
// Expected format: {"data":{"create_CollectionName":[{"_docID":"..."}]}}
func extractDocID(resp string) (string, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return "", fmt.Errorf("failed to parse mutation response: %w", err)
	}

	data, ok := parsed["data"].(map[string]interface{})
	if !ok {
		// Check for errors
		if errs, ok := parsed["errors"]; ok {
			return "", fmt.Errorf("GraphQL error: %v", errs)
		}
		return "", fmt.Errorf("unexpected response format: no data field")
	}

	// Find the first key in data (e.g., "create_Ethereum__Mainnet__Block")
	for _, v := range data {
		switch arr := v.(type) {
		case []interface{}:
			if len(arr) > 0 {
				if doc, ok := arr[0].(map[string]interface{}); ok {
					if docID, ok := doc["_docID"].(string); ok {
						return docID, nil
					}
				}
			}
		case map[string]interface{}:
			if docID, ok := arr["_docID"].(string); ok {
				return docID, nil
			}
		}
	}

	return "", fmt.Errorf("no _docID found in response: %s", resp)
}

// parseBlockNumberFromResponse extracts the block number from a query response.
func parseBlockNumberFromResponse(resp string) (int64, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return 0, fmt.Errorf("failed to parse query response: %w", err)
	}

	data, ok := parsed["data"].(map[string]interface{})
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", "parseBlockNumberFromResponse", constants.CollectionBlock, "no data")
	}

	blockArray, ok := data[constants.CollectionBlock].([]interface{})
	if !ok || len(blockArray) == 0 {
		return 0, errors.NewDocumentNotFound("defra", "parseBlockNumberFromResponse", constants.CollectionBlock, "no blocks")
	}

	block, ok := blockArray[0].(map[string]interface{})
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", "parseBlockNumberFromResponse", constants.CollectionBlock, "invalid format")
	}

	switch v := block["number"].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case json.Number:
		return v.Int64()
	}

	return 0, errors.NewDocumentNotFound("defra", "parseBlockNumberFromResponse", constants.CollectionBlock, "invalid number type")
}

// escapeGraphQL escapes a string for use in a GraphQL string literal.
func escapeGraphQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// buildBlockMutation builds a GraphQL create mutation for a block.
func (h *FFIBlockHandler) buildBlockMutation(block *types.Block, blockInt int64) string {
	return fmt.Sprintf(`mutation { create_%s(input: {hash: "%s", number: %d, timestamp: "%s", parentHash: "%s", difficulty: "%s", totalDifficulty: "%s", gasUsed: "%s", gasLimit: "%s", baseFeePerGas: "%s", nonce: "%s", miner: "%s", size: "%s", stateRoot: "%s", sha3Uncles: "%s", transactionsRoot: "%s", receiptsRoot: "%s", logsBloom: "%s", extraData: "%s", mixHash: "%s"}) { _docID } }`,
		constants.CollectionBlock,
		escapeGraphQL(block.Hash),
		blockInt,
		escapeGraphQL(block.Timestamp),
		escapeGraphQL(block.ParentHash),
		escapeGraphQL(block.Difficulty),
		escapeGraphQL(block.TotalDifficulty),
		escapeGraphQL(block.GasUsed),
		escapeGraphQL(block.GasLimit),
		escapeGraphQL(block.BaseFeePerGas),
		escapeGraphQL(block.Nonce),
		escapeGraphQL(block.Miner),
		escapeGraphQL(block.Size),
		escapeGraphQL(block.StateRoot),
		escapeGraphQL(block.Sha3Uncles),
		escapeGraphQL(block.TransactionsRoot),
		escapeGraphQL(block.ReceiptsRoot),
		escapeGraphQL(block.LogsBloom),
		escapeGraphQL(block.ExtraData),
		escapeGraphQL(block.MixHash),
	)
}

// buildTransactionMutation builds a GraphQL create mutation for a transaction.
func (h *FFIBlockHandler) buildTransactionMutation(tx *types.Transaction, blockID string) string {
	txBlockNum, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
	return fmt.Sprintf(`mutation { create_%s(input: {hash: "%s", blockHash: "%s", blockNumber: %d, from: "%s", to: "%s", value: "%s", gas: "%s", gasPrice: "%s", gasUsed: "%s", maxFeePerGas: "%s", maxPriorityFeePerGas: "%s", input: "%s", nonce: "%s", transactionIndex: %d, type: "%s", chainId: "%s", v: "%s", r: "%s", s: "%s", status: %t, cumulativeGasUsed: "%s", effectiveGasPrice: "%s", block_id: "%s"}) { _docID } }`,
		constants.CollectionTransaction,
		escapeGraphQL(tx.Hash),
		escapeGraphQL(tx.BlockHash),
		txBlockNum,
		escapeGraphQL(tx.From),
		escapeGraphQL(tx.To),
		escapeGraphQL(tx.Value),
		escapeGraphQL(tx.Gas),
		escapeGraphQL(tx.GasPrice),
		escapeGraphQL(tx.GasUsed),
		escapeGraphQL(tx.MaxFeePerGas),
		escapeGraphQL(tx.MaxPriorityFeePerGas),
		escapeGraphQL(string(tx.Input)),
		escapeGraphQL(tx.Nonce),
		tx.TransactionIndex,
		escapeGraphQL(tx.Type),
		escapeGraphQL(tx.ChainId),
		escapeGraphQL(tx.V),
		escapeGraphQL(tx.R),
		escapeGraphQL(tx.S),
		tx.Status,
		escapeGraphQL(tx.CumulativeGasUsed),
		escapeGraphQL(tx.EffectiveGasPrice),
		blockID,
	)
}

// buildLogMutation builds a GraphQL create mutation for a log entry.
func (h *FFIBlockHandler) buildLogMutation(log *types.Log, blockID, txID string) string {
	logBlockNum, _ := utils.HexToInt(log.BlockNumber)
	topicsGraphQL := formatStringArrayForGraphQL(log.Topics)
	return fmt.Sprintf(`mutation { create_%s(input: {address: "%s", topics: %s, data: "%s", blockNumber: %d, transactionHash: "%s", transactionIndex: %d, blockHash: "%s", logIndex: %d, removed: "%v", block_id: "%s", transaction_id: "%s"}) { _docID } }`,
		constants.CollectionLog,
		escapeGraphQL(log.Address),
		topicsGraphQL,
		escapeGraphQL(log.Data),
		logBlockNum,
		escapeGraphQL(log.TransactionHash),
		log.TransactionIndex,
		escapeGraphQL(log.BlockHash),
		log.LogIndex,
		log.Removed,
		blockID,
		txID,
	)
}

// buildALEMutation builds a GraphQL create mutation for an access list entry.
func (h *FFIBlockHandler) buildALEMutation(ale *types.AccessListEntry, txID string, blockNumber int64) string {
	storageKeysGraphQL := formatStringArrayForGraphQL(ale.StorageKeys)
	return fmt.Sprintf(`mutation { create_%s(input: {address: "%s", blockNumber: %d, storageKeys: %s, transaction_id: "%s"}) { _docID } }`,
		constants.CollectionAccessListEntry,
		escapeGraphQL(ale.Address),
		blockNumber,
		storageKeysGraphQL,
		txID,
	)
}

// buildBatchSignatureMutationFromJSON parses the batch signature JSON from the FFI
// and builds a GraphQL create mutation for the BatchSignature document.
func (h *FFIBlockHandler) buildBatchSignatureMutationFromJSON(batchSigJSON, blockHash string, blockNumber int64) (string, error) {
	var sig struct {
		MerkleRoot string `json:"merkle_root"`
		CIDCount   int    `json:"cid_count"`
		Header     struct {
			Type     string `json:"type"`
			Identity string `json:"identity"`
		} `json:"header"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(batchSigJSON), &sig); err != nil {
		return "", fmt.Errorf("failed to parse batch signature JSON: %w", err)
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)

	mutation := fmt.Sprintf(`mutation { create_%s(input: {blockNumber: %d, blockHash: "%s", merkleRoot: "%s", cidCount: %d, signatureType: "%s", signatureIdentity: "%s", signatureValue: "%s", createdAt: "%s"}) { _docID } }`,
		constants.CollectionBatchSignature,
		blockNumber,
		escapeGraphQL(blockHash),
		escapeGraphQL(sig.MerkleRoot),
		sig.CIDCount,
		escapeGraphQL(sig.Header.Type),
		escapeGraphQL(sig.Header.Identity),
		escapeGraphQL(sig.Value),
		createdAt,
	)
	return mutation, nil
}

// formatStringArrayForGraphQL formats a string slice as a GraphQL array literal.
// e.g., ["a", "b"] becomes ["a", "b"]
func formatStringArrayForGraphQL(arr []string) string {
	if len(arr) == 0 {
		return "[]"
	}
	parts := make([]string, len(arr))
	for i, s := range arr {
		parts[i] = fmt.Sprintf(`"%s"`, escapeGraphQL(s))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
