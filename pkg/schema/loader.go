package schema

import (
	"embed"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

//go:embed collections/*.graphql
var collectionFS embed.FS

// collectionFilenames derives the ordered list of .graphql filenames from
// constants.DefaultCollections(). Each file contains exactly one primary type.
func collectionFilenames() []string {
	names := constants.DefaultCollections()
	prefix := constants.DefaultCollectionPrefix + "__"
	filenames := make([]string, len(names))
	for i, name := range names {
		suffix := strings.TrimPrefix(name, prefix)
		filenames[i] = strings.ToLower(suffix[:1]) + suffix[1:] + ".graphql"
	}
	return filenames
}

// LoadSchemaSDL reads all collections/*.graphql files in the order defined by
// collectionFilenames() and concatenates them into a single SDL document.
func LoadSchemaSDL() (string, error) {
	collectionOrder := collectionFilenames()

	var parts []string

	for _, filename := range collectionOrder {
		data, err := collectionFS.ReadFile("collections/" + filename)
		if err != nil {
			return "", fmt.Errorf("failed to read %s: %w", filename, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return "", fmt.Errorf("collection file %s is empty", filename)
		}

		parts = append(parts, content)
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("no collection files found in collections/")
	}

	concatenated := strings.Join(parts, "\n\n")

	expectedTypes := constants.DefaultCollections()
	for _, typeName := range expectedTypes {
		if !strings.Contains(concatenated, typeName) {
			return "", fmt.Errorf("concatenated SDL missing expected type: %s", typeName)
		}
	}

	return concatenated, nil
}
