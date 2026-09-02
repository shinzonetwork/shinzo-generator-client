package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/client"
)

// enforceSnapshotLimit deletes the oldest snapshot files (and their companion
// snapshotSignature docs) so that at most cfg.MaxSnapshots files remain.
// MaxSnapshots < 0 means unlimited (no-op); 0 falls back to
// config.DefaultMaxSnapshots, mirroring SnapshotConfig.SetDefaults.
// Individual file/doc deletions are logged and skipped on failure; an error
// is returned only when expired files could not be removed from disk.
func (s *Snapshotter) enforceSnapshotLimit(ctx context.Context) error {
	limit := s.cfg.MaxSnapshots
	if limit < 0 {
		return nil // unlimited retention.
	}
	if limit == 0 {
		limit = config.DefaultMaxSnapshots
	}

	// ListSnapshots returns well-formed snapshot files sorted ascending by
	// start block, so the oldest snapshots are at the front. Transient ".tmp"
	// files never match the glob and are never purge candidates.
	infos := s.ListSnapshots()
	if len(infos) <= limit {
		return nil
	}

	expired := infos[:len(infos)-limit]
	logger.Sugar.Infof("Snapshot purge: %d file(s) exceed max_snapshots=%d, removing %d oldest",
		len(infos), limit, len(expired))

	var (
		removed  int
		failed   int
		firstErr error
	)
	for _, info := range expired {
		if err := os.Remove(filepath.Join(s.cfg.Dir, info.Filename)); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			logger.Sugar.Warnf("Snapshot purge: failed to remove %s: %v", info.Filename, err)
			continue
		}
		removed++
		logger.Sugar.Infof("Snapshot purged: %s (blocks %d-%d)", info.Filename, info.StartBlock, info.EndBlock)
		s.deleteSnapshotSignatureDoc(ctx, info.Filename)
	}

	// Keep metrics aligned with the on-disk state after purging.
	if remaining, err := filepath.Glob(filepath.Join(s.cfg.Dir, "snapshot_*.kvsnap.gz")); err == nil {
		s.mu.Lock()
		s.totalSnapshots = len(remaining)
		s.mu.Unlock()
	}

	if firstErr != nil {
		return fmt.Errorf("snapshot purge: failed to remove %d of %d expired file(s): %w", failed, len(expired), firstErr)
	}
	logger.Sugar.Infof("Snapshot purge complete: removed %d file(s), %d remaining", removed, len(infos)-removed)
	return nil
}

// deleteSnapshotSignatureDoc removes the SnapshotSignature documents that
// accompany a purged snapshot file so no orphaned metadata accumulates in
// DefraDB. Deletion is skipped gracefully when there is no DefraDB node or
// the snapshotSignature collection is unresolved (e.g. tests without a DB);
// failures are logged and never abort the file purge.
func (s *Snapshotter) deleteSnapshotSignatureDoc(ctx context.Context, filename string) {
	if s.defraNode == nil || s.snapshotSigCollection == "" {
		return
	}

	docIDs, err := s.querySnapshotSignatureDocIDs(ctx, filename)
	if err != nil {
		logger.Sugar.Warnf("Snapshot purge: failed to query signature docs for %s: %v", filename, err)
		return
	}
	if len(docIDs) == 0 {
		return
	}

	clientDocIDs := make([]client.DocID, 0, len(docIDs))
	for _, id := range docIDs {
		docID, err := client.NewDocIDFromString(id)
		if err != nil {
			logger.Sugar.Warnf("Snapshot purge: skipping invalid docID %s for %s: %v", id, filename, err)
			continue
		}
		clientDocIDs = append(clientDocIDs, docID)
	}
	if len(clientDocIDs) == 0 {
		return
	}

	col, err := s.defraNode.DB.GetCollectionByName(ctx, s.snapshotSigCollection)
	if err != nil {
		logger.Sugar.Warnf("Snapshot purge: failed to get collection %s: %v", s.snapshotSigCollection, err)
		return
	}

	// Signature docs are write-once; historical DAG version pruning is unnecessary.
	if err := col.PurgeByDocIDs(ctx, clientDocIDs, false); err != nil {
		logger.Sugar.Warnf("Snapshot purge: failed to delete %d signature doc(s) for %s: %v", len(clientDocIDs), filename, err)
		return
	}

	logger.Sugar.Infof("Snapshot purge: deleted %d signature doc(s) for %s", len(clientDocIDs), filename)
}

// querySnapshotSignatureDocIDs returns the _docIDs of SnapshotSignature
// documents whose snapshotFile matches the given filename.
func (s *Snapshotter) querySnapshotSignatureDocIDs(ctx context.Context, filename string) ([]string, error) {
	query := fmt.Sprintf(
		`query { %s(filter: {snapshotFile: {_eq: %q}}) { _docID } }`,
		s.snapshotSigCollection, filename,
	)

	result := s.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil, fmt.Errorf("query snapshot signature docIDs: %w", result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil, nil
	}
	raw := data[s.snapshotSigCollection]
	if raw == nil {
		return nil, nil
	}

	var docIDs []string
	appendDocID := func(m map[string]any) {
		if id, ok := m["_docID"].(string); ok {
			docIDs = append(docIDs, id)
		}
	}
	switch typed := raw.(type) {
	case []any:
		for _, d := range typed {
			if m, ok := d.(map[string]any); ok {
				appendDocID(m)
			}
		}
	case []map[string]any:
		for _, m := range typed {
			appendDocID(m)
		}
	}

	return docIDs, nil
}
