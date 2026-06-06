package defradb

import (
	"context"
	"fmt"
	"strings"

	indexerErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/sourcenetwork/defradb/node"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

// ApplyCollectionSchemas applies the embedded collection schemas to the
// DefraDB node. If chainPrefix is empty, constants.DefaultCollectionPrefix
// is used.
//
// It first attempts a monolithic AddSchema call (all collections in one SDL)
// so that cross-type references within the schema are resolved by the engine.
// If that fails with "collection already exists" (i.e. a previous run already
// created some collections), it falls back to per-file application: each
// collection file is applied individually so that already-existing collections
// are skipped while missing ones are created. All non-idempotent errors cause
// an immediate return with the filename wrapped for debugging.
//
// AddSchema is strictly additive: it can create new collections and add new
// fields to existing types that do not yet have them. It CANNOT:
//   - Rename or remove existing fields
//   - Change field types
//   - Modify existing @relation definitions
//   - Alter composite types or indexes
//
// Migrating or modifying already-existing collections requires a separate
// mechanism such as DefraDB Lens migrations or purge-and-reapply.
func ApplyCollectionSchemas(ctx context.Context, defraNode *node.Node, chainPrefix string) error {
	prefix := chainPrefix
	if prefix == "" {
		prefix = constants.DefaultCollectionPrefix
	}

	sdl, err := schema.LoadSchemaSDLForChain(prefix)
	if err != nil {
		return fmt.Errorf("failed to load schema: %w", err)
	}

	_, err = defraNode.DB.AddSchema(ctx, sdl)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), indexerErrors.ErrStrCollectionAlreadyExists) {
		return fmt.Errorf("failed to apply schema: %w", err)
	}

	logger.Sugar.Info("Some collections already exist, applying per file")
	return applyCollectionSchemasPerFile(ctx, defraNode, prefix)
}

// applyCollectionSchemasPerFile applies each collection schema file individually.
// A "collection already exists" error for any single file is logged at Info
// level and skipped so that re-starts are idempotent.
//
// NOTE: This fallback path assumes all dependent types (Block, Transaction,
// Log, AccessListEntry) already exist from a prior monolithic application. If
// only independent types (BlockSignature, SnapshotSignature) were pre-seeded,
// the per-file application of dependent types will fail because their @relation
// cross-references cannot be resolved individually. This scenario only arises
// from manual partial pre-seeding; normal operation always hits the monolithic
// path first or the full-restart fallback where all types already exist.
func applyCollectionSchemasPerFile(ctx context.Context, defraNode *node.Node, prefix string) error {
	files, err := schema.ListCollectionFiles()
	if err != nil {
		return fmt.Errorf("failed to list collection files: %w", err)
	}

	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, prefix)
		if err != nil {
			return fmt.Errorf("failed to load collection file %s: %w", file, err)
		}

		_, err = defraNode.DB.AddSchema(ctx, sdl)
		if err != nil {
			if strings.Contains(err.Error(), indexerErrors.ErrStrCollectionAlreadyExists) {
				logger.Sugar.Infof("Collection from %s already exists, skipping", file)
				continue
			}
			return fmt.Errorf("failed to apply collection schema %s: %w", file, err)
		}
	}

	return nil
}
