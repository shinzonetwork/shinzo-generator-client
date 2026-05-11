package defradb

import (
	"context"
	"fmt"
	"os"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/node"
)

// This could be moved to schema package

// SchemaApplier applies a schema to a DefraDB node.
type SchemaApplier interface {
	ApplySchema(ctx context.Context, defraNode *node.Node) error
}

// MockSchemaApplierThatSucceeds is a test schema applier that always returns nil.
type MockSchemaApplierThatSucceeds struct{}

// ApplySchema implements SchemaApplier and intentionally performs no-op success.
func (schema *MockSchemaApplierThatSucceeds) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	return nil
}

const defaultPath = "schema/schema.graphql"

// SchemaApplierFromFile reads a schema file and applies it to a DefraDB node.
type SchemaApplierFromFile struct {
	DefaultPath string
}

// ApplySchema loads schema text from disk and applies it to the given DefraDB node.
func (schema *SchemaApplierFromFile) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	if len(schema.DefaultPath) == 0 {
		schema.DefaultPath = defaultPath
	}

	schemaPath, err := utils.FindFile(schema.DefaultPath)
	if err != nil {
		return fmt.Errorf("Failed to find schema file: %w", err)
	}

	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("Failed to read schema file: %w", err)
	}

	_, err = defraNode.DB.AddSchema(ctx, string(schemaBytes))
	return err
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
	_, err := defraNode.DB.AddSchema(ctx, string(schema.ProvidedSchema))
	return err
}
