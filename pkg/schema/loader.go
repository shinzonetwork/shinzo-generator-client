package schema

import (
	"embed"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

//go:embed collections/*.graphql
var collectionFS embed.FS

// ListCollectionFiles returns ordered .graphql filenames from
// constants.SchemaApplyOrder, suitable for per-file AddSchema calls.
func ListCollectionFiles() ([]string, error) {
	order := constants.SchemaApplyOrder()
	files := make([]string, len(order))
	for i, typeName := range order {
		f := constants.CollectionFileForType(typeName)
		if f == "" {
			return nil, fmt.Errorf("unknown collection type: %s", typeName)
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
		return "", fmt.Errorf("collection file %s is empty", filename)
	}
	return content, nil
}

// LoadCollectionSDLForChain reads a single collection .graphql file and
// replaces the default prefix with the provided one.
func LoadCollectionSDLForChain(filename, prefix string) (string, error) {
	raw, err := LoadCollectionSDL(filename)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(raw, constants.DefaultCollectionPrefix, prefix), nil
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
