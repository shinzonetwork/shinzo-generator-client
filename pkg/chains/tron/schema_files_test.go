package tron

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectionFiles_ApplyOrderAndNames pins the dependency-safe schema
// apply order and the literal file/stem pairs.
func TestCollectionFiles_ApplyOrderAndNames(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	files, err := cols.CollectionFiles()
	require.NoError(t, err)
	require.Len(t, files, 6)

	type file struct {
		typeName string
		name     string
		schema   string
	}
	got := make([]file, 0, len(files))
	for _, f := range files {
		got = append(got, file{typeName: f.TypeName, name: f.Name, schema: f.File})
	}
	assert.Equal(t, []file{
		{DefaultCollectionBlock, "block", blockCollectionFile},
		{DefaultCollectionBlockSignature, "blockSignature", blockSignatureCollectionFile},
		{DefaultCollectionSnapshotSignature, "snapshotSignature", snapshotSignatureCollectionFile},
		{DefaultCollectionTransaction, "transaction", transactionCollectionFile},
		{DefaultCollectionLog, "log", logCollectionFile},
		{DefaultCollectionInternalTransfer, "internalTransfer", internalTransferCollectionFile},
	}, got)
}

// TestCollectionFiles_PrefixSwap pins that SDLs are authored against
// DefaultCollectionPrefix and swapped to the caller's prefix with no
// remnants of the default.
func TestCollectionFiles_PrefixSwap(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Tron__Testnet")
	files, err := cols.CollectionFiles()
	require.NoError(t, err)

	for _, f := range files {
		assert.Contains(t, f.SDL, "type Tron__Testnet__")
		assert.NotContains(t, f.SDL, DefaultCollectionPrefix)
	}
}

// TestCollectionSDL_IndexesBlockNumberField pins the pruner invariant:
// every collection except SnapshotSignature declares @index on its
// block-number field.
func TestCollectionSDL_IndexesBlockNumberField(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	files, err := cols.CollectionFiles()
	require.NoError(t, err)

	indexedFields := map[string]string{
		blockCollectionFile:            "number",
		transactionCollectionFile:      "blockNumber",
		logCollectionFile:              "blockNumber",
		internalTransferCollectionFile: "blockNumber",
		blockSignatureCollectionFile:   "blockNumber",
	}

	for _, f := range files {
		field, mustIndex := indexedFields[f.File]
		if !mustIndex {
			continue
		}
		found := false
		for line := range strings.SplitSeq(f.SDL, "\n") {
			if strings.Contains(line, field+": Int") {
				assert.Contains(t, line, "@index", "%s: the %s field must carry @index", f.File, field)
				found = true
				break
			}
		}
		assert.True(t, found, "%s must declare %s: Int", f.File, field)
	}
}

// TestCollectionSDL_SnapshotSignatureUnindexed pins the one exception: the
// snapshot signature carries no block range filter for the pruner.
func TestCollectionSDL_SnapshotSignatureUnindexed(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	files, err := cols.CollectionFiles()
	require.NoError(t, err)

	var snapshotSDL string
	for _, f := range files {
		if f.File == snapshotSignatureCollectionFile {
			snapshotSDL = f.SDL
			break
		}
	}
	require.NotEmpty(t, snapshotSDL)
	assert.NotContains(t, snapshotSDL, "@index")
}

// TestMergedSDL_Join pins the full-document render: every pair in apply
// order, joined by MergeSDL.
func TestMergedSDL_Join(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Tron__Testnet")
	merged, err := cols.MergedSDL()
	require.NoError(t, err)

	files, err := cols.CollectionFiles()
	require.NoError(t, err)
	for _, f := range files {
		assert.Contains(t, merged, f.SDL)
	}
	assert.Greater(t, len(merged), len(files[0].SDL), "the document holds more than the first collection")
}
