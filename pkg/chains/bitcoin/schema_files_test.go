package bitcoin

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectionFiles(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	files, err := c.CollectionFiles()
	require.NoError(t, err)
	require.Len(t, files, 6)

	expected := []struct {
		typeName string
		name     string
		file     string
	}{
		{DefaultCollectionBlock, blockCollectionStem, blockCollectionFile},
		{DefaultCollectionBlockSignature, blockSignatureCollectionStem, blockSignatureCollectionFile},
		{DefaultCollectionSnapshotSignature, snapshotSignatureCollectionStem, snapshotSignatureCollectionFile},
		{DefaultCollectionTransaction, transactionCollectionStem, transactionCollectionFile},
		{DefaultCollectionTxInput, txInputCollectionStem, txInputCollectionFile},
		{DefaultCollectionTxOutput, txOutputCollectionStem, txOutputCollectionFile},
	}

	for i, tt := range expected {
		assert.Equal(t, tt.typeName, files[i].TypeName)
		assert.Equal(t, tt.name, files[i].Name)
		assert.Equal(t, tt.file, files[i].File)
		assert.NotEmpty(t, files[i].SDL, "SDL must be non-empty for %s", tt.file)
	}
}

func TestCollectionFiles_OrderMatchesAllCollections(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	files, err := c.CollectionFiles()
	require.NoError(t, err)

	typeNames := make([]string, 0, len(files))
	for _, f := range files {
		typeNames = append(typeNames, f.TypeName)
	}
	assert.Equal(t, c.AllCollections(), typeNames)
}

func TestCollectionFiles_CustomPrefixSwapsSDL(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames("Bitcoin__Signet")

	files, err := c.CollectionFiles()
	require.NoError(t, err)

	for _, f := range files {
		assert.NotContains(t, f.SDL, DefaultCollectionPrefix, "SDL must not contain the default prefix (%s)", f.File)
		assert.True(t, strings.HasPrefix(f.TypeName, "Bitcoin__Signet__"),
			"TypeName %q must carry the custom prefix", f.TypeName)
	}

	block := files[0]
	assert.Contains(t, block.SDL, "Bitcoin__Signet__Block")
	// The swap must also rewrite relation targets inside the SDL, not just
	// the collection type name.
	assert.Contains(t, block.SDL, "Bitcoin__Signet__Transaction")
	assert.Contains(t, block.SDL, "[Bitcoin__Signet__Transaction] @relation")

	transaction := files[3]
	assert.Contains(t, transaction.SDL, "Bitcoin__Signet__Block @relation")
	assert.Contains(t, transaction.SDL, "[Bitcoin__Signet__TxInput] @relation")
	assert.Contains(t, transaction.SDL, "[Bitcoin__Signet__TxOutput] @relation")
}

func TestCollectionFiles_DefaultPrefixRetainsSDL(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	files, err := c.CollectionFiles()
	require.NoError(t, err)

	raw, err := readCollectionSDL(blockCollectionFile)
	require.NoError(t, err)

	assert.Equal(t, raw, files[0].SDL, "SDL must be byte-identical to the embedded file for the default prefix")
}

// TestCollectionFiles_IndexInvariant pins the pruner's block-number index
// contract: every collection except SnapshotSignature declares @index on
// its block-number field (number on Block, blockNumber elsewhere), so the
// pruner's ordering and GetDocIDsByBlockRange range filters are served.
// SnapshotSignature has no block-number field and is the sole exception.
// Transaction is the one carve-out inside that contract: transaction
// lookups run through the indexed blockHash, so its blockNumber field
// carries no index.
func TestCollectionFiles_IndexInvariant(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	files, err := c.CollectionFiles()
	require.NoError(t, err)

	for _, f := range files {
		if f.File == snapshotSignatureCollectionFile {
			continue
		}
		wanted := "blockNumber: Int @index"
		if f.File == blockCollectionFile {
			wanted = "number: Int @index"
		}
		if f.File == transactionCollectionFile {
			assert.NotContains(t, f.SDL, wanted,
				"%s must not index the block-number field", f.File)
			assert.Contains(t, f.SDL, "blockNumber: Int",
				"%s must keep the block-number field", f.File)
			assert.Contains(t, f.SDL, "blockHash: String @index",
				"%s must index the blockHash field", f.File)
			continue
		}
		assert.Contains(t, f.SDL, wanted,
			"%s must declare @index on its block-number field", f.File)
	}

	snapshot := files[2]
	assert.NotContains(t, snapshot.SDL, "@index",
		"SnapshotSignature is the sole exemption from the index invariant")
}

// TestCollectionFiles_BlockIdentityIndex pins the block document's identity
// fields: hash carries a unique index so no two block documents can share a
// content hash, and number stays the generic stack's query surface (the
// reorg roller, seed check, and pruner filter Block by that name).
func TestCollectionFiles_BlockIdentityIndex(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	files, err := c.CollectionFiles()
	require.NoError(t, err)

	block := files[0]
	assert.Contains(t, block.SDL, "hash: String @index(unique: true)")
	assert.Contains(t, block.SDL, "number: Int @index")
}

func TestMergedSDL_ContainsAllCollectionTypes(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	sdl, err := c.MergedSDL()
	require.NoError(t, err)
	assert.NotEmpty(t, sdl)

	for _, typeName := range DefaultCollections() {
		assert.Contains(t, sdl, typeName)
	}
}

func TestMergedSDL_ReplacesPrefix(t *testing.T) {
	t.Parallel()
	defaultSDL, err := NewCollectionNames(DefaultCollectionPrefix).MergedSDL()
	require.NoError(t, err)
	signetSDL, err := NewCollectionNames("Bitcoin__Signet").MergedSDL()
	require.NoError(t, err)

	assert.NotEqual(t, defaultSDL, signetSDL, "merged SDL must differ per prefix")
	assert.NotContains(t, signetSDL, DefaultCollectionPrefix)
	assert.Contains(t, signetSDL, "Bitcoin__Signet__Block")
}

func TestMergedSDL_Deterministic(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	sdl1, err := c.MergedSDL()
	require.NoError(t, err)
	sdl2, err := c.MergedSDL()
	require.NoError(t, err)
	assert.Equal(t, sdl1, sdl2, "merged SDL must be identical on repeated calls")
}

func TestMergedSDL_DefaultPrefixByteIdentity(t *testing.T) {
	t.Parallel()
	c := NewCollectionNames(DefaultCollectionPrefix)

	merged, err := c.MergedSDL()
	require.NoError(t, err)

	// Pins the join contract: the merged document is the raw embedded files
	// concatenated in schema-apply order, and the default-prefix swap is the
	// identity operation.
	files, err := c.CollectionFiles()
	require.NoError(t, err)
	raws := make([]string, 0, len(files))
	for _, f := range files {
		raw, err := readCollectionSDL(f.File)
		require.NoError(t, err, "read %s", f.File)
		raws = append(raws, raw)
	}
	assert.Equal(t, strings.Join(raws, "\n\n"), merged)
}
