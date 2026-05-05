package defrasdk

import (
	"context"
	"fmt"
	"os"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/utils"
	"github.com/sourcenetwork/defradb/node"
)

// This could be moved to schema package

type SchemaApplier interface {
	ApplySchema(ctx context.Context, defraNode *node.Node) error
}

type MockSchemaApplierThatSucceeds struct{}

func (schema *MockSchemaApplierThatSucceeds) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	return nil
}

const defaultPath = "schema/schema.graphql"

type SchemaApplierFromFile struct {
	DefaultPath string
}

func (schema *SchemaApplierFromFile) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	if len(schema.DefaultPath) == 0 {
		schema.DefaultPath = defaultPath
	}

	schemaPath, err := utils.FindFile(schema.DefaultPath)
	if err != nil {
		return fmt.Errorf("Failed to find schema file: %v", err)
	}

	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("Failed to read schema file: %v", err)
	}

	_, err = defraNode.DB.AddSchema(ctx, string(schemaBytes))
	return err
}

type SchemaApplierFromProvidedSchema struct {
	ProvidedSchema string
}

func NewSchemaApplierFromProvidedSchema(schema string) *SchemaApplierFromProvidedSchema {
	return &SchemaApplierFromProvidedSchema{
		ProvidedSchema: schema,
	}
}

func (schema *SchemaApplierFromProvidedSchema) ApplySchema(ctx context.Context, defraNode *node.Node) error {
	_, err := defraNode.DB.AddSchema(ctx, string(schema.ProvidedSchema))
	return err
}