package solana

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
	require.Len(t, files, 7)

	expected := []struct {
		typeName string
		name     string
		file     string
	}{
		{DefaultCollectionBlock, blockCollectionStem, blockCollectionFile},
		{DefaultCollectionBlockSignature, blockSignatureCollectionStem, blockSignatureCollectionFile},
		{DefaultCollectionSnapshotSignature, snapshotSignatureCollectionStem, snapshotSignatureCollectionFile},
		{DefaultCollectionTransaction, transactionCollectionStem, transactionCollectionFile},
		{DefaultCollectionInstruction, instructionCollectionStem, instructionCollectionFile},
		{DefaultCollectionTokenBalanceChange, tokenBalanceChangeCollectionStem, tokenBalanceChangeCollectionFile},
		{DefaultCollectionReward, rewardCollectionStem, rewardCollectionFile},
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
	c := NewCollectionNames("Solana__Devnet")

	files, err := c.CollectionFiles()
	require.NoError(t, err)

	for _, f := range files {
		assert.NotContains(t, f.SDL, DefaultCollectionPrefix, "SDL must not contain the default prefix (%s)", f.File)
		assert.True(t, strings.HasPrefix(f.TypeName, "Solana__Devnet__"),
			"TypeName %q must carry the custom prefix", f.TypeName)
	}

	block := files[0]
	assert.Contains(t, block.SDL, "Solana__Devnet__Block")
	// The swap must also rewrite relation targets inside the SDL, not just
	// the collection type name.
	assert.Contains(t, block.SDL, "[Solana__Devnet__Transaction] @relation")
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

func TestCollectionSDL_IndexInvariant(t *testing.T) {
	t.Parallel()

	// Every collection except SnapshotSignature must declare @index on its
	// block-number field (number on Block, blockNumber elsewhere): the
	// pruner orders and filters by it, and GetDocIDsByBlockRange queries
	// it. SnapshotSignature is the sole exception; it is not
	// block-range-filtered.
	tests := []struct {
		file        string
		expectIndex bool
	}{
		{file: blockCollectionFile, expectIndex: true},
		{file: blockSignatureCollectionFile, expectIndex: true},
		{file: snapshotSignatureCollectionFile, expectIndex: false},
		{file: transactionCollectionFile, expectIndex: true},
		{file: instructionCollectionFile, expectIndex: true},
		{file: tokenBalanceChangeCollectionFile, expectIndex: true},
		{file: rewardCollectionFile, expectIndex: true},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			sdl, err := readCollectionSDL(tt.file)
			require.NoError(t, err)
			if tt.expectIndex {
				assert.Contains(t, sdl, "@index", "SDL %s must declare the @index directive", tt.file)
			} else {
				assert.NotContains(t, sdl, "@index", "SDL %s must not declare the @index directive", tt.file)
			}
		})
	}
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
	devSDL, err := NewCollectionNames("Solana__Devnet").MergedSDL()
	require.NoError(t, err)

	assert.NotEqual(t, defaultSDL, devSDL, "merged SDL must differ per prefix")
	assert.NotContains(t, devSDL, DefaultCollectionPrefix)
	assert.Contains(t, devSDL, "Solana__Devnet__Block")
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
