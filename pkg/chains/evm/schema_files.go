package evm

import (
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// ErrEmptyCollectionFile is returned when a collection .graphql file
// exists in the embedded FS but contains no content.
var ErrEmptyCollectionFile = errors.New("collection file is empty")

// The EVM implementation owns the .graphql schema files: only this package
// knows which files exist, how their names map to collection stems, and that
// their SDL is authored against DefaultCollectionPrefix. Embedding them here
// (instead of in a chain-agnostic package) lets CollectionFiles serve SDLs
// directly through the chains.Collections interface, so generic consumers
// never need file reads or prefix-swap knowledge.

//go:embed collections/*.graphql
var collectionFS embed.FS

// collectionSpec pairs a CollectionNames field with its file and stem name.
type collectionSpec struct {
	typeName string
	name     string
	file     string
}

// Collection SDL file names with the stem each is served under
// (CollectionFile.Name — the JSON-API cache key and CLI listing name).
// Both spellings are literal constants: the stem is never derived from the
// file name, so no filename string surgery exists in this package.
const (
	blockCollectionFile             = "block.graphql"
	blockSignatureCollectionFile    = "blockSignature.graphql"
	snapshotSignatureCollectionFile = "snapshotSignature.graphql"
	transactionCollectionFile       = "transaction.graphql"
	accessListEntryCollectionFile   = "accessListEntry.graphql"
	logCollectionFile               = "log.graphql"

	blockCollectionStem             = "block"
	blockSignatureCollectionStem    = "blockSignature"
	snapshotSignatureCollectionStem = "snapshotSignature"
	transactionCollectionStem       = "transaction"
	accessListEntryCollectionStem   = "accessListEntry"
	logCollectionStem               = "log"
)

// CollectionFiles implements chains.Collections. It returns the collection
// type names, files, and SDLs in schema-apply order, with every SDL adapted
// to the chain's prefix. Unknown collection types are structurally
// impossible — the pairs are built from the same name fields as
// AllCollections.
func (c *CollectionNames) CollectionFiles() ([]chains.CollectionFile, error) {
	specs := []collectionSpec{
		{c.Block, blockCollectionStem, blockCollectionFile},
		{c.BlockSignature, blockSignatureCollectionStem, blockSignatureCollectionFile},
		{c.SnapshotSignature, snapshotSignatureCollectionStem, snapshotSignatureCollectionFile},
		{c.Transaction, transactionCollectionStem, transactionCollectionFile},
		{c.AccessListEntry, accessListEntryCollectionStem, accessListEntryCollectionFile},
		{c.Log, logCollectionStem, logCollectionFile},
	}

	files := make([]chains.CollectionFile, 0, len(specs))
	for _, spec := range specs {
		sdl, err := readCollectionSDL(spec.file)
		if err != nil {
			return nil, err
		}
		files = append(files, chains.CollectionFile{
			TypeName: spec.typeName,
			Name:     spec.name,
			File:     spec.file,
			SDL:      strings.ReplaceAll(sdl, DefaultCollectionPrefix, c.prefix),
		})
	}
	return files, nil
}

// MergedSDL returns the full schema SDL document with all collection files
// concatenated in schema-apply order and the chain's prefix applied. The
// join itself is the shared chains.MergeSDL, so every consumer of the pairs
// produces the same document.
func (c *CollectionNames) MergedSDL() (string, error) {
	files, err := c.CollectionFiles()
	if err != nil {
		return "", err
	}
	return chains.MergeSDL(files), nil
}

// readCollectionSDL reads a single collection .graphql file and returns its
// raw content (no prefix replacement).
func readCollectionSDL(filename string) (string, error) {
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
