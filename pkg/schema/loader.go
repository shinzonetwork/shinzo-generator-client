package schema

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

var (
	// ErrEmptyPrefix is returned when a chain prefix is required but not provided.
	ErrEmptyPrefix = errors.New("prefix must not be empty")

	// ErrUnknownCollectionType is returned when SchemaApplyOrder contains a type
	// name that has no corresponding filename in CollectionFileForType.
	ErrUnknownCollectionType = errors.New("unknown collection type")

	// ErrEmptyCollectionFile is returned when a collection .graphql file exists
	// in the embedded FS but contains no content.
	ErrEmptyCollectionFile = errors.New("collection file is empty")
)

//go:embed collections/*.graphql
var collectionFS embed.FS

// CollectionEntry represents a named collection with its GraphQL type name.
type CollectionEntry struct {
	Name     string `json:"name"`
	TypeName string `json:"type_name"`
}

// ListCollectionFiles returns ordered .graphql filenames from
// constants.SchemaApplyOrder, suitable for per-file AddSchema calls.
func ListCollectionFiles() ([]string, error) {
	order := constants.SchemaApplyOrder()
	files := make([]string, len(order))
	for i, typeName := range order {
		f := constants.CollectionFileForType(typeName)
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
// replaces the default prefix with the provided one.
func LoadCollectionSDLForChain(filename, prefix string) (string, error) {
	if prefix == "" {
		return "", ErrEmptyPrefix
	}
	raw, err := LoadCollectionSDL(filename)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(raw, constants.DefaultCollectionPrefix, prefix), nil
}

// ListCollections returns all collections in schema dependency order,
// using the provided prefix to build fully-qualified type names.
func ListCollections(prefix string) []CollectionEntry {
	order := constants.SchemaApplyOrder()
	entries := make([]CollectionEntry, 0, len(order))
	for _, typeName := range order {
		filename := constants.CollectionFileForType(typeName)
		stem := strings.TrimSuffix(filename, ".graphql")
		// SchemaApplyOrder returns type names with the default chain prefix
		// (e.g. "Ethereum__Mainnet__Block"). Strip the default prefix to get
		// the collection suffix ("Block"), then re-apply the caller's prefix
		// so the result is e.g. "Arbitrum__Block".
		suffix := strings.TrimPrefix(typeName, constants.DefaultCollectionPrefix+"__")
		entries = append(entries, CollectionEntry{
			Name:     stem,
			TypeName: prefix + "__" + suffix,
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
func PrecomputeCollectionSDLs(prefix string) (map[string]string, error) {
	cache := make(map[string]string)
	for _, typeName := range constants.SchemaApplyOrder() {
		filename := constants.CollectionFileForType(typeName)
		if filename == "" {
			continue
		}
		stem := strings.TrimSuffix(filename, ".graphql")
		sdl, err := LoadCollectionSDLForChain(filename, prefix)
		if err != nil {
			return nil, fmt.Errorf("load collection SDL %s for prefix %s: %w", filename, prefix, err)
		}
		cache[stem] = sdl
	}
	return cache, nil
}

// LoadSchemaSDL reads all collections/*.graphql files in dependency order
// and concatenates them into a single SDL document.
func LoadSchemaSDL() (string, error) {
	files, err := ListCollectionFiles()
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
// concatenates them into a single SDL document with the default collection
// prefix replaced by the provided one.
func LoadSchemaSDLForChain(prefix string) (string, error) {
	if prefix == "" {
		return "", ErrEmptyPrefix
	}
	sdl, err := LoadSchemaSDL()
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(sdl, constants.DefaultCollectionPrefix, prefix), nil
}
