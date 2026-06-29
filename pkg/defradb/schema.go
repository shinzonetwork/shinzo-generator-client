package defradb

import (
	"context"

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
	ChainPrefix string
}

// NewSchemaApplierFromDir creates a schema applier that uses the embedded
// modular collection files. If chainPrefix is empty, the default prefix is used.
func NewSchemaApplierFromDir(chainPrefix string) *SchemaApplierFromDir {
	return &SchemaApplierFromDir{ChainPrefix: chainPrefix}
}

// ApplySchema applies the embedded schema to the given DefraDB node.
func (s *SchemaApplierFromDir) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	return ApplyCollectionSchemas(ctx, defraNode, s.ChainPrefix)
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
