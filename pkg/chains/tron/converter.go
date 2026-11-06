package tron

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// chainPrefixFromConfig derives the collection prefix (e.g.
// "Tron__Mainnet") from the chain config, applying Tron fallbacks where the
// config leaves name or network empty. A nil config keeps the Tron default,
// matching the chains.NewCollections(nil) fallback contract; the nil-config
// callers in pkg/server stay evm-default, which is fine.
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

const (
	// highestBlockQueryLimit keeps the tip query at a single row: corruption
	// at the highest block indicates a writer bug, not purge residue.
	highestBlockQueryLimit = 1

	// chunkSize bounds each GetDocIDsByBlockRange query window.
	chunkSize = 100
)

// GetHighestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "DESC", "GetHighestStoredBlockNumber", highestBlockQueryLimit)
}

// GetLowestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "ASC", "GetLowestStoredBlockNumber", c.lowestBlockQueryLimit())
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

// queryBlockNumber runs the block-number query with the given ordering
// ("DESC" or "ASC") and row limit, returning the first row whose number
// field is parseable. The `_geq: 0` filter excludes rows whose number is
// missing or null (purge residue): block numbers are non-negative, so every
// real block including genesis is admitted, while numberless rows, which
// sort ahead of real numbers under ASC, can never fill the query window.
// Rows that still arrive unparseable are skipped and reported; all errors
// are tagged with opName.
func (c *Converter) queryBlockNumber(ctx context.Context, n *node.Node, order, opName string, queryLimit int) (int64, error) {
	blockCol := c.collections.Block
	field := constants.NumberFieldName
	query := `query {` + blockCol + ` (filter: {` + field + `: {_geq: 0}}, order: {` + field + `: ` + order + `}, limit: ` + strconv.Itoa(queryLimit) + `) { ` + field + ` _docID }}`

	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, errors.NewQueryFailed("defra", opName, query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no data")
	}

	var rows []any
	switch arr := data[blockCol].(type) {
	case []any:
		rows = arr
	case []map[string]any:
		rows = make([]any, len(arr))
		for i, m := range arr {
			rows[i] = m
		}
	default:
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
	}

	if len(rows) == 0 {
		// The number filter hides rows without a number, so an empty result
		// does not by itself mean the collection is empty. Distinguish the
		// two cases: no documents at all is benign ("not found"), while
		// documents without any usable number are corruption and must
		// hard-fail so the pruner maps them to ErrNoValidBlocks instead of
		// skipping pruning.
		present, err := c.hasAnyBlockDocs(ctx, n, blockCol, opName)
		if err != nil {
			return 0, err
		}
		if present {
			return 0, fmt.Errorf("%s: %w (no rows have a usable number field)", opName, chains.ErrBlockNumberCorrupt)
		}
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
	}

	return firstUsableRow(rows, opName)
}

// firstUsableRow returns the block number of the first parseable row,
// skipping and summarizing unparseable ones. It reports
// chains.ErrBlockNumberCorrupt when no row in the window is usable.
func firstUsableRow(rows []any, opName string) (int64, error) {
	var skipped []string
	for _, r := range rows {
		block, ok := r.(map[string]any)
		if !ok {
			skipped = append(skipped, fmt.Sprintf("row=%#v", r))
			continue
		}
		num, ok := parseBlockNumberRow(block)
		if ok {
			if len(skipped) > 0 {
				logger.Sugar.Warnf("%s: skipped %d corrupt block row(s): %s",
					opName, len(skipped), strings.Join(skipped, "; "))
			}
			return num, nil
		}
		docID, _ := block["_docID"].(string)
		raw := block[constants.NumberFieldName]
		skipped = append(skipped, fmt.Sprintf("docID=%s number=%T(%v)", docID, raw, raw))
	}

	return 0, fmt.Errorf("%s: %w (all %d rows corrupt: %s)",
		opName, chains.ErrBlockNumberCorrupt, len(rows), strings.Join(skipped, "; "))
}

// hasAnyBlockDocs reports whether the collection holds any document,
// including rows whose number field is missing.
func (c *Converter) hasAnyBlockDocs(ctx context.Context, n *node.Node, blockCol, opName string) (bool, error) {
	query := `query {` + blockCol + ` (limit: 1) { _docID }}`

	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return false, errors.NewQueryFailed("defra", opName, query, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return false, nil
	}
	switch rows := data[blockCol].(type) {
	case []any:
		return len(rows) > 0, nil
	case []map[string]any:
		return len(rows) > 0, nil
	}
	return false, nil
}

// parseBlockNumberRow extracts the block number from a block query row. It
// reports false when the number field is missing or has an unparseable
// type.
func parseBlockNumberRow(block map[string]any) (int64, bool) {
	switch v := block[constants.NumberFieldName].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// queryCollectionDocIDs queries a single collection for all document IDs
// whose block-number field falls in [from, to] inclusive. It uses chunked
// _geq/_leq GraphQL range filters.
func (c *Converter) queryCollectionDocIDs(ctx context.Context, n *node.Node, colName, field string, from, to int64) ([]string, error) {
	var allDocIDs []string

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
