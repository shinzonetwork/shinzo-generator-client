package defradb

import (
	"context"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/sourcenetwork/defradb/node"
)

// SchemaApplier applies a schema to a DefraDB node.
type SchemaApplier interface {
	ApplySchema(ctx context.Context, defraNode *node.Node) error
}

// MockSchemaApplierThatSucceeds is a test schema applier that always returns nil.
type MockSchemaApplierThatSucceeds struct{}

// ApplySchema implements SchemaApplier and intentionally performs no-op success.
func (schema *MockSchemaApplierThatSucceeds) ApplySchema(_ context.Context, _ *node.Node) error {
	return nil
}

// SchemaApplierFromDir applies the embedded modular schema to a DefraDB node.
// It delegates to ApplyCollectionSchemas, which first attempts a monolithic
// AddSchema call and falls back to per-file application on restart.
// Note: only additive schema changes are supported. See ApplyCollectionSchemas
// for details.
type SchemaApplierFromDir struct {
	Collections chains.Collections
}

// NewSchemaApplierFromDir creates a schema applier that uses the embedded
// modular collection files with the given chain's collection names.
func NewSchemaApplierFromDir(collections chains.Collections) *SchemaApplierFromDir {
	return &SchemaApplierFromDir{Collections: collections}
}

// ApplySchema applies the embedded schema to the given DefraDB node.
func (s *SchemaApplierFromDir) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	return ApplyCollectionSchemas(ctx, defraNode, s.Collections)
}

// SchemaApplierFromProvidedSchema applies schema text provided directly in memory.
type SchemaApplierFromProvidedSchema struct {
	ProvidedSchema string
}

// NewSchemaApplierFromProvidedSchema creates a schema applier from schema text.
func NewSchemaApplierFromProvidedSchema(schema string) *SchemaApplierFromProvidedSchema {
	return &SchemaApplierFromProvidedSchema{
		ProvidedSchema: schema,
	}
}

// ApplySchema applies the provided schema text to the given DefraDB node.
func (schema *SchemaApplierFromProvidedSchema) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	_, err := defraNode.DB.AddCollection(ctx, schema.ProvidedSchema)
	return err
}
