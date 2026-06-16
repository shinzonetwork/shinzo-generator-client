package defradb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	indexerErrors "github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/sourcenetwork/defradb/node"
)

// SchemaApplyBackend abstracts the transport for applying a schema SDL.
// Implementations wrap either direct DB calls or HTTP requests.
type SchemaApplyBackend interface {
	// ApplySchema submits one SDL document and returns any error.
	// For "collection already exists" conditions, implementations must
	// return an error whose message contains ErrStrCollectionAlreadyExists.
	ApplySchema(ctx context.Context, sdl string) error
}

// DBBackend applies schema via direct DefraDB API calls.
type DBBackend struct {
	DB node.DB
}

// ApplySchema submits a schema SDL document via the DefraDB client API.
func (b *DBBackend) ApplySchema(ctx context.Context, sdl string) error {
	_, err := b.DB.AddSchema(ctx, sdl)
	return err
}

// HTTPBackend applies schema via HTTP POST to a DefraDB instance.
type HTTPBackend struct {
	URL string
}

// httpError wraps a non-200 HTTP response for downstream substring matching.
type httpError struct {
	StatusCode int
	Body       string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("schema application failed with status %d: %s", e.StatusCode, e.Body)
}

// ApplySchema submits a schema SDL document via HTTP POST to the DefraDB schema API.
func (b *HTTPBackend) ApplySchema(ctx context.Context, sdl string) error {
	schemaURL := b.URL + "/api/v0/schema"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, schemaURL, bytes.NewBufferString(sdl))
	if err != nil {
		return fmt.Errorf("failed to create schema request: %w", err)
	}
	req.Header.Set("Content-Type", "application/schema")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send schema: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		_, _ = io.ReadAll(resp.Body)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return &httpError{StatusCode: resp.StatusCode, Body: string(body)}
}

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
	return applyWithBackend(ctx, &DBBackend{DB: defraNode.DB}, chainPrefix)
}

// ApplyCollectionSchemasViaHTTP applies the embedded collection schemas to an
// external DefraDB instance via its HTTP API. If chainPrefix is empty,
// constants.DefaultCollectionPrefix is used.
//
// See ApplyCollectionSchemas for the additive-only guarantee.
func ApplyCollectionSchemasViaHTTP(ctx context.Context, defraURL, chainPrefix string) error {
	return applyWithBackend(ctx, &HTTPBackend{URL: defraURL}, chainPrefix)
}

// applyWithBackend applies schemas using the given backend. It first attempts
// a monolithic apply (all collections in one SDL) and falls back to per-file
// application on "collection already exists".
func applyWithBackend(ctx context.Context, backend SchemaApplyBackend, chainPrefix string) error {
	prefix := chainPrefix
	if prefix == "" {
		prefix = constants.DefaultCollectionPrefix
	}

	sdl, err := schema.LoadSchemaSDLForChain(prefix)
	if err != nil {
		return fmt.Errorf("failed to load schema: %w", err)
	}

	err = backend.ApplySchema(ctx, sdl)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), indexerErrors.ErrStrCollectionAlreadyExists) {
		return fmt.Errorf("failed to apply schema: %w", err)
	}

	logger.Sugar.Info("Some collections already exist, applying per file")
	return applyPerFileWithBackend(ctx, backend, prefix)
}

// applyPerFileWithBackend applies each collection schema file individually
// via the given backend. A "collection already exists" error for any single
// file is logged at Info level and skipped so that re-starts are idempotent.
//
// NOTE: This fallback path assumes all dependent types (Block, Transaction,
// Log, AccessListEntry) already exist from a prior monolithic application. If
// only independent types (BlockSignature, SnapshotSignature) were pre-seeded,
// the per-file application of dependent types will fail because their @relation
// cross-references cannot be resolved individually. This scenario only arises
// from manual partial pre-seeding; normal operation always hits the monolithic
// path first or the full-restart fallback where all types already exist.
func applyPerFileWithBackend(ctx context.Context, backend SchemaApplyBackend, prefix string) error {
	files, err := schema.ListCollectionFiles()
	if err != nil {
		return fmt.Errorf("failed to list collection files: %w", err)
	}

	for _, file := range files {
		sdl, err := schema.LoadCollectionSDLForChain(file, prefix)
		if err != nil {
			return fmt.Errorf("failed to load collection file %s: %w", file, err)
		}

		err = backend.ApplySchema(ctx, sdl)
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
