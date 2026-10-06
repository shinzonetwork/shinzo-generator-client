package schema

import (
	"errors"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errStubFiles is the sentinel the stub reports when its files read fails.
var errStubFiles = errors.New("stub collection files failure")

// stubCollections is a minimal chains.Collections standing in for a chain
// adapter with literal pairs. The facade tests in this package are
// chain-agnostic by design: SDL content, prefix swapping, and real file
// names are the chain implementation's responsibility — this package tests
// only the glue: entry mapping, cache building, join order, and error
// handling.
type stubCollections struct {
	prefix string
	files  []chains.CollectionFile
	err    error
}

func (s *stubCollections) Prefix() string { return s.prefix }

func (s *stubCollections) AllCollections() []string {
	names := make([]string, 0, len(s.files))
	for _, f := range s.files {
		names = append(names, f.TypeName)
	}
	return names
}

func (s *stubCollections) CollectionFiles() ([]chains.CollectionFile, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.files, nil
}

func (s *stubCollections) BlockCollection() string {
	return s.prefix + "__Block"
}

func (s *stubCollections) SnapshotSignatureCollection() string {
	return s.prefix + "__SnapshotSignature"
}

func (s *stubCollections) BlockSignatureCollection() string {
	return s.prefix + "__BlockSignature"
}

// testStubCollections returns the fixed three-pair fixture used by the
// facade tests. Three pairs are the minimum that proves order and the
// join separator unambiguously.
func testStubCollections() *stubCollections {
	return &stubCollections{
		prefix: "Stub__Prefix",
		files: []chains.CollectionFile{
			{TypeName: "Stub__Prefix__Alpha", Name: "alpha", File: "alpha.graphql", SDL: "type Alpha"},
			{TypeName: "Stub__Prefix__Beta", Name: "beta", File: "beta.graphql", SDL: "type Beta"},
			{TypeName: "Stub__Prefix__Gamma", Name: "gamma", File: "gamma.graphql", SDL: "type Gamma"},
		},
	}
}

func TestListCollections(t *testing.T) {
	t.Parallel()

	entries, err := ListCollections(testStubCollections())
	require.NoError(t, err)

	assert.Equal(t, []CollectionEntry{
		{Name: "alpha", TypeName: "Stub__Prefix__Alpha"},
		{Name: "beta", TypeName: "Stub__Prefix__Beta"},
		{Name: "gamma", TypeName: "Stub__Prefix__Gamma"},
	}, entries)
}

func TestPrecomputeCollectionSDLs(t *testing.T) {
	t.Parallel()

	cache, err := PrecomputeCollectionSDLs(testStubCollections())
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"alpha": "type Alpha",
		"beta":  "type Beta",
		"gamma": "type Gamma",
	}, cache)
}

func TestLoadSchemaSDLForChain_JoinsInApplyOrder(t *testing.T) {
	t.Parallel()

	sdl, err := LoadSchemaSDLForChain(testStubCollections())
	require.NoError(t, err)
	assert.Equal(t, "type Alpha\n\ntype Beta\n\ntype Gamma", sdl)
}

func TestLoadSchemaSDLForChain_NoFilesFailsLoudly(t *testing.T) {
	t.Parallel()

	sdl, err := LoadSchemaSDLForChain(&stubCollections{prefix: "Stub__Prefix"})
	assert.Empty(t, sdl)
	assert.ErrorContains(t, err, "no collection files found for prefix Stub__Prefix")
}

// TestCollectionFilesErrorPropagation pins that every facade entry point
// fails loudly when the chain's CollectionFiles read fails, wrapping the
// error with the chain prefix for context instead of degrading silently.
func TestCollectionFilesErrorPropagation(t *testing.T) {
	t.Parallel()

	t.Run("ListCollections: nil entries and wrapped error with chain prefix", func(t *testing.T) {
		t.Parallel()

		entries, err := ListCollections(&stubCollections{prefix: "Stub__Prefix", err: errStubFiles})
		assert.Nil(t, entries)
		assert.ErrorIs(t, err, errStubFiles)
		assert.Contains(t, err.Error(), "Stub__Prefix", "error must carry the chain prefix for context")
	})

	t.Run("PrecomputeCollectionSDLs: nil cache and wrapped error", func(t *testing.T) {
		t.Parallel()

		cache, err := PrecomputeCollectionSDLs(&stubCollections{prefix: "Stub__Prefix", err: errStubFiles})
		assert.Nil(t, cache)
		assert.ErrorIs(t, err, errStubFiles)
	})

	t.Run("LoadSchemaSDLForChain: empty SDL and wrapped error", func(t *testing.T) {
		t.Parallel()

		sdl, err := LoadSchemaSDLForChain(&stubCollections{prefix: "Stub__Prefix", err: errStubFiles})
		assert.Empty(t, sdl)
		assert.ErrorIs(t, err, errStubFiles)
	})
}
