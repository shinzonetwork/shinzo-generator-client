package schema

import (
	_ "embed"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

// SchemaGraphQL contains the embedded GraphQL schema definition from schema.graphql.
//
//go:embed schema.graphql
var SchemaGraphQL string

// GetSchema returns the GraphQL schema found in `schema.graphql` as a string.
func GetSchema() string {
	return SchemaGraphQL
}

// GetSchemaForChain returns the schema with collection names adapted for the given chain prefix.
// It replaces the default prefix with the provided one.
func GetSchemaForChain(prefix string) string {
	s := GetSchema()
	return strings.ReplaceAll(s, constants.DefaultCollectionPrefix, prefix)
}
