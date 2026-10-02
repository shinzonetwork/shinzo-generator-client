package defra

import (
	"context"
	"fmt"
	"sort"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// ReorgHandler is the rollback primitive used when the chain reorgs.
type ReorgHandler interface {
	// RollbackBlocks soft-deletes every doc (e.g. Block, Transaction, Log,
	// AccessListEntry, BlockSignature) for blocks [from, to]. Replicates
	// to hosts.
	RollbackBlocks(ctx context.Context, from, to int64) error

	// GetStoredBlockHash returns the stored block hash at a height; ""
	// when none stored.
	GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error)
}

// ReorgRoller implements ReorgHandler against an embedded DefraDB node.
// Rollback deletions are ordinary DAG commits — not local purges — so they
// replicate to peers and hosts drop the orphan docs; the deletion commits
// also let hosts distinguish the delete (old docID) from the canonical
// re-insert (new docID) during re-indexing.
type ReorgRoller struct {
	defraNode *node.Node
	converter chains.Converter
	// docIDTracker optionally removes the rolled-back heights from the
	// prune queue, whose persisted entries would otherwise keep driving
	// prunes with the dead docIDs the rollback just deleted.
	docIDTracker DocIDTrackerInterface
}

// Compile-time guarantee that ReorgRoller implements ReorgHandler.
var _ ReorgHandler = (*ReorgRoller)(nil)

// SetDocIDTracker attaches the docID tracker whose entries the rollback
// cleans up; nil (the default) skips the cleanup.
func (r *ReorgRoller) SetDocIDTracker(tracker DocIDTrackerInterface) {
	r.docIDTracker = tracker
}

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

// RollbackBlocks implements ReorgHandler. It resolves the docID set for
// [from, to] via the converter's chain-agnostic range query — whose default
// queries already exclude soft-deleted documents, so a re-run over an
// already rolled-back range selects nothing and deletes nothing — and
// soft-deletes the documents collection by collection. A collection with no
// docs in range is skipped, and an empty range is a no-op. After the
// deletions succeed it drops the range from the docID tracker (prune-queue
// cleanup); a tracker failure is surfaced so the caller stops indexing
// rather than running a pruner over inconsistent bookkeeping.
func (r *ReorgRoller) RollbackBlocks(ctx context.Context, from, to int64) error {
	docIDsByCollection, err := r.converter.GetDocIDsByBlockRange(ctx, r.defraNode, from, to)
	if err != nil {
		return fmt.Errorf("rollback blocks [%d, %d]: query docIDs: %w", from, to, err) //nolint:err113
	}

	cols := make([]string, 0, len(docIDsByCollection))
	for col := range docIDsByCollection {
		cols = append(cols, col)
	}
	sort.Strings(cols)

	for _, colName := range cols {
		if err := r.softDeleteCollectionDocs(ctx, colName, docIDsByCollection[colName]); err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: %w", from, to, err) //nolint:err113
		}
	}

	// The tracker cleanup runs only after the soft-deletes succeeded:
	// dropping queue entries for docs that are still stored would strand
	// them from every future prune.
	if r.docIDTracker != nil {
		if err := r.docIDTracker.RollbackBlocks(from, to); err != nil {
			return fmt.Errorf("rollback blocks [%d, %d]: prune-queue cleanup: %w", from, to, err) //nolint:err113
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
			"_docID": map[string]any{"_in": conditions},
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
// rolled-back height reports as not stored. Multiple rows at one height
// (number is indexed but not unique) return the first row's hash.
func (r *ReorgRoller) GetStoredBlockHash(ctx context.Context, blockNumber int64) (string, error) {
	blockCol, err := r.converter.Collections().GetCollection(chains.TypeBlock)
	if err != nil {
		return "", fmt.Errorf("resolve block collection: %w", err) //nolint:err113
	}

	query := fmt.Sprintf(
		`query { %s(filter: {%s: {_eq: %d}}) { %s _docID } }`,
		blockCol, constants.NumberFieldName, blockNumber, constants.HashFieldName,
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
