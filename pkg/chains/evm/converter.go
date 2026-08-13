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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
	"github.com/sourcenetwork/defradb/node"
)

// errBlockNumberCorrupt indicates that a block document exists in the store
// but its "number" field is missing or has an unparseable type.
var errBlockNumberCorrupt = fmt.Errorf("block exists but has invalid or unparseable number field")

const (
	// defaultChainName is the fallback chain name when config is empty.
	defaultChainName = "Ethereum"

	// defaultNetwork is the fallback network name when config is empty.
	defaultNetwork = "Mainnet"
)

// chainPrefixFromConfig derives the collection prefix (e.g. "Ethereum__Mainnet")
// from the chain config, applying the same defaults as the existing indexer.
func chainPrefixFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return DefaultCollectionPrefix
	}
	name := cfg.Chain.Name
	network := cfg.Chain.Network
	if name == "" {
		name = defaultChainName
	}
	if network == "" {
		network = defaultNetwork
	}
	return fmt.Sprintf("%s__%s", name, network)
}

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
// BlockHandler.Store via the returned LinkStamper after AddDocument assigns
// persistent docIDs.
//
// Each group is tagged with BatchSize (from config) and BlockNumField (the
// field name holding the block number in each doc map). The signature
// collection name is returned inside the ConversionResult; the signature
// document itself is built later by BlockHandler during signing.
func (c *Converter) Convert(
	_ context.Context,
	rawBlock any,
) (chains.ConversionResult, error) {
	bundle, ok := rawBlock.(*BlockBundle)
	if !ok {
		return chains.ConversionResult{}, fmt.Errorf("converter: expected *BlockBundle, got %T", rawBlock)
	}
	if bundle == nil || bundle.Block == nil {
		return chains.ConversionResult{}, fmt.Errorf("converter: nil block in bundle")
	}

	blockInt, err := utils.HexToInt(bundle.Block.Number)
	if err != nil {
		return chains.ConversionResult{}, fmt.Errorf("converter: parse block number: %w", err)
	}

	blockData := c.buildBlockData(bundle.Block, blockInt)
	txDocs := c.buildTransactionDocs(bundle)
	receiptMap := c.buildReceiptMap(bundle.Receipts)
	logDocs := c.buildLogDocs(bundle.Transactions, receiptMap)
	aleDocs, aleParentRefs := c.buildALEDocs(bundle.Transactions, blockInt)

	defaultBatch := c.maxDocsPerTxn()

	groups := []chains.DocumentGroup{
		{
			Collection:     c.collections.Block,
			Docs:           []map[string]any{blockData},
			BatchSize:      defaultBatch,
			BlockNumField:  constants.NumberFieldValue,
			BlockHashField: constants.HashKeyValue,
		},
	}
	if len(txDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.Transaction,
			Docs:          txDocs,
			BatchSize:     c.txBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberKeyValue,
		})
	}
	if len(logDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.Log,
			Docs:          logDocs,
			BatchSize:     c.logBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberKeyValue,
		})
	}
	if len(aleDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.AccessListEntry,
			Docs:          aleDocs,
			BatchSize:     c.aleBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberKeyValue,
		})
	}

	stamper := newEvmLinkStamper(c.collections, aleParentRefs)

	return chains.ConversionResult{
		Groups:              groups,
		SignatureCollection: c.collections.BlockSignature,
		LinkStamper:         stamper,
	}, nil
}

// maxDocsPerTxn returns the configured default batch size for BlockHandler.
func (c *Converter) maxDocsPerTxn() int {
	if c.cfg != nil && c.cfg.Indexer.MaxDocsPerTxn > 0 {
		return c.cfg.Indexer.MaxDocsPerTxn
	}
	return 1000 //nolint:mnd
}

func (c *Converter) txBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxTxDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxTxDocsPerBatch
	}
	return defaultBatch
}

func (c *Converter) logBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxLogDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxLogDocsPerBatch
	}
	return defaultBatch
}

func (c *Converter) aleBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxALEDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxALEDocsPerBatch
	}
	return defaultBatch
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
func (c *Converter) buildReceiptMap(receipts []*TransactionReceipt) map[string]*TransactionReceipt {
	m := make(map[string]*TransactionReceipt)
	for _, receipt := range receipts {
		if receipt != nil {
			m[receipt.TransactionHash] = receipt
		}
	}
	return m
}

// buildLogDocs builds data maps for all logs found in the receipts.
func (c *Converter) buildLogDocs(txs []*Transaction, receiptMap map[string]*TransactionReceipt) []map[string]any {
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
// used by the LinkStamper to resolve _transactionID links without requiring a
// transactionHash field on the ALE schema.
func (c *Converter) buildALEDocs(txs []*Transaction, blockInt int64) ([]map[string]any, []string) {
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

// SignatureCollection implements chains.Converter. It returns the collection
// name used for block signatures (e.g. "Ethereum__Mainnet__BlockSignature")
// without requiring a ConversionResult. Used by pruner/snapshot to resolve
// the block signature collection and by the processor's storeWithRetry when
// calling SignExisting.
func (c *Converter) SignatureCollection() string {
	return c.collections.BlockSignature
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
func (c *Converter) buildBlockData(block *Block, blockInt int64) map[string]any {
	return map[string]any{
		constants.HashKeyValue:             block.Hash,
		constants.NumberFieldValue:         blockInt,
		constants.TimestampKeyValue:        block.Timestamp,
		constants.ParentHashKeyValue:       block.ParentHash,
		constants.DifficultyKeyValue:       block.Difficulty,
		"totalDifficulty":                  block.TotalDifficulty,
		constants.GasUsedKeyValue:          block.GasUsed,
		constants.GasLimitKeyValue:         block.GasLimit,
		"baseFeePerGas":                    block.BaseFeePerGas,
		constants.NonceKeyValue:            block.Nonce,
		constants.MinerKeyValue:            block.Miner,
		"size":                             block.Size,
		constants.StateRootKeyValue:        block.StateRoot,
		constants.Sha3UnclesKeyValue:       block.Sha3Uncles,
		constants.TransactionsRootKeyValue: block.TransactionsRoot,
		constants.ReceiptsRootKeyValue:     block.ReceiptsRoot,
		constants.LogsBloomKeyValue:        block.LogsBloom,
		constants.ExtraDataKeyValue:        block.ExtraData,
		constants.MixHashKeyValue:          block.MixHash,
		"uncles":                           block.Uncles,
	}
}

// buildTransactionData builds the data map for a transaction document.
// Link fields (_blockID) are NOT set here; BlockHandler.Store resolves them
// after AddDocument assigns the block's persistent docID.
func (c *Converter) buildTransactionData(tx *Transaction) map[string]any {
	txBlockNum, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
	return map[string]any{
		constants.HashKeyValue:              tx.Hash,
		constants.BlockNumberKeyValue:       txBlockNum,
		constants.BlockHashKeyValue:         tx.BlockHash,
		constants.TransactionIndexKeyValue:  tx.TransactionIndex,
		"from":                              tx.From,
		"to":                                tx.To,
		"value":                             tx.Value,
		"gas":                               tx.Gas,
		"gasPrice":                          tx.GasPrice,
		"maxFeePerGas":                      tx.MaxFeePerGas,
		"maxPriorityFeePerGas":              tx.MaxPriorityFeePerGas,
		"input":                             tx.Input,
		constants.NonceKeyValue:             tx.Nonce,
		constants.TypeKeyValue:              tx.Type,
		"chainId":                           tx.ChainID,
		"v":                                 tx.V,
		"r":                                 tx.R,
		"s":                                 tx.S,
		constants.CumulativeGasUsedKeyValue: tx.CumulativeGasUsed,
		constants.EffectiveGasPriceKeyValue: tx.EffectiveGasPrice,
		constants.StatusKeyValue:            tx.Status,
	}
}

// buildLogData builds the data map for a log document.
// Link fields (_blockID, _transactionID) are NOT set here; BlockHandler.Store
// resolves them after AddDocument assigns the block and tx docIDs.
func (c *Converter) buildLogData(logEntry *Log) map[string]any {
	logBlockNum, _ := utils.HexToInt(logEntry.BlockNumber)
	return map[string]any{
		constants.AddressKeyValue:          logEntry.Address,
		"topics":                           logEntry.Topics,
		"data":                             logEntry.Data,
		constants.BlockNumberKeyValue:      logBlockNum,
		constants.TransactionHashKeyValue:  logEntry.TransactionHash,
		constants.TransactionIndexKeyValue: logEntry.TransactionIndex,
		constants.BlockHashKeyValue:        logEntry.BlockHash,
		"logIndex":                         logEntry.LogIndex,
		"removed":                          fmt.Sprintf("%v", logEntry.Removed),
	}
}

// buildALEData builds the data map for an access list entry document.
// Link fields (_transactionID) are NOT set here; the LinkStamper resolves
// them after AddDocument assigns the tx docID, using the parent refs
// parallel array passed to newEvmLinkStamper.
func (c *Converter) buildALEData(ale *AccessListEntry, blockNumber int64) map[string]any {
	return map[string]any{
		constants.AddressKeyValue:     ale.Address,
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

func init() {
	chains.RegisterConverterFactory("evm", func(cfg *config.Config) (chains.Converter, error) {
		return NewConverter(cfg), nil
	})
}
