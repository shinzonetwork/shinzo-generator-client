package defra

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"

	stderrors "errors"
)

// ReorgHandler is the rollback primitive used when the chain reorgs.
type ReorgHandler interface {
	// RollbackBlocks soft-deletes the verified orphan documents for the
	// height range [from, to]: per height, the stored block hash is
	// compared against the new chain's hash delivered via
	// WithRollbackChainHashes, and only heights whose stored hash disagrees
	// are deleted, with children collections first and the block document
	// last. Deletions replicate to hosts; prune-queue entries are retained
	// so the pruner can hard-purge the soft-deleted docs later.
	RollbackBlocks(ctx context.Context, from, to int64) error

	// GetStoredBlockHash returns the stored block hash at a height; ""
	// when none stored.
	GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error)
}

// rollbackChainHashesKey keys the context value WithRollbackChainHashes
// attaches. It lives in this package so the processor and the roller agree
// on the transport inside ReorgHandler's stable two-argument signature: the
// pass-through handlers the processor is wired with forward the context
// untouched, so the verified hashes reach the roller through every wrapper.
type rollbackChainHashesKey struct{}

// WithRollbackChainHashes returns a context that carries the verified
// new-chain block hashes for the pending rollback, keyed by height. The
// roller deletes only heights whose stored hash disagrees with the map;
// heights it does not cover (or covers with an empty hash) stay untouched,
// because nothing may be deleted without a hash-verified proof that it is
// an orphan.
func WithRollbackChainHashes(ctx context.Context, chainHashes map[int64]string) context.Context {
	return context.WithValue(ctx, rollbackChainHashesKey{}, chainHashes)
}

// rollbackChainHashAt reads the verified new-chain hash for a height from
// the rollback context. ok is false when the height carries no verified
// hash, which leaves the height untouched by the rollback.
func rollbackChainHashAt(ctx context.Context, blockNumber int64) (string, bool) {
	chainHashes, _ := ctx.Value(rollbackChainHashesKey{}).(map[int64]string)
	if len(chainHashes) == 0 {
		return "", false
	}
	chainHash, ok := chainHashes[blockNumber]
	return chainHash, ok && chainHash != ""
}

// ReorgRoller implements ReorgHandler against an embedded DefraDB node.
// Rollback deletions are ordinary DAG commits, not local purges, so they
// replicate to peers and hosts drop the orphan docs; the deletion commits
// also let hosts distinguish the delete (old docID) from the canonical
// re-insert (new docID) during re-indexing.
type ReorgRoller struct {
	defraNode *node.Node
	converter chains.Converter
}

// Compile-time guarantee that ReorgRoller implements ReorgHandler.
var _ ReorgHandler = (*ReorgRoller)(nil)

// SetDocIDTracker is kept for call-site compatibility only and does
// nothing: the rollback intentionally retains every prune-queue entry so
// the pruner can hard-purge the soft-deleted orphan docIDs later, and the
// resume height no longer reads the queue. Trackers are wired to the block
// handler only.
func (r *ReorgRoller) SetDocIDTracker(_ DocIDTrackerInterface) {}

// NewReorgRoller creates a ReorgRoller over the given embedded DefraDB node,
// using the converter for collection-name resolution and range docID queries.
func NewReorgRoller(defraNode *node.Node, converter chains.Converter) (*ReorgRoller, error) {
	if defraNode == nil {
		return nil, errors.NewConfigurationError("defra", "NewReorgRoller",
			"defraNode is nil", "", nil)
	}
	if converter == nil {
		return nil, errors.NewConfigurationError("defra", "NewReorgRoller",
			"converter is nil", "", nil)
	}
	return &ReorgRoller{
		defraNode: defraNode,
		converter: converter,
	}, nil
}

// deleteOrder orders the collections to delete for one height so the
// crash-recovery invariant holds: the block document goes last, and the
// children go before it in the approved order (transaction, log,
// access-list entry, signature), with any other collection cleared
// alphabetically in between. Block-last keeps the dangerous direction safe
// and detectable: a rollback dying midway leaves the orphan's block doc
// visible, so the next run's seed check re-detects the mismatch and
// repeats the rollback (defect 8; alphabetical ordering never achieves
// this because BlockSignature sorts after Block).
func (r *ReorgRoller) deleteOrder(cols []string) ([]string, error) {
	ordered := make([]string, 0, len(cols))

	for _, role := range []string{
		chains.TypeTransaction,
		chains.TypeLog,
		chains.TypeAccessListEntry,
		chains.TypeBlockSignature,
	} {
		name, err := r.converter.Collections().GetCollection(role)
		if err != nil {
			if stderrors.Is(err, chains.ErrUnknownCollection) {
				continue
			}
			return nil, fmt.Errorf("resolve collection %q: %w", role, err) //nolint:err113
		}
		if slices.Contains(cols, name) {
			ordered = append(ordered, name)
		}
	}

	blockCol, err := r.converter.Collections().GetCollection(chains.TypeBlock)
	if err != nil {
		return nil, fmt.Errorf("resolve block collection: %w", err) //nolint:err113
	}
	for _, col := range cols {
		if col == blockCol || slices.Contains(ordered, col) {
			continue
		}
		ordered = append(ordered, col)
	}
	if slices.Contains(cols, blockCol) {
		ordered = append(ordered, blockCol)
	}
	return ordered, nil
}

// RollbackBlocks implements ReorgHandler. It verifies every height in
// [from, to] against the new chain's hash, delivered by the caller through
// WithRollbackChainHashes, and soft-deletes only the proven orphans: a
// height is deleted exactly when a stored block hash exists at it and
// disagrees with the verified new-chain hash. Heights without a verified
// chain hash - above the processor's confirmed segment, say - and heights
// matching the new chain are kept untouched, never guessed at. An orphan
// left behind on that unverifiable path keeps its visible block doc, so
// the next run's seed check re-detects the mismatch and repeats the
// rollback.
//
// Per verified orphan the docIDs are resolved via the converter's
// chain-agnostic range query - whose default queries already exclude
// soft-deleted documents, so a re-run over an already rolled-back height
// selects nothing and deletes nothing - and are soft-deleted collection by
// collection, children first and the block document last.
//
// The deletes are NOT transactional across collections: a failure midway
// leaves the height partly soft-deleted, which is safe because only
// visible documents are selectable (the next rollback picks up where this
// one left off) and the caller stops indexing on error. Block-last is the
// crash-recovery invariant: whatever survives keeps the orphan's block doc
// visible, so the mismatch stays re-detectable (defect 8).
//
// The prune-queue entries of deleted docs are intentionally retained: the
// pruner is the only mechanism that hard-purges the soft-deleted docIDs
// locally.
func (r *ReorgRoller) RollbackBlocks(ctx context.Context, from, to int64) error {
	chainHashes, ok := ctx.Value(rollbackChainHashesKey{}).(map[int64]string)
	if from <= to && (!ok || len(chainHashes) == 0) {
		logger.Sugar.Warnf("Rollback [%d, %d]: no verified new-chain hashes provided; no height can be verified or deleted",
			from, to)
	}

	for height := from; height <= to; height++ {
		chainHash, verified := rollbackChainHashAt(ctx, height)
		if !verified {
			continue
		}

		stored, err := r.GetStoredBlockHash(ctx, height)
		if err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: %w", from, to, err) //nolint:err113
		}
		if stored == "" || stored == chainHash {
			continue
		}

		logger.Sugar.Infof("Rollback: deleting the verified orphan at height %d (stored hash %s, new-chain hash %s)",
			height, stored, chainHash)

		docIDsByCollection, err := r.converter.GetDocIDsByBlockRange(ctx, r.defraNode, height, height)
		if err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: query docIDs at height %d: %w", from, to, height, err) //nolint:err113
		}

		cols := make([]string, 0, len(docIDsByCollection))
		for col := range docIDsByCollection {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		ordered, err := r.deleteOrder(cols)
		if err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: %w", from, to, err) //nolint:err113
		}

		for _, colName := range ordered {
			if err := r.softDeleteCollectionDocs(ctx, colName, docIDsByCollection[colName]); err != nil {
				return fmt.Errorf("rollback blocks [%d, %d]: %w", from, to, err) //nolint:err113
			}
		}
	}
	return nil
}

// softDeleteCollectionDocs soft-deletes the given documents of one collection,
// chunked so no single delete filter carries more than chunkSize docIDs. The
// filter targets only default-visible docs (soft-deleted ones are excluded
// from selection), which keeps repeated rollbacks error-free rather than
// failing on an already-deleted document.
func (r *ReorgRoller) softDeleteCollectionDocs(ctx context.Context, colName string, docIDs []string) error {
	const chunkSize = 100

	for chunkStart := 0; chunkStart < len(docIDs); chunkStart += chunkSize {
		chunk := docIDs[chunkStart:min(chunkStart+chunkSize, len(docIDs))]

		col, err := r.defraNode.DB.GetCollectionByName(ctx, colName)
		if err != nil {
			return fmt.Errorf("get collection %s: %w", colName, err) //nolint:err113
		}

		conditions := make([]any, len(chunk))
		for i, id := range chunk {
			conditions[i] = id
		}
		filter := map[string]any{
			docIDFieldName: map[string]any{"_in": conditions},
		}
		res, err := col.DeleteDocumentsWithFilter(ctx, filter)
		if err != nil {
			return fmt.Errorf("soft-delete documents from %s: %w", colName, err) //nolint:err113
		}
		logger.Sugar.Infof("Rollback: soft-deleted %d/%d documents from %s", res.Count, len(chunk), colName)
	}
	return nil
}

// GetStoredBlockHash implements ReorgHandler. It queries the block collection
// at the given height through the same ExecRequest pattern as the converter's
// range queries; since default queries exclude soft-deleted docs, a
// rolled-back height reports as not stored. More than one visible block doc
// at one height (the number field is indexed but not unique) is an
// inconsistent store that the continuity checks feed on, so it errors
// instead of guessing a row: a wrong guess can false-rollback a good block,
// and stopping indexing loudly is the safe response.
func (r *ReorgRoller) GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error) {
	blockCol, err := r.converter.Collections().GetCollection(chains.TypeBlock)
	if err != nil {
		return "", fmt.Errorf("resolve block collection: %w", err) //nolint:err113
	}

	query := fmt.Sprintf(
		`query { %s(filter: {%s: {_eq: %d}}) { %s %s } }`,
		blockCol, constants.NumberFieldName, blockNumber, constants.HashFieldName, docIDFieldName,
	)

	result := r.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return "", fmt.Errorf("query stored block %d: %w", blockNumber, result.GQL.Errors[0]) //nolint:err113
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return "", nil
	}

	rows := gqlRows(data[blockCol])
	if len(rows) == 0 {
		return "", nil
	}
	if len(rows) > 1 {
		return "", fmt.Errorf("query stored block %d: %d visible block documents share the height; the store state is inconsistent",
			blockNumber, len(rows)) //nolint:err113
	}

	hash, _ := rows[0][constants.HashFieldName].(string)
	return hash, nil
}

// gqlRows flattens a GraphQL result field into row maps. The rows arrive as
// []any or []map[string]any depending on the driver path; both shapes are
// handled by the existing ExecRequest consumers.
func gqlRows(raw any) []map[string]any {
	var rows []map[string]any
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
	case []map[string]any:
		rows = typed
	}
	return rows
}
