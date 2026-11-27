package evm

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
	"github.com/sourcenetwork/defradb/node"
)

// chainPrefixFromConfig derives the collection prefix (e.g. "Ethereum__Mainnet")
// from the chain name and network. It returns config.ErrChainUnset when cfg is
// nil or either field is empty, so no caller gets a prefix it did not configure.
func chainPrefixFromConfig(cfg *config.Config) (string, error) {
	if cfg == nil || cfg.Chain.Name == "" || cfg.Chain.Network == "" {
		return "", config.ErrChainUnset
	}
	return fmt.Sprintf("%s__%s", cfg.Chain.Name, cfg.Chain.Network), nil
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

// NewConverter creates a Converter for the chain cfg names. It returns
// config.ErrChainUnset when cfg is nil or has no chain name or network.
func NewConverter(cfg *config.Config) (*Converter, error) {
	prefix, err := chainPrefixFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Converter{
		collections: NewCollectionNames(prefix),
		cfg:         cfg,
	}, nil
}

// Convert implements chains.Converter. It type-asserts rawBlock to *BlockBundle
// and builds DocumentGroups for block, transactions, logs, and access list
// entries. The data maps contain only field values — cross-document link fields
// (_blockID, _transactionID) are NOT set here; BlockHandler.Store resolves them
// via the returned LinkStamper: links derivable from already-recorded state are
// stamped into the docs before each write, and each write's docIDs are
// recorded afterwards to resolve later groups.
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
			Collection:      c.collections.Block,
			Docs:            []map[string]any{blockData},
			BatchSize:       defaultBatch,
			BlockNumField:   constants.NumberFieldName,
			BlockHashField:  constants.HashFieldName,
			ParentHashField: ParentHashFieldName,
		},
	}
	if len(txDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.Transaction,
			Docs:          txDocs,
			BatchSize:     c.txBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}
	if len(logDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.Log,
			Docs:          logDocs,
			BatchSize:     c.logBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}
	if len(aleDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.AccessListEntry,
			Docs:          aleDocs,
			BatchSize:     c.aleBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberFieldName,
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

// lowestBlockQueryLimit returns the configured row-window size for the
// lowest-block number query (converter.lowest_block_query_limit).
func (c *Converter) lowestBlockQueryLimit() int {
	if c.cfg != nil && c.cfg.Converter.LowestBlockQueryLimit > 0 {
		return c.cfg.Converter.LowestBlockQueryLimit
	}
	return config.DefaultLowestBlockQueryLimit
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
//
// A transaction may legally repeat an access-list entry: EIP-2930 does not
// require uniqueness and each repeat just costs gas again on-chain. Repeats
// inside one tx carry the same address, block number, storage keys and parent
// tx hash, so they produce content-identical documents, and content-addressed
// docIDs can hold at most one document per unique content — emitting a repeat
// is a statement the store cannot honour and it makes the whole batch's write
// fail with "already exists". Each repeat is therefore dropped here so the
// emitted set is exactly the store's writable document set. The RPC/fetch
// layer keeps chain-data fidelity intact; this is the correct seam because
// Transaction.AccessList is consumed nowhere else.
func (c *Converter) buildALEDocs(txs []*Transaction, blockInt int64) ([]map[string]any, []string) {
	var docs []map[string]any
	var parentRefs []string
	seen := make(map[string]struct{})
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		for i := range tx.AccessList {
			ale := &tx.AccessList[i]
			key := aleDocKey(tx.Hash, ale)
			if _, dup := seen[key]; dup {
				logger.Sugar.Debugf("converter: block %d: dropping repeated access-list entry for %s in tx %s; its document is content-identical to one already emitted",
					blockInt, ale.Address, tx.Hash)
				continue
			}
			seen[key] = struct{}{}
			docs = append(docs, c.buildALEData(ale, blockInt))
			parentRefs = append(parentRefs, tx.Hash)
		}
	}
	return docs, parentRefs
}

// aleDocKey builds the identity of an access-list entry document within one
// Convert call: parent tx hash, address, and storage keys joined in chain
// order. The tx hash is load-bearing — the same address and keys brought by
// two different transactions resolve to different _transactionID links and
// therefore different content-addressed docIDs, so both must be kept; repeats
// across blocks never collide either, since the block number is part of the
// document content. Storage-key order is preserved because document content
// is order-sensitive, and the separators cannot occur in hex input so the
// components stay unambiguous.
func aleDocKey(txHash string, ale *AccessListEntry) string {
	return txHash + "|" + ale.Address + "|" + strings.Join(ale.StorageKeys, ",")
}

// GetSchema implements chains.Converter. It delegates to the schema loader,
// which concatenates embedded .graphql files and swaps the chain prefix.
func (c *Converter) GetSchema() (string, error) {
	return c.collections.MergedSDL()
}

// GetCollections implements chains.Converter.
func (c *Converter) GetCollections() []string {
	return c.collections.AllCollections()
}

// Collections implements chains.Converter.
func (c *Converter) Collections() chains.Collections {
	return c.collections
}

// GetHighestStoredBlockNumber implements chains.Converter. The shared
// block-number query keeps the tip query at a single row (corruption at the
// highest block indicates a writer bug, not purge residue).
func (c *Converter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return chains.QueryBlockNumber(ctx, n, c.collections.Block, constants.NumberFieldName, "DESC", "GetHighestStoredBlockNumber", chains.HighestBlockQueryLimit)
}

// GetLowestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return chains.QueryBlockNumber(ctx, n, c.collections.Block, constants.NumberFieldName, "ASC", "GetLowestStoredBlockNumber", c.lowestBlockQueryLimit())
}

// GetDocIDsByBlockRange implements chains.Converter. It returns document IDs
// for every relevant collection whose block-number field falls in [from, to]
// inclusive. SnapshotSignature is excluded; BlockSignature is included.
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	cols := []struct {
		name  string
		field string
	}{
		{c.collections.Block, constants.NumberFieldName},
		{c.collections.Transaction, constants.BlockNumberFieldName},
		{c.collections.Log, constants.BlockNumberFieldName},
		{c.collections.AccessListEntry, constants.BlockNumberFieldName},
		{c.collections.BlockSignature, constants.BlockNumberFieldName},
	}

	result := make(map[string][]string)
	for _, col := range cols {
		docIDs, err := chains.QueryCollectionDocIDs(ctx, n, col.name, col.field, from, to)
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
		constants.HashFieldName:   block.Hash,
		constants.NumberFieldName: blockInt,
		TimestampFieldName:        block.Timestamp,
		ParentHashFieldName:       block.ParentHash,
		DifficultyFieldName:       block.Difficulty,
		"totalDifficulty":         block.TotalDifficulty,
		GasUsedFieldName:          block.GasUsed,
		GasLimitFieldName:         block.GasLimit,
		"baseFeePerGas":           block.BaseFeePerGas,
		NonceFieldName:            block.Nonce,
		MinerFieldName:            block.Miner,
		"size":                    block.Size,
		StateRootFieldName:        block.StateRoot,
		Sha3UnclesFieldName:       block.Sha3Uncles,
		TransactionsRootFieldName: block.TransactionsRoot,
		ReceiptsRootFieldName:     block.ReceiptsRoot,
		LogsBloomFieldName:        block.LogsBloom,
		ExtraDataFieldName:        block.ExtraData,
		MixHashFieldName:          block.MixHash,
		"uncles":                  block.Uncles,
	}
}

// buildTransactionData builds the data map for a transaction document.
// Link fields (_blockID) are NOT set here; BlockHandler.Store resolves them
// after AddDocument assigns the block's persistent docID.
func (c *Converter) buildTransactionData(tx *Transaction) map[string]any {
	txBlockNum, _ := strconv.ParseInt(tx.BlockNumber, 10, 64)
	return map[string]any{
		constants.HashFieldName:        tx.Hash,
		constants.BlockNumberFieldName: txBlockNum,
		constants.BlockHashFieldName:   tx.BlockHash,
		TransactionIndexFieldName:      tx.TransactionIndex,
		"from":                         tx.From,
		"to":                           tx.To,
		"value":                        tx.Value,
		"gas":                          tx.Gas,
		"gasPrice":                     tx.GasPrice,
		"maxFeePerGas":                 tx.MaxFeePerGas,
		"maxPriorityFeePerGas":         tx.MaxPriorityFeePerGas,
		"input":                        tx.Input,
		NonceFieldName:                 tx.Nonce,
		TypeFieldName:                  tx.Type,
		"chainId":                      tx.ChainID,
		"v":                            tx.V,
		"r":                            tx.R,
		"s":                            tx.S,
		GasUsedFieldName:               tx.GasUsed,
		CumulativeGasUsedFieldName:     tx.CumulativeGasUsed,
		EffectiveGasPriceFieldName:     tx.EffectiveGasPrice,
		StatusFieldName:                tx.Status,
	}
}

// buildLogData builds the data map for a log document.
// Link fields (_blockID, _transactionID) are NOT set here; BlockHandler.Store
// resolves them after AddDocument assigns the block and tx docIDs.
func (c *Converter) buildLogData(logEntry *Log) map[string]any {
	logBlockNum, _ := utils.HexToInt(logEntry.BlockNumber)
	return map[string]any{
		AddressFieldName:               logEntry.Address,
		"topics":                       logEntry.Topics,
		"data":                         logEntry.Data,
		constants.BlockNumberFieldName: logBlockNum,
		TransactionHashFieldName:       logEntry.TransactionHash,
		TransactionIndexFieldName:      logEntry.TransactionIndex,
		constants.BlockHashFieldName:   logEntry.BlockHash,
		"logIndex":                     logEntry.LogIndex,
		"removed":                      fmt.Sprintf("%v", logEntry.Removed),
	}
}

// buildALEData builds the data map for an access list entry document.
// Link fields (_transactionID) are NOT set here; the LinkStamper resolves
// them after AddDocument assigns the tx docID, using the parent refs
// parallel array passed to newEvmLinkStamper.
func (c *Converter) buildALEData(ale *AccessListEntry, blockNumber int64) map[string]any {
	return map[string]any{
		AddressFieldName:               ale.Address,
		constants.BlockNumberFieldName: blockNumber,
		StorageKeysFieldName:           ale.StorageKeys,
	}
}
