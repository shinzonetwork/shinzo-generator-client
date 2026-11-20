package bitcoin

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

const (
	// defaultChainName is the fallback chain name when config is empty.
	defaultChainName = "Bitcoin"

	// defaultNetwork is the fallback network name when config is empty.
	defaultNetwork = "Mainnet"

	// highestBlockQueryLimit keeps the tip query at a single row: corruption
	// at the highest block indicates a writer bug, not purge residue.
	highestBlockQueryLimit = 1

	// defaultMaxDocsPerTxn is the batch fallback when no indexer config is
	// present, matching the store's own default.
	defaultMaxDocsPerTxn = 1000

	// avgOutputsPerTx sizes the output-doc slice from a typical transaction
	// output count; append grows it for deeper blocks without any penalty
	// beyond an occasionally skipped re-allocation.
	avgOutputsPerTx = 2
)

// chainPrefixFromConfig derives the collection prefix (e.g. Bitcoin__Mainnet)
// from the chain config, applying the same defaults the indexer applies: an
// empty name or network falls back to Bitcoin and Mainnet, and a nil config
// falls back to the adapter-wide default prefix.
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

// Converter is the Bitcoin chain-specific knowledge layer. It implements
// chains.Converter by transforming one getblock verbosity-3 decode (*Block)
// into []DocumentGroup, generating the chain schema, and answering progress
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
// collection prefix via chainPrefixFromConfig. A nil config uses the default
// Bitcoin__Mainnet prefix, matching chainPrefixFromConfig behaviour.
func NewConverter(cfg *config.Config) *Converter {
	prefix := chainPrefixFromConfig(cfg)
	return &Converter{
		collections: NewCollectionNames(prefix),
		cfg:         cfg,
	}
}

// Convert implements chains.Converter. It type-asserts rawBlock to the
// verbosity-3 decode the fetcher returns and builds DocumentGroups for block,
// transactions, inputs and outputs, in that write order. The data maps carry
// only field values: cross-document relation links (_blockID,
// _transactionID) are decided by BlockHandler through the returned
// LinkStamper, never here.
//
// Amounts leave this package as int64 satoshis; a value the raw RPC float
// cannot reproduce exactly is a hard error rather than a silently rounded
// document, because wire data cannot produce such values.
func (c *Converter) Convert(_ context.Context, rawBlock any) (chains.ConversionResult, error) {
	block, ok := rawBlock.(*Block)
	if !ok {
		return chains.ConversionResult{}, fmt.Errorf("converter: expected *Block, got %T", rawBlock)
	}
	if block == nil {
		return chains.ConversionResult{}, fmt.Errorf("converter: nil block")
	}

	txDocs, err := c.buildTransactionDocs(block)
	if err != nil {
		return chains.ConversionResult{}, err
	}
	inputDocs, err := c.buildTxInputDocs(block)
	if err != nil {
		return chains.ConversionResult{}, err
	}
	outputDocs, err := c.buildTxOutputDocs(block)
	if err != nil {
		return chains.ConversionResult{}, err
	}

	defaultBatch := c.maxDocsPerTxn()

	// The block group always leads with exactly one doc: BlockHandler.Store
	// reads Groups[0] to extract the number and hash contract fields. Derived
	// groups append only when non-empty.
	groups := []chains.DocumentGroup{
		{
			Collection:      c.collections.Block,
			Docs:            []map[string]any{c.buildBlockDoc(block)},
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
	if len(inputDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.TxInput,
			Docs:          inputDocs,
			BatchSize:     c.logBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}
	if len(outputDocs) > 0 {
		groups = append(groups, chains.DocumentGroup{
			Collection:    c.collections.TxOutput,
			Docs:          outputDocs,
			BatchSize:     c.logBatchSize(defaultBatch),
			BlockNumField: constants.BlockNumberFieldName,
		})
	}

	stamper := newBitcoinLinkStamper(c.collections)

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
	return defaultMaxDocsPerTxn
}

// lowestBlockQueryLimit returns the configured row-window size for the
// lowest-block number query.
func (c *Converter) lowestBlockQueryLimit() int {
	if c.cfg != nil && c.cfg.Converter.LowestBlockQueryLimit > 0 {
		return c.cfg.Converter.LowestBlockQueryLimit
	}
	return config.DefaultLowestBlockQueryLimit
}

// txBatchSize maps the transaction docs onto the transaction batch knob.
func (c *Converter) txBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxTxDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxTxDocsPerBatch
	}
	return defaultBatch
}

// logBatchSize maps the small child docs (txInput, txOutput) onto the
// small-document batch knob the log docs use on evm chains.
func (c *Converter) logBatchSize(defaultBatch int) int {
	if c.cfg != nil && c.cfg.Indexer.MaxLogDocsPerBatch > 0 {
		return c.cfg.Indexer.MaxLogDocsPerBatch
	}
	return defaultBatch
}

// buildBlockDoc builds the data map for one block document. The block schema
// carries no link fields: the transactions relation is the inverse side of
// the transaction docs' block relation and is derived by DefraDB from the
// stamped _blockID.
func (c *Converter) buildBlockDoc(block *Block) map[string]any {
	return map[string]any{
		constants.HashFieldName:   block.Hash,
		constants.NumberFieldName: block.Height,
		ParentHashFieldName:       block.PreviousBlockHash,
		// version rides the schema's Int, widened losslessly from the wire's
		// uint32. nonce rides the schema's String and is written as the
		// decimal spelling of the protocol's 32-bit field.
		"version":           int64(block.Version),
		MerkleRootFieldName: block.MerkleRoot,
		TimeFieldName:       block.Time,
		BitsFieldName:       block.Bits,
		NonceFieldName:      strconv.FormatUint(uint64(block.Nonce), 10),
		ChainworkFieldName:  block.Chainwork,
	}
}

// buildTransactionDocs builds data maps for every transaction of the block in
// wire order; the index is the txIndex each transaction document stores.
func (c *Converter) buildTransactionDocs(block *Block) ([]map[string]any, error) {
	docs := make([]map[string]any, 0, len(block.Tx))
	for i := range block.Tx {
		doc, err := c.buildTransactionDoc(&block.Tx[i], i, block)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// buildTransactionDoc builds the data map for one transaction document. The
// fee rides the undo data: nil means the coinbase transaction or an
// undo-less block, and the nullable feeSat key is omitted in both cases
// instead of writing a fake zero, which would corrupt amount analytics and
// bake a lie into content-addressed docIDs.
func (c *Converter) buildTransactionDoc(tx *Tx, index int, block *Block) (map[string]any, error) {
	doc := map[string]any{
		TxIDFieldName:                  tx.TxID,
		"hash":                         tx.Hash,
		constants.BlockHashFieldName:   block.Hash,
		constants.BlockNumberFieldName: block.Height,
		TransactionIndexFieldName:      index,
		"version":                      int64(tx.Version),
		"locktime":                     int64(tx.LockTime),
		IsCoinbaseFieldName:            isCoinbaseTx(tx),
	}
	if tx.Fee != nil {
		feeSat, err := btcToSatoshis(*tx.Fee)
		if err != nil {
			return nil, fmt.Errorf("converter: tx %s fee: %w", tx.TxID, err)
		}
		doc[FeeSatFieldName] = feeSat
	}
	return doc, nil
}

// isCoinbaseTx reads the wire truth: a transaction is the coinbase exactly
// when its first input carries the coinbase script, which also holds for a
// transaction whose witness makes hash differ from txid.
func isCoinbaseTx(tx *Tx) bool {
	return len(tx.Vin) > 0 && tx.Vin[0].Coinbase != ""
}

// buildTxInputDocs builds data maps for every input of every transaction.
// The txid join field plus the per-doc ordinals keep every document's content
// unique: two byte-identical inputs inside one transaction would mean a
// double-spend consensus violation, and across transactions the txid differs.
func (c *Converter) buildTxInputDocs(block *Block) ([]map[string]any, error) {
	docs := make([]map[string]any, 0, len(block.Tx))
	for i := range block.Tx {
		tx := &block.Tx[i]
		for j := range tx.Vin {
			doc, err := c.buildTxInputDoc(tx, &tx.Vin[j], j, block)
			if err != nil {
				return nil, err
			}
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

// buildTxInputDoc builds the data map for one input document. The coinbase
// branch carries only the script and the witness: Core's wire shape leaves
// txid, vout, scriptSig and prevout unset on it, and writing fake zero
// outpoints would fabricate a spend of some actual output. Regular inputs
// carry the outpoint, the unlocking script, the witness and the denormalized
// prevout when the node's undo data covers it, with each unavailable piece
// key-omitted rather than zeroed.
func (c *Converter) buildTxInputDoc(tx *Tx, vin *Vin, index int, block *Block) (map[string]any, error) {
	doc := map[string]any{
		TxIDFieldName:                  tx.TxID,
		constants.BlockNumberFieldName: block.Height,
		InputIndexFieldName:            index,
		SequenceFieldName:              int64(vin.Sequence),
	}
	if vin.Coinbase != "" {
		doc[CoinbaseFieldName] = vin.Coinbase
	} else {
		if vin.TxID != "" {
			doc[PrevTxIDFieldName] = vin.TxID
		}
		if vin.Vout != nil {
			doc[PrevVoutFieldName] = int64(*vin.Vout)
		}
		if vin.ScriptSig != nil {
			doc[ScriptSigFieldName] = vin.ScriptSig.Hex
		}
	}
	if len(vin.TxInWitness) > 0 {
		doc[WitnessFieldName] = vin.TxInWitness
	}
	if vin.Prevout != nil {
		valueSat, err := btcToSatoshis(vin.Prevout.Value)
		if err != nil {
			return nil, fmt.Errorf("converter: tx %s input %d prevout value: %w", tx.TxID, index, err)
		}
		doc[PrevoutValueSatFieldName] = valueSat
		doc[PrevoutTypeFieldName] = vin.Prevout.ScriptPubKey.Type
		if vin.Prevout.ScriptPubKey.Address != "" {
			doc[PrevoutAddressFieldName] = vin.Prevout.ScriptPubKey.Address
		}
	}
	return doc, nil
}

// buildTxOutputDocs builds data maps for every output of every transaction.
func (c *Converter) buildTxOutputDocs(block *Block) ([]map[string]any, error) {
	docs := make([]map[string]any, 0, len(block.Tx)*avgOutputsPerTx)
	for i := range block.Tx {
		tx := &block.Tx[i]
		for k := range tx.Vout {
			doc, err := c.buildTxOutputDoc(tx, &tx.Vout[k], block)
			if err != nil {
				return nil, err
			}
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

// buildTxOutputDoc builds the data map for one output document. Core omits
// the address for scripts without one (OP_RETURN endings and non-standard
// styles), and the nullable-in-practice address joins the spend graph by
// being absent rather than empty when that happens.
func (c *Converter) buildTxOutputDoc(tx *Tx, vout *Vout, block *Block) (map[string]any, error) {
	valueSat, err := btcToSatoshis(vout.Value)
	if err != nil {
		return nil, fmt.Errorf("converter: tx %s output %d value: %w", tx.TxID, vout.Index, err)
	}
	doc := map[string]any{
		TxIDFieldName:                  tx.TxID,
		constants.BlockNumberFieldName: block.Height,
		VoutIndexFieldName:             int64(vout.Index),
		ValueSatFieldName:              valueSat,
		ScriptPubKeyHexFieldName:       vout.ScriptPubKey.Hex,
		ScriptPubKeyTypeFieldName:      vout.ScriptPubKey.Type,
	}
	if vout.ScriptPubKey.Address != "" {
		doc[ScriptPubKeyAddressFieldName] = vout.ScriptPubKey.Address
	}
	return doc, nil
}

// GetSchema implements chains.Converter. It returns the merged SDL of the
// embedded collection files with the deployment prefix applied.
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

// GetHighestStoredBlockNumber implements chains.Converter. The single-row
// window treats an unparseable number at the tip as writer corruption, not
// purge residue.
func (c *Converter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "DESC", "GetHighestStoredBlockNumber", highestBlockQueryLimit)
}

// GetLowestStoredBlockNumber implements chains.Converter. Its wider window
// tolerates purge residue rows around the oldest real block.
func (c *Converter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "ASC", "GetLowestStoredBlockNumber", c.lowestBlockQueryLimit())
}

// GetDocIDsByBlockRange implements chains.Converter. It returns document IDs
// for every relevant collection whose block-number field falls in [from, to]
// inclusive. SnapshotSignature is intentionally excluded: its documents span
// ranges and a block-range rollback must never delete snapshot history.
// BlockSignature is included: signatures are minted per stored block and roll
// back with it. Transaction rides the unindexed blockNumber field here, an
// accepted deviation because the range stays small and the schema keeps the
// block lookups on the indexed blockHash instead.
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	cols := []struct {
		name  string
		field string
	}{
		{c.collections.Block, constants.NumberFieldName},
		{c.collections.Transaction, constants.BlockNumberFieldName},
		{c.collections.TxInput, constants.BlockNumberFieldName},
		{c.collections.TxOutput, constants.BlockNumberFieldName},
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
// ("DESC" or "ASC") and row limit, returning the first row whose number field
// is parseable. The `_geq: 0` filter excludes rows whose number is missing or
// null (purge residue): block numbers are non-negative, so every real block
// including genesis is admitted, while numberless rows, which sort ahead of
// real numbers under ASC, can never fill the query window. Rows that still
// arrive unparseable are skipped and reported; all errors are tagged with
// opName.
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
		// does not by itself mean the collection is empty. Distinguish the two
		// cases: no documents at all is benign ("not found"), while documents
		// without any usable number are corruption and must hard-fail so the
		// pruner maps them to ErrNoValidBlocks instead of skipping pruning.
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
				logger.Sugar.Warnf("%s: skipped %d corrupt block row(s): %s", opName, len(skipped), strings.Join(skipped, "; "))
			}
			return num, nil
		}
		docID, _ := block["_docID"].(string)
		raw := block[constants.NumberFieldName]
		skipped = append(skipped, fmt.Sprintf("docID=%s number=%T(%v)", docID, raw, raw))
	}

	return 0, fmt.Errorf("%s: %w (all %d rows corrupt: %s)", opName, chains.ErrBlockNumberCorrupt, len(rows), strings.Join(skipped, "; "))
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
// reports false when the number field is missing or has an unparseable type.
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
// _geq/_leq GraphQL range filters so a wide rollback stays bounded per query.
func (c *Converter) queryCollectionDocIDs(ctx context.Context, n *node.Node, colName, field string, from, to int64) ([]string, error) {
	var allDocIDs []string
	const chunkSize = 100

	for chunkStart := from; chunkStart <= to; chunkStart += chunkSize {
		chunkEnd := min(chunkStart+chunkSize-1, to)

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
