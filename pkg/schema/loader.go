package schema

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// CollectionEntry represents a named collection with its GraphQL type name.
type CollectionEntry struct {
	Name     string `json:"name"`
	TypeName string `json:"type_name"`
}

// ListCollections returns all collections in schema dependency order, with
// fully-qualified type names and stem names taken from the chain's
// CollectionFiles. No collection is silently skipped: the chain
// implementation guarantees each pair is complete or reports an error.
func ListCollections(collections chains.Collections) ([]CollectionEntry, error) {
	files, err := collections.CollectionFiles()
	if err != nil {
		return nil, fmt.Errorf("list collection files for prefix %s: %w", collections.Prefix(), err)
	}
	entries := make([]CollectionEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, CollectionEntry{
			Name:     f.Name,
			TypeName: f.TypeName,
		})
	}
	return entries, nil
}

// PrecomputeCollectionSDLs builds a map of collection stem names to their
// chain-specific SDLs. The map is computed once at registration time, so
// per-request handlers never read from the chain's embedded schema files.
//
// It returns an error if any collection SDL cannot be loaded, so callers
// fail fast at startup instead of silently serving a degraded cache.
func PrecomputeCollectionSDLs(collections chains.Collections) (map[string]string, error) {
	files, err := collections.CollectionFiles()
	if err != nil {
		return nil, fmt.Errorf("list collection files for prefix %s: %w", collections.Prefix(), err)
	}
	cache := make(map[string]string, len(files))
	for _, f := range files {
		cache[f.Name] = f.SDL
	}
	return cache, nil
}

// LoadSchemaSDLForChain reads all collection files in dependency order and
// concatenates them into a single SDL document with the chain's prefix
// applied. The join itself is the shared chains.MergeSDL, so this document is
// byte-identical to the chain implementation's own merged output.
func LoadSchemaSDLForChain(collections chains.Collections) (string, error) {
	files, err := collections.CollectionFiles()
	if err != nil {
		return "", fmt.Errorf("list collection files for prefix %s: %w", collections.Prefix(), err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no collection files found for prefix %s", collections.Prefix())
	}
	return chains.MergeSDL(files), nil
}
