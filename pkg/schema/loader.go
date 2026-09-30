package schema

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

var (
	// ErrEmptyPrefix is retained for backward compatibility; it is no longer
	// returned by any function in this package.
	ErrEmptyPrefix = errors.New("prefix must not be empty")

	// ErrUnknownCollectionType is returned when SchemaApplyOrder contains a type
	// name that has no corresponding filename in CollectionFileForType.
	ErrUnknownCollectionType = errors.New("unknown collection type")

	// ErrEmptyCollectionFile is returned when a collection .graphql file exists
	// in the embedded FS but contains no content.
	ErrEmptyCollectionFile = errors.New("collection file is empty")
)

// embeddedPrefix is the literal prefix baked into the embedded .graphql files.
// The loader swaps this with collections.Prefix() at load time.
const embeddedPrefix = "Ethereum__Mainnet"

//go:embed collections/*.graphql
var collectionFS embed.FS

// CollectionSDLProvider is an optional extension of chains.Collections for
// chain adapters whose collection SDL is embedded in their own package rather
// than in this package's shared collections directory. When the loader detects
// that a Collections implementation also implements this interface, it sources
// file content from the provider — which returns SDL with the chain's
// collection prefix already applied — and skips its own embedded-prefix swap.
//
// Adapters without the interface (e.g. the EVM adapter) keep using the shared
// embedded SDL plus the generic embeddedPrefix replacement.
type CollectionSDLProvider interface {
	// CollectionSDL returns the SDL for the given collection .graphql filename
	// (e.g. "block.graphql"), with the chain's collection prefix already
	// applied. It must return an error for unknown or empty files.
	CollectionSDL(filename string) (string, error)
}

// CollectionEntry represents a named collection with its GraphQL type name.
type CollectionEntry struct {
	Name     string `json:"name"`
	TypeName string `json:"type_name"`
}

// ListCollectionFiles returns ordered .graphql filenames from the given
// chain's SchemaApplyOrder, suitable for per-file AddSchema calls.
func ListCollectionFiles(collections chains.Collections) ([]string, error) {
	order := collections.SchemaApplyOrder()
	files := make([]string, len(order))
	for i, typeName := range order {
		f := collections.CollectionFileForType(typeName)
		if f == "" {
			return nil, fmt.Errorf("%w: %s", ErrUnknownCollectionType, typeName)
		}
		files[i] = f
	}
	return files, nil
}

// LoadCollectionSDL reads a single collection .graphql file and returns
// its raw content (no prefix replacement).
func LoadCollectionSDL(filename string) (string, error) {
	data, err := collectionFS.ReadFile("collections/" + filename)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", filename, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("%w: %s", ErrEmptyCollectionFile, filename)
	}
	return content, nil
}

// LoadCollectionSDLForChain reads a single collection .graphql file and
// returns its SDL with the chain's collection prefix applied. When the chain
// implements CollectionSDLProvider the content comes from the provider
// (prefix already applied); otherwise the shared embedded SDL is used and the
// embedded prefix is replaced with the chain's prefix.
func LoadCollectionSDLForChain(collections chains.Collections, filename string) (string, error) {
	if provider, ok := collections.(CollectionSDLProvider); ok {
		return provider.CollectionSDL(filename)
	}
	raw, err := LoadCollectionSDL(filename)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(raw, embeddedPrefix, collections.Prefix()), nil
}

// ListCollections returns all collections in schema dependency order,
// using the chain's own prefix to build fully-qualified type names.
func ListCollections(collections chains.Collections) []CollectionEntry {
	order := collections.SchemaApplyOrder()
	entries := make([]CollectionEntry, 0, len(order))
	for _, typeName := range order {
		filename := collections.CollectionFileForType(typeName)
		stem := strings.TrimSuffix(filename, ".graphql")
		entries = append(entries, CollectionEntry{
			Name:     stem,
			TypeName: typeName,
		})
	}
	return entries
}

// PrecomputeCollectionSDLs builds a map of collection stem names to their
// chain-specific SDLs. The map is computed once at registration time, so
// per-request handlers never read from the embedded FS or run strings.ReplaceAll.
//
// It returns an error if any collection file cannot be loaded or have its
// prefix replaced, so callers fail fast at startup instead of silently serving
// a degraded cache.
func PrecomputeCollectionSDLs(collections chains.Collections) (map[string]string, error) {
	cache := make(map[string]string)
	for _, typeName := range collections.SchemaApplyOrder() {
		filename := collections.CollectionFileForType(typeName)
		if filename == "" {
			continue
		}
		stem := strings.TrimSuffix(filename, ".graphql")
		sdl, err := LoadCollectionSDLForChain(collections, filename)
		if err != nil {
			return nil, fmt.Errorf("load collection SDL %s for prefix %s: %w", filename, collections.Prefix(), err)
		}
		cache[stem] = sdl
	}
	return cache, nil
}

// LoadSchemaSDL reads all collections/*.graphql files in dependency order
// and concatenates them into a single SDL document (no prefix swap — returns
// raw embeddedPrefix content).
//
// Note: the files always come from this package's shared (EVM) embedded SDL.
// Chain adapters that provide their own SDL via CollectionSDLProvider must be
// loaded through LoadSchemaSDLForChain instead.
func LoadSchemaSDL(collections chains.Collections) (string, error) {
	files, err := ListCollectionFiles(collections)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, f := range files {
		sdl, err := LoadCollectionSDL(f)
		if err != nil {
			return "", err
		}
		parts = append(parts, sdl)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no collection files found in collections/")
	}
	return strings.Join(parts, "\n\n"), nil
}

// LoadSchemaSDLForChain reads all collection files in dependency order and
// concatenates them into a single SDL document with the chain's collection
// prefix applied (per file — see LoadCollectionSDLForChain).
func LoadSchemaSDLForChain(collections chains.Collections) (string, error) {
	files, err := ListCollectionFiles(collections)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(files))
	for _, f := range files {
		sdl, err := LoadCollectionSDLForChain(collections, f)
		if err != nil {
			return "", err
		}
		parts = append(parts, sdl)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no collection files found in collections/")
	}
	return strings.Join(parts, "\n\n"), nil
}
