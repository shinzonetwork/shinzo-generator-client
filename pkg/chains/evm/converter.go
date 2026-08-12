package evm

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
	"github.com/sourcenetwork/defradb/node"
)

// errBlockNumberCorrupt indicates that a block document exists in the store
// but its "number" field is missing or has an unparseable type.
var errBlockNumberCorrupt = fmt.Errorf("block exists but has invalid or unparseable number field")

// Converter is the EVM chain-specific knowledge layer. It implements
// chains.Converter by transforming raw block data (BlockBundle) into
// []DocumentGroup, generating the chain schema, and answering progress
// queries against the local DefraDB instance.
//
// Converter never stores a *node.Node — it receives one explicitly on each
// progress-query call, keeping it stateless and testable without a live DB.
//
// Coexistence (Phase C): the original build helpers were moved here from
// pkg/defra/block_handler.go. BlockHandler.Store (Phase D) consumes the
// DocumentGroups produced by this converter. Phase G removes any
// remaining duplicates.
type Converter struct {
	collections *CollectionNames
	cfg         *config.Config
}

// Compile-time guarantee that Converter implements chains.Converter.
var _ chains.Converter = (*Converter)(nil)

// NewConverter creates a Converter from the given config, deriving the
// collection prefix via chainPrefixFromConfig. A nil config uses defaults
// (Ethereum__Mainnet), matching chainPrefixFromConfig behaviour.
func NewConverter(cfg *config.Config) *Converter {
	prefix := chainPrefixFromConfig(cfg)
	return &Converter{
		collections: NewCollectionNames(prefix),
		cfg:         cfg,
	}
}

// Convert implements chains.Converter. It type-asserts rawBlock to *BlockBundle
// and builds DocumentGroups for block, transactions, logs, and access list
// entries. The data maps contain only field values — cross-document link fields
// (_blockID, _transactionID) are NOT set here; they are resolved by
// BlockHandler.Store (Phase D) after AddDocument assigns persistent docIDs.
//
// The vp (CollectionVersionProvider) parameter is retained in the interface
// signature for future use but is not needed by the current implementation.
//
// The signature collection name is returned as the second value; the signature
// document itself is built later by BlockHandler during signing (Phase D).
func (c *Converter) Convert(
	_ context.Context,
	rawBlock any,
	_ chains.CollectionVersionProvider,
) ([]chains.DocumentGroup, string, error) {
	bundle, ok := rawBlock.(*BlockBundle)
	if !ok {
		return nil, "", fmt.Errorf("converter: expected *BlockBundle, got %T", rawBlock)
	}
	if bundle == nil || bundle.Block == nil {
		return nil, "", fmt.Errorf("converter: nil block in bundle")
	}

	blockInt, err := utils.HexToInt(bundle.Block.Number)
	if err != nil {
		return nil, "", fmt.Errorf("converter: parse block number: %w", err)
	}

	blockData := c.buildBlockData(bundle.Block, blockInt)
	txDocs := c.buildTransactionDocs(bundle)
	receiptMap := c.buildReceiptMap(bundle.Receipts)
	logDocs := c.buildLogDocs(bundle.Transactions, receiptMap)
	aleDocs, aleParentRefs := c.buildALEDocs(bundle.Transactions, blockInt)

	groups := []chains.DocumentGroup{
		{Collection: c.collections.Block, Docs: []map[string]any{blockData}},
	}
	if len(txDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: c.collections.Transaction, Docs: txDocs})
	}
	if len(logDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{Collection: c.collections.Log, Docs: logDocs})
	}
	if len(aleDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection: c.collections.AccessListEntry,
			Docs:       aleDocs,
			ParentRef:  aleParentRefs,
		})
	}

	return groups, c.collections.BlockSignature, nil
}

// buildTransactionDocs builds data maps for all non-nil transactions in the bundle.
func (c *Converter) buildTransactionDocs(bundle *BlockBundle) []map[string]any {
	var docs []map[string]any
	for _, tx := range bundle.Transactions {
		if tx == nil {
			continue
		}
		docs = append(docs, c.buildTransactionData(tx))
	}
	return docs
}

// buildReceiptMap indexes receipts by transaction hash for log iteration.
func (c *Converter) buildReceiptMap(receipts []*types.TransactionReceipt) map[string]*types.TransactionReceipt {
	m := make(map[string]*types.TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			m[receipt.TransactionHash] = receipt
		}
	}
	return m
}

// buildLogDocs builds data maps for all logs found in the receipts.
func (c *Converter) buildLogDocs(txs []*types.Transaction, receiptMap map[string]*types.TransactionReceipt) []map[string]any {
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
			docs = append(docs, c.buildLogData(&receipt.Logs[i]))
		}
	}
	return docs
}

// buildALEDocs builds data maps for all access list entries across transactions.
// It also returns a parallel slice of parent transaction hashes (one per doc)
// so BlockHandler.Store can resolve _transactionID links without requiring a
// transactionHash field on the ALE schema.
func (c *Converter) buildALEDocs(txs []*types.Transaction, blockInt int64) ([]map[string]any, []string) {
	var docs []map[string]any
	var parentRefs []string
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		for i := range tx.AccessList {
			docs = append(docs, c.buildALEData(&tx.AccessList[i], blockInt))
			parentRefs = append(parentRefs, tx.Hash)
		}
	}
	return docs, parentRefs
}

// GetSchema implements chains.Converter. It delegates to the schema loader,
// which concatenates embedded .graphql files and swaps the chain prefix.
func (c *Converter) GetSchema() (string, error) {
	return schema.GetSchemaForChain(c.collections)
}

// GetCollections implements chains.Converter.
func (c *Converter) GetCollections() []string {
	return c.collections.AllCollections()
}

// Collections implements chains.Converter.
func (c *Converter) Collections() chains.Collections {
	return c.collections
}

// GetHighestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "DESC", "GetHighestStoredBlockNumber")
}

// GetLowestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "ASC", "GetLowestStoredBlockNumber")
}

// GetDocIDsByBlockRange implements chains.Converter. It returns document IDs
// for every relevant collection whose block-number field falls in [from, to]
// inclusive. SnapshotSignature is excluded; BlockSignature is included.
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	cols := []struct {
		name  string
		field string
	}{
		{c.collections.Block, constants.NumberFieldValue},
		{c.collections.Transaction, constants.BlockNumberKeyValue},
		{c.collections.Log, constants.BlockNumberKeyValue},
		{c.collections.AccessListEntry, constants.BlockNumberKeyValue},
		{c.collections.BlockSignature, constants.BlockNumberKeyValue},
	}

	result := make(map[string][]string)
	for _, col := range cols {
		docIDs, err := c.queryCollectionDocIDs(ctx, n, col.name, col.field, from, to)
		if err != nil {
			return nil, fmt.Errorf("query docIDs for %s: %w", col.name, err)
		}
		if len(docIDs) > 0 {
			result[col.name] = docIDs
		}
	}
	return result, nil
}

// --- Build helpers (data maps only; Document creation deferred to BlockHandler.Store) ---

// buildBlockData builds the data map for a block document.
func (c *Converter) buildBlockData(block *types.Block, blockInt int64) map[string]any {
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

// buildTransactionData builds the data map for a transaction document.
// Link fields (_blockID) are NOT set here; BlockHandler.Store resolves them
// after AddDocument assigns the block's persistent docID.
func (c *Converter) buildTransactionData(tx *types.Transaction) map[string]any {
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

// buildLogData builds the data map for a log document.
// Link fields (_blockID, _transactionID) are NOT set here; BlockHandler.Store
// resolves them after AddDocument assigns the block and tx docIDs.
func (c *Converter) buildLogData(logEntry *types.Log) map[string]any {
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

// buildALEData builds the data map for an access list entry document.
// Link fields (_transactionID) are NOT set here; BlockHandler.Store resolves
// them after AddDocument assigns the tx docID, using the ParentRef parallel
// array on the DocumentGroup.
func (c *Converter) buildALEData(ale *types.AccessListEntry, blockNumber int64) map[string]any {
	return map[string]any{
		"address":                     ale.Address,
		constants.BlockNumberKeyValue: blockNumber,
		"storageKeys":                 ale.StorageKeys,
	}
}

// BuildBlockSignatureData builds the data map for a block signature document.
// This is called by BlockHandler during signing (Phase D), not by Convert.
func (c *Converter) BuildBlockSignatureData(
	blockSig *node.BatchSignature,
	blockHash string,
	blockNumber int64,
	sortedCIDStrings []string,
) map[string]any {
	return map[string]any{
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
}

// --- Progress query helpers ---

// queryBlockNumber runs the single-row block-number query with the given
// ordering ("DESC" or "ASC") and tags all errors with opName.
func (c *Converter) queryBlockNumber(ctx context.Context, n *node.Node, order, opName string) (int64, error) {
	blockCol := c.collections.Block
	query := `query {` + blockCol + ` (order: {number: ` + order + `}, limit: 1) { number }}`

	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, errors.NewQueryFailed("defra", opName, query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no data")
	}

	var block map[string]any
	switch arr := data[blockCol].(type) {
	case []any:
		if len(arr) == 0 {
			return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
		}
		var ok bool
		block, ok = arr[0].(map[string]any)
		if !ok {
			return 0, fmt.Errorf("%s: %w", opName, errBlockNumberCorrupt)
		}
	case []map[string]any:
		if len(arr) == 0 {
			return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
		}
		block = arr[0]
	default:
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
	}

	switch v := block[constants.NumberFieldValue].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	}

	return 0, fmt.Errorf("%s: %w", opName, errBlockNumberCorrupt)
}

// queryCollectionDocIDs queries a single collection for all document IDs
// whose block-number field falls in [from, to] inclusive. It uses chunked
// _geq/_leq GraphQL range filters.
func (c *Converter) queryCollectionDocIDs(ctx context.Context, n *node.Node, colName, field string, from, to int64) ([]string, error) {
	var allDocIDs []string
	const chunkSize = 100

	for chunkStart := from; chunkStart <= to; chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize - 1
		chunkEnd = min(chunkEnd, to)

		query := fmt.Sprintf(
			`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { _docID } }`,
			colName, field, chunkStart, chunkEnd,
		)

		result := n.DB.ExecRequest(ctx, query)
		if len(result.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s [%d-%d]: %w", colName, chunkStart, chunkEnd, result.GQL.Errors[0])
		}

		data, ok := result.GQL.Data.(map[string]any)
		if !ok {
			continue
		}

		raw := data[colName]
		if raw == nil {
			continue
		}

		var docs []any
		switch typed := raw.(type) {
		case []any:
			docs = typed
		case []map[string]any:
			docs = make([]any, len(typed))
			for i, d := range typed {
				docs[i] = d
			}
		default:
			continue
		}

		for _, doc := range docs {
			m, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			if docID, ok := m["_docID"].(string); ok {
				allDocIDs = append(allDocIDs, docID)
			}
		}
	}

	return allDocIDs, nil
}
