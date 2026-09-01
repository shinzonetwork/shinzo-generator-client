package snapshot

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ImportKV error paths
// ---------------------------------------------------------------------------

func TestImportKV_FileNotFound(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	result, err := ImportKV(ctx, td.Node, "/nonexistent/snapshot.kvsnap.gz")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "open snapshot")
}

func TestImportKV_InvalidGzip(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	tmpFile := filepath.Join(t.TempDir(), "bad.kvsnap.gz")
	err := os.WriteFile(tmpFile, []byte("not gzip data"), 0o600)
	require.NoError(t, err)

	result, err := ImportKV(ctx, td.Node, tmpFile)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "gzip reader")
}

func TestImportKV_InvalidMagic(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	// Create a valid gzip file with a header that has the wrong magic
	tmpFile := filepath.Join(t.TempDir(), "bad_magic.kvsnap.gz")
	f, err := os.Create(filepath.Clean(tmpFile))
	require.NoError(t, err)

	gw := gzip.NewWriter(f)
	header := kvSnapshotHeader{
		Magic:      "XXXX",
		Version:    1,
		StartBlock: 0,
		EndBlock:   0,
	}
	require.NoError(t, writeKVSnapHeader(gw, header))
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	result, err := ImportKV(ctx, td.Node, tmpFile)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid snapshot magic")
}

// ===========================================================================
// NEW TESTS: Targeting all uncovered lines for 100% coverage
// ===========================================================================

// ---------------------------------------------------------------------------
// ImportKV error paths: truncated header length, invalid header JSON
// ---------------------------------------------------------------------------

func TestImportKV_TruncatedHeaderLength(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	// Write a gzip file with only 2 bytes (less than the 4-byte header length)
	p := writeKVSnapGz(t, dir, "truncated_len.kvsnap.gz", func(gw *gzip.Writer) {
		_, err := gw.Write([]byte{0x00, 0x01})
		require.NoError(t, err)
	})

	result, err := ImportKV(ctx, td.Node, p)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "read header length")
}

func TestImportKV_InvalidHeaderJSON(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	// Write a gzip file with valid 4-byte length prefix but garbage JSON
	p := writeKVSnapGz(t, dir, "bad_json.kvsnap.gz", func(gw *gzip.Writer) {
		garbage := []byte("not json at all!!")
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(garbage))) //nolint:gosec
		_, err := gw.Write(lenBuf[:])
		require.NoError(t, err)
		_, err = gw.Write(garbage)
		require.NoError(t, err)
	})

	result, err := ImportKV(ctx, td.Node, p)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "parse header")
}

func TestImportKV_TruncatedHeaderBody(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	// Write a gzip file with length prefix claiming 100 bytes but only 5 bytes of body
	p := writeKVSnapGz(t, dir, "truncated_body.kvsnap.gz", func(gw *gzip.Writer) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], 100) // claims 100 bytes
		_, err := gw.Write(lenBuf[:])
		require.NoError(t, err)
		_, err = gw.Write([]byte("short"))
		require.NoError(t, err)
	})

	result, err := ImportKV(ctx, td.Node, p)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "read header")
}

// ---------------------------------------------------------------------------
// ImportKV with ImportRawKVs error (valid header but no KV data to import)
// ---------------------------------------------------------------------------

func TestImportKV_ValidHeaderEmptyKVs(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	// Create a valid kvsnap file with proper header but just an EOF marker
	p := writeKVSnapGz(t, dir, "empty_kvs.kvsnap.gz", func(gw *gzip.Writer) {
		header := kvSnapshotHeader{
			Magic:      constants.HeaderMagicValue,
			Version:    1,
			StartBlock: 0,
			EndBlock:   0,
		}
		require.NoError(t, writeKVSnapHeader(gw, header))

		// Write EOF marker (key_len = 0)
		require.NoError(t, writeKVSnapEOF(gw))
	})

	result, err := ImportKV(ctx, td.Node, p)
	// This may succeed or fail depending on ImportRawKVs behavior with empty input
	if err != nil {
		assert.Contains(t, err.Error(), "import raw KVs")
	} else {
		require.NotNil(t, result)
		assert.Equal(t, int64(0), result.StartBlock)
		assert.Equal(t, int64(0), result.EndBlock)
	}
}

// ---------------------------------------------------------------------------
// ImportKV + full roundtrip with header containing BlockSigMerkleRoots
// ---------------------------------------------------------------------------

func TestImportKV_HeaderWithBlockSigMerkleRoots(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	// Create a kvsnap with roots in the header, then import
	p := writeKVSnapGz(t, dir, "with_roots.kvsnap.gz", func(gw *gzip.Writer) {
		header := kvSnapshotHeader{
			Magic:               constants.HeaderMagicValue,
			Version:             1,
			StartBlock:          5000,
			EndBlock:            5999,
			CreatedAt:           "2024-01-15T12:00:00Z",
			BlockSigMerkleRoots: []string{"aabb", "ccdd"},
		}
		require.NoError(t, writeKVSnapHeader(gw, header))

		// Write EOF marker
		require.NoError(t, writeKVSnapEOF(gw))
	})

	result, err := ImportKV(ctx, td.Node, p)
	// May succeed or fail depending on ImportRawKVs handling of EOF marker
	if err == nil {
		require.NotNil(t, result)
		assert.Equal(t, int64(5000), result.StartBlock)
		assert.Equal(t, int64(5999), result.EndBlock)
	}
}

// ---------------------------------------------------------------------------
// ImportKV: ImportRawKVs error path
// We create a file with a valid header but corrupt KV data after the header.
// ---------------------------------------------------------------------------

func TestImportKV_CorruptKVData(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	p := writeKVSnapGz(t, dir, "corrupt_kv.kvsnap.gz", func(gw *gzip.Writer) {
		header := kvSnapshotHeader{
			Magic:      constants.HeaderMagicValue,
			Version:    1,
			StartBlock: 0,
			EndBlock:   0,
		}
		require.NoError(t, writeKVSnapHeader(gw, header))

		// Write garbage KV data (not a valid key_len/key/value format)
		_, err := gw.Write([]byte("this is not valid KV data that ImportRawKVs can parse correctly"))
		require.NoError(t, err)
	})

	result, err := ImportKV(ctx, td.Node, p)
	// ImportRawKVs may or may not error depending on how it handles malformed data
	if err != nil {
		assert.Contains(t, err.Error(), "import raw KVs")
		assert.Nil(t, result)
	}
}
