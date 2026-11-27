package tron

import (
	"context"
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/sourcenetwork/defradb/node"
)

// chainPrefixFromConfig derives the collection prefix (e.g.
// "Tron__Mainnet") from the chain config, applying Tron fallbacks where the
// config leaves name or network empty. A nil config keeps the Tron default;
// every Tron prefix derivation follows this lenient fallback contract, while
// the nil-config callers in pkg/server stay evm-default, which is fine.
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

// Converter is the Tron chain-specific knowledge layer. It implements
// chains.Converter by transforming raw block data (BlockBundle) into
// []DocumentGroup, generating the chain schema, and answering progress
// queries against the local DefraDB instance.
//
// Converter never stores a *node.Node. It receives one explicitly on each
// progress-query call, keeping it stateless and testable without a live DB.
type Converter struct {
	collections *CollectionNames
	cfg         *config.Config
}

// Compile-time guarantee that Converter implements chains.Converter.
var _ chains.Converter = (*Converter)(nil)

// NewConverter creates a Converter from the given config, deriving the
// collection prefix via chainPrefixFromConfig.
func NewConverter(cfg *config.Config) *Converter {
	prefix := chainPrefixFromConfig(cfg)
	return &Converter{
		collections: NewCollectionNames(prefix),
		cfg:         cfg,
	}
}

// Convert implements chains.Converter. It type-asserts rawBlock to
// *BlockBundle and builds DocumentGroups for block, transactions, logs, and
// internal transfers, each data group only when non-empty: empty blocks
// (common on Tron) produce just the block group, and the BlockHandler skips
// nothing else. The data maps contain only field values; cross-document
// link fields (_blockID, _transactionID) are resolved during Store by the
// returned LinkStamper.
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

	receiptMap := buildReceiptMap(bundle.Receipts)
	blockData := buildBlockData(bundle.Block)
	txDocs := buildTransactionDocs(bundle, receiptMap)
	logDocs := buildLogDocs(bundle, receiptMap)
	transferDocs := buildInternalTransferDocs(bundle, receiptMap)

	groups := []chains.DocumentGroup{
		{
			Collection:      c.collections.Block,
			Docs:            []map[string]any{blockData},
			BatchSize:       c.maxDocsPerTxn(),
			BlockNumField:   constants.NumberFieldName,
			BlockHashField:  constants.HashFieldName,
			ParentHashField: ParentHashFieldName,
		},
	}
	if len(txDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.Transaction,
			Docs:          txDocs,
			BatchSize:     c.txBatchSize(c.maxDocsPerTxn()),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}
	if len(logDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.Log,
			Docs:          logDocs,
			BatchSize:     c.logBatchSize(c.maxDocsPerTxn()),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}
	if len(transferDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.InternalTransfer,
			Docs:          transferDocs,
			BatchSize:     c.logBatchSize(c.maxDocsPerTxn()),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}

	return chains.ConversionResult{
		Groups:              groups,
		SignatureCollection: c.collections.BlockSignature,
		LinkStamper:         newTronLinkStamper(c.collections),
	}, nil
}

// buildReceiptMap indexes receipts by transaction id for the join with the
// block's transaction list.
func buildReceiptMap(receipts []*TransactionInfo) map[string]*TransactionInfo {
	m := make(map[string]*TransactionInfo)
	for _, receipt := range receipts {
		if receipt != nil {
			m[receipt.ID] = receipt
		}
	}
	return m
}

// maxDocsPerTxn returns the configured default batch size for BlockHandler.
func (c *Converter) maxDocsPerTxn() int {
	if c.cfg != nil && c.cfg.Indexer.MaxDocsPerTxn > 0 {
		return c.cfg.Indexer.MaxDocsPerTxn
	}
	return 1000 //nolint:mnd
}

// txBatchSize returns the transaction group's per-write batch size.
func (c *Converter) txBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxTxDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxTxDocsPerBatch
	}
	return defaultBatch
}

// logBatchSize returns the per-write batch size shared by the log and
// internal-transfer groups: both derive from receipts, and the optional
// MaxALEDocsPerBatch knob stays evm-only.
func (c *Converter) logBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxLogDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxLogDocsPerBatch
	}
	return defaultBatch
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

// lowestBlockQueryLimit returns the configured row-window size for the
// lowest-block number query (converter.lowest_block_query_limit).
func (c *Converter) lowestBlockQueryLimit() int {
	if c.cfg != nil && c.cfg.Converter.LowestBlockQueryLimit > 0 {
		return c.cfg.Converter.LowestBlockQueryLimit
	}
	return config.DefaultLowestBlockQueryLimit
}

// GetDocIDsByBlockRange implements chains.Converter. It returns document
// IDs for every relevant collection whose block-number field falls in
// [from, to] inclusive. SnapshotSignature is excluded; BlockSignature is
// included.
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	cols := []struct {
		name  string
		field string
	}{
		{c.collections.Block, constants.NumberFieldName},
		{c.collections.Transaction, constants.BlockNumberFieldName},
		{c.collections.Log, constants.BlockNumberFieldName},
		{c.collections.InternalTransfer, constants.BlockNumberFieldName},
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
