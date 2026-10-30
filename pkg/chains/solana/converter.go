package solana

import (
	"context"
	"fmt"
	"slices"
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
	defaultChainName = "Solana"

	// defaultNetwork is the fallback network name when config is empty.
	defaultNetwork = "Mainnet"

	// highestBlockQueryLimit keeps the tip query at a single row: corruption
	// at the highest slot indicates a writer bug, not purge residue.
	highestBlockQueryLimit = 1
)

// chainPrefixFromConfig derives the collection prefix (e.g. "Solana__Mainnet")
// from the chain config. A nil config uses the adapter's default prefix,
// matching the chains.NewCollections(nil) fallback contract; empty
// name/network fall back to Solana/Mainnet.
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

// Converter is the Solana chain-specific knowledge layer. It implements
// chains.Converter by transforming raw block data (*Block, as returned by
// the fetcher) into []DocumentGroup, generating the chain schema, and
// answering progress queries against the local DefraDB instance.
//
// Converter never stores a *node.Node: it receives one explicitly on each
// progress-query call, keeping it stateless and testable without a live DB.
type Converter struct {
	collections *CollectionNames
	cfg         *config.Config
}

// Compile-time guarantee that Converter implements chains.Converter.
var _ chains.Converter = (*Converter)(nil)

// NewConverter creates a Converter from the given config, deriving the
// collection prefix via chainPrefixFromConfig. A nil config uses defaults
// (Solana__Mainnet), matching chainPrefixFromConfig behaviour.
func NewConverter(cfg *config.Config) *Converter {
	return &Converter{
		collections: NewCollectionNames(chainPrefixFromConfig(cfg)),
		cfg:         cfg,
	}
}

// Convert implements chains.Converter. It type-asserts rawBlock to *Block,
// the fetcher's return type, which already embeds transactions, metadata,
// instructions, token balances, and rewards, and builds DocumentGroups in
// the fixed order block -> transaction -> outer instructions -> inner
// instructions -> token balance changes -> rewards, omitting empty groups.
//
// The data maps contain only field values; cross-document link fields
// (_blockID, _transactionID, _parentInstructionID) are NOT set here:
// BlockHandler.Store resolves them via the returned LinkStamper. The two
// instruction groups share one collection (Instruction); the stamper
// discriminates outer from inner docs by the stackHeight key-presence
// invariant documented on buildInstructionDocs.
//
// The block group carries the generator-host contract fields
// (number/hash/parentHash) so the shared reorg, pruning, and snapshot
// machinery queries it unchanged; derives groups are all tagged with
// blockNumber.
func (c *Converter) Convert(
	_ context.Context,
	rawBlock any,
) (chains.ConversionResult, error) {
	block, ok := rawBlock.(*Block)
	if !ok {
		return chains.ConversionResult{}, fmt.Errorf("converter: expected *Block, got %T", rawBlock)
	}
	if block == nil {
		return chains.ConversionResult{}, fmt.Errorf("converter: nil block")
	}

	defaultBatch := c.maxDocsPerTxn()

	groups := []chains.DocumentGroup{
		{
			Collection:      c.collections.Block,
			Docs:            []map[string]any{c.buildBlockData(block)},
			BatchSize:       defaultBatch,
			BlockNumField:   constants.NumberFieldName,
			BlockHashField:  constants.HashFieldName,
			ParentHashField: ParentHashFieldName,
		},
	}

	outerDocs, innerDocs, outerKeys, innerRefs := c.buildInstructionDocs(block)
	tbcDocs := c.buildTokenBalanceChangeDocs(block)
	groups = c.appendGroup(groups, c.collections.Transaction,
		c.buildTransactionDocs(block), c.txBatchSize(defaultBatch))
	groups = c.appendGroup(groups, c.collections.Instruction,
		outerDocs, c.logBatchSize(defaultBatch))
	groups = c.appendGroup(groups, c.collections.Instruction,
		innerDocs, c.logBatchSize(defaultBatch))
	groups = c.appendGroup(groups, c.collections.TokenBalanceChange,
		tbcDocs, c.logBatchSize(defaultBatch))
	// Reward documents are bounded by the slot leader's validator kinship
	// (small lists), so they ride the default batch instead of a knob.
	groups = c.appendGroup(groups, c.collections.Reward,
		c.buildRewardDocs(block), defaultBatch)

	stamper := newSolanaLinkStamper(c.collections, outerKeys, innerRefs)

	return chains.ConversionResult{
		Groups:              groups,
		SignatureCollection: c.collections.BlockSignature,
		LinkStamper:         stamper,
	}, nil
}

// appendGroup appends a non-empty document group with the shared
// blockNumber tagging, mirroring the EVM converter's omit-empty behavior.
func (c *Converter) appendGroup(groups []chains.DocumentGroup, collection string, docs []map[string]any, batch int) []chains.DocumentGroup {
	if len(docs) == 0 {
		return groups
	}
	return append(groups, chains.DocumentGroup{
		Collection:    collection,
		Docs:          docs,
		BatchSize:     batch,
		BlockNumField: constants.BlockNumberFieldName,
	})
}

// maxDocsPerTxn returns the configured default batch size for BlockHandler.
func (c *Converter) maxDocsPerTxn() int {
	if c.cfg != nil && c.cfg.Indexer.MaxDocsPerTxn > 0 {
		return c.cfg.Indexer.MaxDocsPerTxn
	}
	return 1000 //nolint:mnd
}

// lowestBlockQueryLimit returns the configured row-window size for the
// lowest-slot number query (converter.lowest_block_query_limit).
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

// GetSchema implements chains.Converter. It delegates to the adapter's own
// merged SDL (embedded files with the prefix swapped), the same pattern the
// EVM converter uses.
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

// --- Build helpers (data maps only; Document creation deferred to BlockHandler.Store) ---

// i64 widens an unsigned chain value to int64 for document maps. Solana
// numerics (slots, lamports, balances, fees) fit int64 comfortably, and the
// generic BlockHandler's number parsing only accepts signed ints, so every
// unsigned value is normalized here. No range check: values that could
// exceed int64 are stored as strings instead (reward postBalance).
func i64(v uint64) int64 {
	return int64(v) //nolint:gosec // chain numerics fit int64; larger values are stored as strings
}

// buildBlockData builds the data map for a block document. The nullable RPC
// field blockTime flows through as nil when absent.
func (c *Converter) buildBlockData(block *Block) map[string]any {
	var blockTime any
	if block.BlockTime != nil {
		blockTime = *block.BlockTime
	}
	return map[string]any{
		constants.NumberFieldName: i64(block.Slot),
		constants.HashFieldName:   block.Blockhash,
		ParentHashFieldName:       block.PreviousBlockhash,
		BlockTimeFieldName:        blockTime,
		TransactionCountFieldName: len(block.Transactions),
		RewardCountFieldName:      len(block.Rewards),
	}
}

// buildTransactionDocs builds data maps for all transactions in the block.
func (c *Converter) buildTransactionDocs(block *Block) []map[string]any {
	docs := make([]map[string]any, 0, len(block.Transactions))
	for i := range block.Transactions {
		docs = append(docs, c.buildTransactionData(&block.Transactions[i], block))
	}
	return docs
}

// buildTransactionData builds the data map for a transaction document.
// Nullable metadata (computeUnitsConsumed, logMessages) flows through as nil
// when absent; failed transactions carry the raw JSON error string in err.
// Unsigned lamport values (fee, pre/postBalances) are widened to int64.
func (c *Converter) buildTransactionData(tx *Transaction, block *Block) map[string]any {
	var computeUnits any
	if tx.ComputeUnitsConsumed != nil {
		computeUnits = i64(*tx.ComputeUnitsConsumed)
	}
	return map[string]any{
		SignatureFieldName:             tx.Signature,
		constants.BlockHashFieldName:   block.Blockhash,
		constants.BlockNumberFieldName: i64(block.Slot),
		VersionFieldName:               tx.Version,
		ErrFieldName:                   tx.Err,
		FeeFieldName:                   i64(tx.Fee),
		ComputeUnitsConsumedFieldName:  computeUnits,
		LogMessagesFieldName:           tx.LogMessages,
		PreBalancesFieldName:           toI64Slice(tx.PreBalances),
		PostBalancesFieldName:          toI64Slice(tx.PostBalances),
		RecentBlockhashFieldName:       tx.RecentBlockhash,
		AccountKeysFieldName:           tx.AccountKeys,
		LoadedAddressesFieldName:       tx.LoadedAddresses,
	}
}

// toI64Slice widens unsigned lamport values for document maps.
func toI64Slice(values []uint64) []int64 {
	out := make([]int64, len(values))
	for i, v := range values {
		out[i] = i64(v)
	}
	return out
}

// buildInstructionDocs builds the outer and inner instruction data maps plus
// their parallel parent-ref arrays.
//
// The two returned doc slices are emitted as two separate groups for the
// same Instruction collection, in this order: outers must be written (and
// their docIDs recorded) before inners are stamped, so inner docs can link
// to their parent instruction document. An inner group without its outer
// group is impossible on well-formed data: every CPI group references an
// outer instruction index.
//
// stackHeight key-presence invariant (the stamper's inner/outer
// discriminator): outer docs never contain the stackHeight key, while inner
// docs always set it, even when the node sent no value (then nil).
func (c *Converter) buildInstructionDocs(block *Block) (
	[]map[string]any, []map[string]any, []instructionKey, []innerParentRef,
) {
	var outerDocs, innerDocs []map[string]any
	var outerKeys []instructionKey
	var innerRefs []innerParentRef

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		for k := range tx.Instructions {
			instr := &tx.Instructions[k]
			doc := map[string]any{
				ProgramIDFieldName:             instr.ProgramID,
				AccountsFieldName:              instr.Accounts,
				DataFieldName:                  instr.Data,
				TransactionSignatureFieldName:  tx.Signature,
				constants.BlockHashFieldName:   block.Blockhash,
				constants.BlockNumberFieldName: i64(block.Slot),
			}
			outerDocs = append(outerDocs, doc)
			outerKeys = append(outerKeys, instructionKey{txSignature: tx.Signature, outerIndex: k})
		}
		for _, group := range tx.InnerInstructions {
			for j := range group.Instructions {
				instr := &group.Instructions[j]
				var stackHeight any
				if instr.StackHeight != nil {
					// Widened from the wire's uint16 to int64: DefraDB
					// rejects unexpected numeric types on field set.
					stackHeight = int64(*instr.StackHeight) //nolint:gosec // wire values are small stack depths
				}
				doc := map[string]any{
					ProgramIDFieldName:             instr.ProgramID,
					AccountsFieldName:              instr.Accounts,
					DataFieldName:                  instr.Data,
					StackHeightFieldName:           stackHeight,
					TransactionSignatureFieldName:  tx.Signature,
					constants.BlockHashFieldName:   block.Blockhash,
					constants.BlockNumberFieldName: i64(block.Slot),
				}
				innerDocs = append(innerDocs, doc)
				innerRefs = append(innerRefs, innerParentRef{
					txSignature: tx.Signature,
					outerIndex:  int(group.Index), //nolint:gosec // wire group indexes are small ordinals
				})
			}
		}
	}
	return outerDocs, innerDocs, outerKeys, innerRefs
}

// buildTokenBalanceChangeDocs diffs one transaction's pre/postTokenBalances
// into change documents. The stamper resolves the docs' _transactionID links
// from each doc's own transactionSignature join field, so no parallel
// parent-ref array is needed here.
//
// Entries are unioned by accountIndex: an account present on only one side
// (token account opened or closed mid-transaction) yields nil on the absent
// side. tokenAccount resolves the accountIndex against the transaction's
// committed key list (static keys plus ALT-loaded writable/readonly keys, in
// runtime concatenation order), degrading to "" on out-of-range indices,
// the same trade the client's newInstruction makes.
func (c *Converter) buildTokenBalanceChangeDocs(block *Block) []map[string]any {
	var docs []map[string]any

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		committed := committedAccountKeys(tx)
		pre := indexTokenBalances(tx.PreTokenBalances)
		post := indexTokenBalances(tx.PostTokenBalances)

		for _, accountIndex := range unionAccountIndexes(pre, post) {
			docs = append(docs, c.buildTokenBalanceChangeData(
				block, tx.Signature, accountIndex, pre[accountIndex], post[accountIndex], committed))
		}
	}
	return docs
}

// committedAccountKeys rebuilds the transaction's committed account-key
// list: static message keys plus the ALT-loaded writable and readonly keys,
// in the order the runtime concatenates them (the client collapses both
// loaded ranges into one list, writable first).
func committedAccountKeys(tx *Transaction) []string {
	committed := make([]string, 0, len(tx.AccountKeys)+len(tx.LoadedAddresses))
	committed = append(committed, tx.AccountKeys...)
	committed = append(committed, tx.LoadedAddresses...)
	return committed
}

// indexTokenBalances indexes balance entries by account index. Duplicated
// indices are not expected in well-formed data; last-wins keeps the map
// total.
func indexTokenBalances(balances []TokenBalance) map[uint16]TokenBalance {
	m := make(map[uint16]TokenBalance, len(balances))
	for _, tb := range balances {
		m[tb.AccountIndex] = tb
	}
	return m
}

// unionAccountIndexes returns the sorted union of account indexes present in
// either balance map, giving change docs a deterministic order.
func unionAccountIndexes(pre, post map[uint16]TokenBalance) []uint16 {
	seen := make(map[uint16]struct{}, len(pre)+len(post))
	union := make([]uint16, 0, len(pre)+len(post))
	for idx := range pre {
		seen[idx] = struct{}{}
		union = append(union, idx)
	}
	for idx := range post {
		if _, ok := seen[idx]; !ok {
			union = append(union, idx)
		}
	}
	slices.Sort(union)
	return union
}

// buildTokenBalanceChangeData builds one change document. Identity fields
// (mint, owner, programId) prefer the post entry: the transaction's
// end-state; the whole present entry wins, so no per-field mixing falls back
// to pre. Amounts are raw strings (token amounts can exceed int64); a nil
// amount marks the side where the account had no balance entry.
// transactionSignature is stored as the join field making the doc's content
// unique per parent transaction (see TransactionSignatureFieldName).
func (c *Converter) buildTokenBalanceChangeData(
	block *Block, txSignature string, accountIndex uint16, pre, post TokenBalance, committed []string,
) map[string]any {
	identity := post
	// The zero value means "no entry on this side": fall back to the other
	// side for mint/owner/programId. A real post entry always carries a
	// mint, so an empty mint reliably signals absence.
	if post.Mint == "" {
		identity = pre
	}
	var preAmount, postAmount any
	if pre.Mint != "" {
		preAmount = pre.Amount
	}
	if post.Mint != "" {
		postAmount = post.Amount
	}
	return map[string]any{
		MintFieldName:                  identity.Mint,
		OwnerFieldName:                 identity.Owner,
		TokenAccountFieldName:          resolveKey(committed, uint64(accountIndex)), //nolint:gosec // wire indexes are small ordinals
		PreAmountFieldName:             preAmount,
		PostAmountFieldName:            postAmount,
		ProgramIDFieldName:             identity.ProgramID,
		TransactionSignatureFieldName:  txSignature,
		constants.BlockHashFieldName:   block.Blockhash,
		constants.BlockNumberFieldName: i64(block.Slot),
	}
}

// buildRewardDocs builds data maps for the block's reward entries.
// postBalance is stored as a string (SDL decision: uint64 lamport totals can
// exceed int64) and commission only exists on voting/staking rewards.
func (c *Converter) buildRewardDocs(block *Block) []map[string]any {
	docs := make([]map[string]any, 0, len(block.Rewards))
	for _, reward := range block.Rewards {
		var commission any
		if reward.Commission != nil {
			commission = strconv.FormatUint(uint64(*reward.Commission), 10) //nolint:gosec // wire commissions are small percentages
		}
		docs = append(docs, map[string]any{
			PubkeyFieldName:                reward.Pubkey,
			LamportsFieldName:              reward.Lamports,
			PostBalanceFieldName:           strconv.FormatUint(reward.PostBalance, 10),
			RewardTypeFieldName:            reward.RewardType,
			CommissionFieldName:            commission,
			constants.BlockNumberFieldName: i64(block.Slot),
		})
	}
	return docs
}

// --- Progress queries ---

// GetHighestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetHighestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "DESC", "GetHighestStoredBlockNumber", highestBlockQueryLimit)
}

// GetLowestStoredBlockNumber implements chains.Converter.
func (c *Converter) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	return c.queryBlockNumber(ctx, n, "ASC", "GetLowestStoredBlockNumber", c.lowestBlockQueryLimit())
}

// GetDocIDsByBlockRange implements chains.Converter. It returns document IDs
// for every relevant collection whose block-number field falls in [from, to]
// inclusive. SnapshotSignature is excluded; BlockSignature is included
// (signature documents use the shared blockNumber field, not the slot).
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	cols := []struct {
		name  string
		field string
	}{
		{c.collections.Block, constants.NumberFieldName},
		{c.collections.Transaction, constants.BlockNumberFieldName},
		{c.collections.Instruction, constants.BlockNumberFieldName},
		{c.collections.TokenBalanceChange, constants.BlockNumberFieldName},
		{c.collections.Reward, constants.BlockNumberFieldName},
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

// queryBlockNumber runs the number query with the given ordering
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
// _geq/_leq GraphQL range filters.
func (c *Converter) queryCollectionDocIDs(ctx context.Context, n *node.Node, colName, field string, from, to int64) ([]string, error) {
	var allDocIDs []string
	const chunkSize = 100 //nolint:mnd

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
