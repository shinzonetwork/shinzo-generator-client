package schema

import (
	_ "embed"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

// SchemaGraphQL contains the legacy embedded GraphQL schema definition from schema.graphql.
// Used only for parity validation during migration; will be removed after verification.
//
//go:embed schema.graphql
var SchemaGraphQL string

// GetSchema returns the full concatenated GraphQL schema from all collection files.
func GetSchema() string {
	s, err := LoadSchemaSDL()
	if err != nil {
		panic("schema loader failed: " + err.Error())
	}
	return s
}

// GetSchemaForChain returns the schema with collection names adapted for the given chain prefix.
// It replaces the default prefix with the provided one.
func GetSchemaForChain(prefix string) string {
	s := GetSchema()
	return strings.ReplaceAll(s, constants.DefaultCollectionPrefix, prefix)
}
