package schema

import (
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// GetSchemaForChain returns the schema with collection names adapted for the
// given chain's prefix. It delegates to LoadSchemaSDLForChain.
func GetSchemaForChain(c chains.Collections) (string, error) {
	return LoadSchemaSDLForChain(c)
}