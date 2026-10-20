package cosmos

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// errEmptyCollectionFile is returned when an embedded cosmos .graphql file
// has no content.
var errEmptyCollectionFile = errors.New("collection file is empty")

// sdlPrefix is the placeholder prefix the cosmos .graphql files are authored
// against; CollectionFiles swaps it for the chain's prefix. Cosmos types
// reference the shared EVM Block type through the same placeholder.
const sdlPrefix = "Cosmos__Chain"

//go:embed collections/*.graphql
var collectionFS embed.FS

// Cosmos collection SDL files and the stems they are served under.
const (
	cosmosTransactionCollectionFile = "cosmosTransaction.graphql"
	cosmosMessageCollectionFile     = "cosmosMessage.graphql"
	cosmosEventCollectionFile       = "cosmosEvent.graphql"

	cosmosTransactionCollectionStem = "cosmosTransaction"
	cosmosMessageCollectionStem     = "cosmosMessage"
	cosmosEventCollectionStem       = "cosmosEvent"
)

// CollectionNames is the cosmos-evm collection set: the EVM chain family's
// collections (whose Block collection is shared, since the EVM and CometBFT
// block hashes are the same value) plus the native Cosmos transaction,
// message and event collections.
//
// Every Cosmos collection declares an @index on blockNumber, which the pruner
// and GetDocIDsByBlockRange rely on.
type CollectionNames struct {
	chains.Collections

	CosmosTransaction string
	CosmosMessage     string
	CosmosEvent       string
}

// Compile-time guarantee that CollectionNames implements chains.Collections.
var _ chains.Collections = (*CollectionNames)(nil)

// NewCollectionNames extends the EVM collection set with the Cosmos
// collections under the same prefix.
func NewCollectionNames(evmCollections chains.Collections) *CollectionNames {
	prefix := evmCollections.Prefix()
	return &CollectionNames{
		Collections:       evmCollections,
		CosmosTransaction: fmt.Sprintf("%s__CosmosTransaction", prefix),
		CosmosMessage:     fmt.Sprintf("%s__CosmosMessage", prefix),
		CosmosEvent:       fmt.Sprintf("%s__CosmosEvent", prefix),
	}
}

// AllCollections returns the EVM collections followed by the Cosmos ones, in
// P2P filter order.
func (c *CollectionNames) AllCollections() []string {
	return append(c.Collections.AllCollections(), c.cosmosCollections()...)
}

// CollectionFiles returns the EVM collection files followed by the Cosmos
// ones, every SDL adapted to the chain's prefix.
func (c *CollectionNames) CollectionFiles() ([]chains.CollectionFile, error) {
	files, err := c.Collections.CollectionFiles()
	if err != nil {
		return nil, err
	}
	specs := []struct{ typeName, name, file string }{
		{c.CosmosTransaction, cosmosTransactionCollectionStem, cosmosTransactionCollectionFile},
		{c.CosmosMessage, cosmosMessageCollectionStem, cosmosMessageCollectionFile},
		{c.CosmosEvent, cosmosEventCollectionStem, cosmosEventCollectionFile},
	}
	for _, spec := range specs {
		data, err := collectionFS.ReadFile("collections/" + spec.file)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", spec.file, err)
		}
		sdl := strings.TrimSpace(string(data))
		if sdl == "" {
			return nil, fmt.Errorf("%w: %s", errEmptyCollectionFile, spec.file)
		}
		files = append(files, chains.CollectionFile{
			TypeName: spec.typeName,
			Name:     spec.name,
			File:     spec.file,
			SDL:      strings.ReplaceAll(sdl, sdlPrefix, c.Prefix()),
		})
	}
	return files, nil
}

// cosmosCollections lists the Cosmos-only collections in write order.
func (c *CollectionNames) cosmosCollections() []string {
	return []string{c.CosmosTransaction, c.CosmosMessage, c.CosmosEvent}
}
