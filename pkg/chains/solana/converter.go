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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/sourcenetwork/defradb/node"
)

const (
	// highestBlockQueryLimit keeps the tip query at a single row: corruption
	// at the highest slot indicates a writer bug, not purge residue.
	highestBlockQueryLimit = 1
)

// Converter is the Solana chain-specific knowledge layer. It implements
// chains.Converter by transforming raw block data (*Block, as returned by the
// fetcher) into []DocumentGroup, generating the chain schema, and answering
// progress queries against the local DefraDB instance.
//
// Converter never stores a *node.Node — it receives one explicitly on each
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

// Convert implements chains.Converter. It type-asserts rawBlock to *Block —
// the fetcher's return type, which already embeds transactions, metadata,
// instructions, token balances, and rewards — and builds DocumentGroups in
// the fixed order block → transaction → outer instructions → inner
// instructions → token balance changes → rewards, omitting empty groups.
//
// The data maps contain only field values — cross-document link fields
// (_blockID, _transactionID, _parentInstructionID) are NOT set here;
// BlockHandler.Store resolves them via the returned LinkStamper. The two
// instruction groups share one collection (Instruction); the stamper
// discriminates outer from inner docs by the stackHeight key-presence
// invariant documented on buildInstructionDocs.
//
// Every group is tagged with the batch size and BlockNumField "slot"; the
// block group additionally carries BlockHashField "blockhash". The signature
// collection name is returned inside the ConversionResult; the signature
// document itself is built later by BlockHandler during signing.
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

	blockData := c.buildBlockData(block)
	txDocs := c.buildTransactionDocs(block)
	outerDocs, innerDocs, outerRefs, innerRefs := c.buildInstructionDocs(block)
	tbcDocs, tbcRefs := c.buildTokenBalanceChangeDocs(block)
	rewardDocs := c.buildRewardDocs(block)

	batch := c.maxDocsPerTxn()

	groups := []chains.DocumentGroup{
		{
			Collection:     c.collections.Block,
			Docs:           []map[string]any{blockData},
			BatchSize:      batch,
			BlockNumField:  SlotFieldName,
			BlockHashField: BlockhashFieldName,
		},
	}
	groups = c.appendGroup(groups, c.collections.Transaction, txDocs, batch)
	groups = c.appendGroup(groups, c.collections.Instruction, outerDocs, batch)
	groups = c.appendGroup(groups, c.collections.Instruction, innerDocs, batch)
	groups = c.appendGroup(groups, c.collections.TokenBalanceChange, tbcDocs, batch)
	groups = c.appendGroup(groups, c.collections.Reward, rewardDocs, batch)

	stamper := newSolanaLinkStamper(c.collections, outerRefs, innerRefs, tbcRefs)

	return chains.ConversionResult{
		Groups:              groups,
		SignatureCollection: c.collections.BlockSignature,
		LinkStamper:         stamper,
	}, nil
}

// appendGroup appends a non-empty document group to groups, mirroring the
// EVM converter's omit-empty-groups behaviour.
func (c *Converter) appendGroup(groups []chains.DocumentGroup, collection string, docs []map[string]any, batch int) []chains.DocumentGroup {
	if len(docs) == 0 {
		return groups
	}
	return append(groups, chains.DocumentGroup{
		Collection:    collection,
		Docs:          docs,
		BatchSize:     batch,
		BlockNumField: SlotFieldName,
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

// GetSchema implements chains.Converter. It delegates to the schema loader,
// which sources this adapter's embedded .graphql files via the
// CollectionSDLProvider interface and swaps the chain prefix.
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
// name used for block signatures
// (e.g. "Solana__Mainnet__BlockSignature") without requiring a
// ConversionResult. Used by pruner/snapshot to resolve the block signature
// collection and by the processor's storeWithRetry when calling SignExisting.
func (c *Converter) SignatureCollection() string {
	return c.collections.BlockSignature
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

// buildBlockData builds the data map for a block document. Nullable RPC
// fields (blockHeight, blockTime) flow through as nil when absent.
func (c *Converter) buildBlockData(block *Block) map[string]any {
	var blockHeight, blockTime any
	if block.BlockHeight != nil {
		blockHeight = i64(*block.BlockHeight)
	}
	if block.BlockTime != nil {
		blockTime = *block.BlockTime
	}
	return map[string]any{
		SlotFieldName:             i64(block.Slot),
		BlockhashFieldName:        block.Blockhash,
		ParentSlotFieldName:       i64(block.ParentSlot),
		BlockHeightFieldName:      blockHeight,
		BlockTimeFieldName:        blockTime,
		TransactionCountFieldName: len(block.Transactions),
		RewardCountFieldName:      len(block.Rewards),
	}
}

// buildTransactionDocs builds data maps for all transactions in the block.
func (c *Converter) buildTransactionDocs(block *Block) []map[string]any {
	docs := make([]map[string]any, 0, len(block.Transactions))
	for i := range block.Transactions {
		docs = append(docs, c.buildTransactionData(&block.Transactions[i]))
	}
	return docs
}

// buildTransactionData builds the data map for a transaction document.
// Nullable metadata (computeUnitsConsumed, logMessages) flows through as nil
// when absent; failed transactions carry the raw JSON error string in err.
// Unsigned lamport values (fee, pre/postBalances) are widened to int64.
func (c *Converter) buildTransactionData(tx *Transaction) map[string]any {
	var computeUnits any
	if tx.ComputeUnitsConsumed != nil {
		computeUnits = i64(*tx.ComputeUnitsConsumed)
	}
	return map[string]any{
		SignatureFieldName:               tx.Signature,
		SlotFieldName:                    i64(tx.Slot),
		TransactionIndexFieldName:        tx.TransactionIndex,
		VersionFieldName:                 tx.Version,
		FailedFieldName:                  tx.Failed,
		ErrFieldName:                     tx.Err,
		FeeFieldName:                     i64(tx.Fee),
		ComputeUnitsConsumedFieldName:    computeUnits,
		LogMessagesFieldName:             tx.LogMessages,
		PreBalancesFieldName:             toI64Slice(tx.PreBalances),
		PostBalancesFieldName:            toI64Slice(tx.PostBalances),
		RecentBlockhashFieldName:         tx.RecentBlockhash,
		AccountKeysFieldName:             tx.AccountKeys,
		LoadedAddressesWritableFieldName: tx.LoadedAddressesWritable,
		LoadedAddressesReadonlyFieldName: tx.LoadedAddressesReadonly,
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

// innerParentRef carries one inner instruction's parentage for the
// LinkStamper: the parent transaction's signature and the parent outer
// instruction's index within that transaction. Instruction docs carry the tx
// signature only as a data field (the duplicate-content join field), so the
// stamper resolves links from these parallel arrays (one entry per doc, in
// doc order) — the same pattern the EVM adapter uses for access-list
// entries.
type innerParentRef struct {
	txSignature string
	outerIndex  int
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
// discriminator): outer docs never contain the stackHeight key — the RPC
// reports no stack depth for top-level instructions and the field stays null
// in the document — while inner docs always set it, even when the node sent
// no value (then 0).
func (c *Converter) buildInstructionDocs(block *Block) (
	[]map[string]any, []map[string]any, []string, []innerParentRef,
) {
	var outerDocs, innerDocs []map[string]any
	var outerRefs []string
	var innerRefs []innerParentRef

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		for k := range tx.Instructions {
			instr := &tx.Instructions[k]
			doc := map[string]any{
				ProgramIDFieldName:            instr.ProgramID,
				AccountsFieldName:             instr.Accounts,
				DataFieldName:                 instr.Data,
				InstructionIndexFieldName:     instr.InstructionIndex,
				InnerIndexFieldName:           instr.InnerIndex,
				TransactionSignatureFieldName: tx.Signature,
				SlotFieldName:                 i64(block.Slot),
			}
			outerDocs = append(outerDocs, doc)
			outerRefs = append(outerRefs, tx.Signature)
		}
		for _, group := range tx.InnerInstructions {
			for j := range group.Instructions {
				instr := &group.Instructions[j]
				var stackHeight any
				if instr.StackHeight != nil {
					// Widened from the wire's uint16 to int64: DefraDB
					// rejects unexpected numeric types on field set.
					stackHeight = int64(*instr.StackHeight)
				}
				doc := map[string]any{
					ProgramIDFieldName:            instr.ProgramID,
					AccountsFieldName:             instr.Accounts,
					DataFieldName:                 instr.Data,
					InstructionIndexFieldName:     instr.InstructionIndex,
					InnerIndexFieldName:           instr.InnerIndex,
					StackHeightFieldName:          stackHeight,
					TransactionSignatureFieldName: tx.Signature,
					SlotFieldName:                 i64(block.Slot),
				}
				innerDocs = append(innerDocs, doc)
				innerRefs = append(innerRefs, innerParentRef{
					txSignature: tx.Signature,
					outerIndex:  int(group.Index),
				})
			}
		}
	}
	return outerDocs, innerDocs, outerRefs, innerRefs
}

// buildTokenBalanceChangeDocs diffs one transaction's pre/postTokenBalances
// into change documents plus the parallel parent-transaction-signature refs.
//
// Entries are unioned by accountIndex: an account present on only one side
// (token account opened or closed mid-transaction) yields nil on the absent
// side. tokenAccount resolves the accountIndex against the transaction's
// committed key list (static keys plus ALT-loaded writable/readonly keys, in
// runtime concatenation order), degrading to "" on out-of-range indices —
// the same trade the client's newInstruction makes.
func (c *Converter) buildTokenBalanceChangeDocs(block *Block) ([]map[string]any, []string) {
	var docs []map[string]any
	var refs []string

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		committed := committedAccountKeys(tx)
		pre := indexTokenBalances(tx.PreTokenBalances)
		post := indexTokenBalances(tx.PostTokenBalances)

		for _, accountIndex := range unionAccountIndexes(pre, post) {
			docs = append(docs, c.buildTokenBalanceChangeData(
				tx.Slot, tx.Signature, accountIndex, pre[accountIndex], post[accountIndex], committed))
			refs = append(refs, tx.Signature)
		}
	}
	return docs, refs
}

// committedAccountKeys rebuilds the transaction's committed account-key
// list: static message keys plus the ALT-loaded writable and readonly keys,
// in the order the runtime concatenates them (mirrors solana_client.go).
func committedAccountKeys(tx *Transaction) []string {
	committed := make([]string, 0, len(tx.AccountKeys)+len(tx.LoadedAddressesWritable)+len(tx.LoadedAddressesReadonly))
	committed = append(committed, tx.AccountKeys...)
	committed = append(committed, tx.LoadedAddressesWritable...)
	committed = append(committed, tx.LoadedAddressesReadonly...)
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
// (mint, owner, programId) prefer the post entry — the transaction's
// end-state — falling back to pre. Amounts are raw strings (token amounts
// can exceed int64); a nil amount marks the side where the account had no
// balance entry. txSignature is stored as the join field making the doc's
// content unique per parent transaction (see TransactionSignatureFieldName).
func (c *Converter) buildTokenBalanceChangeData(
	slot uint64, txSignature string, accountIndex uint16, pre, post TokenBalance, committed []string,
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
		MintFieldName:                 identity.Mint,
		OwnerFieldName:                identity.Owner,
		TokenAccountFieldName:         resolveKey(committed, uint64(accountIndex)),
		PreAmountFieldName:            preAmount,
		PostAmountFieldName:           postAmount,
		ProgramIDFieldName:            identity.ProgramID,
		TransactionSignatureFieldName: txSignature,
		SlotFieldName:                 i64(slot),
	}
}

// buildRewardDocs builds data maps for the block's reward entries. postBalance
// is stored as a string (SDL decision: uint64 lamport totals can exceed
// int64) and commission only exists on voting/staking rewards.
func (c *Converter) buildRewardDocs(block *Block) []map[string]any {
	docs := make([]map[string]any, 0, len(block.Rewards))
	for _, reward := range block.Rewards {
		var commission any
		if reward.Commission != nil {
			commission = strconv.FormatUint(uint64(*reward.Commission), 10)
		}
		docs = append(docs, map[string]any{
			PubkeyFieldName:      reward.Pubkey,
			LamportsFieldName:    reward.Lamports,
			PostBalanceFieldName: strconv.FormatUint(reward.PostBalance, 10),
			RewardTypeFieldName:  reward.RewardType,
			CommissionFieldName:  commission,
			SlotFieldName:        i64(block.Slot),
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
// for every relevant collection whose slot field falls in [from, to]
// inclusive. SnapshotSignature is excluded; BlockSignature is included
// (signature documents use the shared blockNumber field, not slot).
func (c *Converter) GetDocIDsByBlockRange(ctx context.Context, n *node.Node, from, to int64) (map[string][]string, error) {
	cols := []struct {
		name  string
		field string
	}{
		{c.collections.Block, SlotFieldName},
		{c.collections.Transaction, SlotFieldName},
		{c.collections.Instruction, SlotFieldName},
		{c.collections.TokenBalanceChange, SlotFieldName},
		{c.collections.Reward, SlotFieldName},
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

// queryBlockNumber runs the slot-number query with the given ordering
// ("DESC" or "ASC") and row limit, returning the first row whose slot field
// is parseable. The `_geq: 0` filter excludes rows whose slot is missing or
// null (purge residue): slots are non-negative, so every real block is
// admitted, while slotless rows — which sort ahead of real numbers under
// ASC — can never fill the query window. Rows that still arrive unparseable
// are skipped and reported; all errors are tagged with opName.
func (c *Converter) queryBlockNumber(ctx context.Context, n *node.Node, order, opName string, queryLimit int) (int64, error) {
	blockCol := c.collections.Block
	field := SlotFieldName
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
		// The number filter hides rows without a slot, so an empty result
		// does not by itself mean the collection is empty. Distinguish the
		// two cases: no documents at all is benign ("not found"), while
		// documents without any usable slot are corruption and must
		// hard-fail so the pruner maps them to ErrNoValidBlocks instead of
		// skipping pruning.
		present, err := c.hasAnyBlockDocs(ctx, n, blockCol, opName)
		if err != nil {
			return 0, err
		}
		if present {
			return 0, fmt.Errorf("%s: %w (no rows have a usable slot field)", opName, chains.ErrBlockNumberCorrupt)
		}
		return 0, errors.NewDocumentNotFound("defra", opName, blockCol, "no blocks")
	}

	return firstUsableRow(rows, opName)
}

// firstUsableRow returns the slot number of the first parseable row,
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
		num, ok := parseSlotNumberRow(block)
		if ok {
			if len(skipped) > 0 {
				logger.Sugar.Warnf("%s: skipped %d corrupt block row(s): %s",
					opName, len(skipped), strings.Join(skipped, "; "))
			}
			return num, nil
		}
		docID, _ := block["_docID"].(string)
		raw := block[SlotFieldName]
		skipped = append(skipped, fmt.Sprintf("docID=%s slot=%T(%v)", docID, raw, raw))
	}

	return 0, fmt.Errorf("%s: %w (all %d rows corrupt: %s)",
		opName, chains.ErrBlockNumberCorrupt, len(rows), strings.Join(skipped, "; "))
}

// hasAnyBlockDocs reports whether the block collection holds any document,
// including rows whose slot field is missing.
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

// parseSlotNumberRow extracts the slot number from a block query row. It
// reports false when the slot field is missing or has an unparseable type.
func parseSlotNumberRow(block map[string]any) (int64, bool) {
	switch v := block[SlotFieldName].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case uint64:
		return int64(v), true //nolint:gosec // stored slots are written via i64, bounded by int64
	default:
		return 0, false
	}
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
