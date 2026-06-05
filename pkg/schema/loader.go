package schema

import (
	"embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

//go:embed collections/*.graphql
var collectionFS embed.FS

var typeRegex = regexp.MustCompile(`(?m)^type\s+(\w+)\s*\{`)

// collectionFilenames derives the ordered list of .graphql filenames from
// constants.DefaultCollections(). The order from DefaultCollections preserves
// deduplication correctness (multi-type files like transaction.graphql appear
// before single-type files that contain overlapping types).
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
// collectionFilenames(), deduplicates overlapping type definitions, and
// concatenates them into a single SDL document.
func LoadSchemaSDL() (string, error) {
	collectionOrder := collectionFilenames()

	seenTypes := make(map[string]bool)
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

		filtered := filterDuplicateTypes(content, seenTypes)
		if filtered != "" {
			parts = append(parts, filtered)
		}
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

// TODO: Refactor. This is likely 1) Not necessary 2). Could be re-written in a more simple way.
func filterDuplicateTypes(content string, seenTypes map[string]bool) string {
	matches := typeRegex.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}
	typeNameRegex := regexp.MustCompile(`^type\s+(\w+)`)
	var blocks []struct {
		typeName string
		start    int
		end      int
	}
	for _, match := range matches {
		header := content[match[0]:]
		endOfLine := strings.Index(header, "\n")
		if endOfLine == -1 {
			endOfLine = len(header)
		}
		m := typeNameRegex.FindStringSubmatch(header[:endOfLine])
		if m == nil {
			continue
		}
		blockStart := match[0]
		pos := match[0] - 1
		for pos >= 0 {
			lineEnd := pos
			for pos >= 0 && content[pos] != '\n' {
				pos--
			}
			line := strings.TrimSpace(content[pos+1 : lineEnd+1])
			if line == "" {
				pos--
				continue
			}
			if strings.HasPrefix(line, "#") {
				blockStart = pos + 1
				pos--
				continue
			}
			break
		}
		blockEnd := findClosingBrace(content, match[0])
		if blockEnd == -1 {
			blockEnd = len(content)
		}

		blocks = append(blocks, struct {
			typeName string
			start    int
			end      int
		}{m[1], blockStart, blockEnd})
	}
	var result []string
	for _, b := range blocks {
		if seenTypes[b.typeName] {
			continue
		}
		seenTypes[b.typeName] = true
		block := strings.TrimSpace(content[b.start:b.end])
		if block != "" {
			result = append(result, block)
		}
	}

	return strings.Join(result, "\n\n")
}

// TODO: Refactor. This is likely not necessary.
func findClosingBrace(content string, openPos int) int {
	depth := 0
	for i := openPos; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
