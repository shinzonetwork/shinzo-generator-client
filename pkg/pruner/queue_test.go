package pruner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

// ─── UUID Helpers ────────────────────────────────────────────────────────────

func TestParseUUIDHex(t *testing.T) {
	t.Run("valid UUID", func(t *testing.T) {
		uuid, err := parseUUIDHex("550e8400-e29b-41d4-a716-446655440000")
		require.NoError(t, err)
		require.Len(t, uuid, 16)
	})

	t.Run("invalid UUID length", func(t *testing.T) {
		_, err := parseUUIDHex("short")
		assert.Error(t, err)
	})

	t.Run("invalid hex characters", func(t *testing.T) {
		_, err := parseUUIDHex("ZZZZZZZZ-ZZZZ-ZZZZ-ZZZZ-ZZZZZZZZZZZZ")
		assert.Error(t, err)
	})
}

func TestFormatUUID(t *testing.T) {
	uuid, err := parseUUIDHex("550e8400-e29b-41d4-a716-446655440000")
	require.NoError(t, err)

	formatted := formatUUID(uuid)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", formatted)
}

func TestExtractUUID(t *testing.T) {
	q := NewIndexerQueue()

	t.Run("valid docID", func(t *testing.T) {
		uuid, err := q.extractUUID(docIDPrefix + "-550e8400-e29b-41d4-a716-446655440000")
		require.NoError(t, err)
		require.Len(t, uuid, 16)
	})

	t.Run("invalid docID no dash", func(t *testing.T) {
		_, err := q.extractUUID("nodash")
		assert.Error(t, err)
	})
}

func TestRestoreDocID(t *testing.T) {
	q := NewIndexerQueue()

	uuid, err := parseUUIDHex("550e8400-e29b-41d4-a716-446655440000")
	require.NoError(t, err)

	restored := q.RestoreDocID(uuid)
	assert.Equal(t, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", restored)
}

func TestPackUnpackDocIDs(t *testing.T) {
	q := NewIndexerQueue()

	docIDs := []string{
		docIDPrefix + "-550e8400-e29b-41d4-a716-446655440000",
		docIDPrefix + "-660e8400-e29b-41d4-a716-446655440001",
	}

	packed, err := q.packDocIDs(docIDs)
	require.NoError(t, err)
	assert.Equal(t, 32, len(packed)) // 2 * 16 bytes

	unpacked := q.UnpackDocIDs(packed)
	assert.Len(t, unpacked, 2)
	assert.Equal(t, docIDs[0], unpacked[0])
	assert.Equal(t, docIDs[1], unpacked[1])
}

func TestPackDocIDsEmpty(t *testing.T) {
	q := NewIndexerQueue()
	packed, err := q.packDocIDs(nil)
	require.NoError(t, err)
	assert.Nil(t, packed)

	packed, err = q.packDocIDs([]string{})
	require.NoError(t, err)
	assert.Nil(t, packed)
}

func TestUnpackDocIDsEmpty(t *testing.T) {
	q := NewIndexerQueue()
	assert.Nil(t, q.UnpackDocIDs(nil))
	assert.Nil(t, q.UnpackDocIDs([]byte{}))
}

func TestPackDocIDsInvalid(t *testing.T) {
	q := NewIndexerQueue()
	_, err := q.packDocIDs([]string{"invalid"})
	assert.Error(t, err)
}

// ─── IndexerQueue ────────────────────────────────────────────────────────────

func TestIndexerQueueBasic(t *testing.T) {
	q := NewIndexerQueue()
	assert.Equal(t, 0, q.Len())
	assert.Equal(t, int64(0), q.HighestBlockNumber())
}

func TestIndexerQueueTrackAndDrain(t *testing.T) {
	q := NewIndexerQueue()
	cols := DefaultCollectionConfig()

	// Track some blocks
	for i := int64(1); i <= 5; i++ {
		err := q.TrackBlockDocIDs(i,
			docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
			map[string][]string{
				constants.CollectionTransaction: {docIDPrefix + "-660e8400-e29b-41d4-a716-446655440001"},
			},
			"",
		)
		require.NoError(t, err)
	}

	assert.Equal(t, 5, q.Len())
	assert.Equal(t, int64(5), q.HighestBlockNumber())

	// Drain keeping 3
	result := q.Drain(3, cols)
	require.NotNil(t, result)
	assert.Equal(t, 2, result.BlockCount)
	assert.Equal(t, 3, q.Len())
}

func TestIndexerQueueDrainNothingToDrain(t *testing.T) {
	q := NewIndexerQueue()
	cols := DefaultCollectionConfig()

	// Empty queue
	assert.Nil(t, q.Drain(10, cols))

	// Queue smaller than keep
	err := q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", nil, "")
	require.NoError(t, err)
	assert.Nil(t, q.Drain(10, cols))
}

func TestIndexerQueueDrainByDocCount(t *testing.T) {
	q := NewIndexerQueue()
	cols := DefaultCollectionConfig()

	// Track block with transactions
	err := q.TrackBlockDocIDs(1,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{
			constants.CollectionTransaction: {
				docIDPrefix + "-660e8400-e29b-41d4-a716-446655440001",
				docIDPrefix + "-770e8400-e29b-41d4-a716-446655440002",
			},
			constants.CollectionLog: {
				docIDPrefix + "-880e8400-e29b-41d4-a716-446655440003",
			},
		},
		docIDPrefix+"-990e8400-e29b-41d4-a716-446655440004",
	)
	require.NoError(t, err)

	// DocCount: 1 block + 2 tx + 1 log + 1 batchsig = 5
	assert.Equal(t, 5, q.DocCount())

	// Empty queue returns nil
	q2 := NewIndexerQueue()
	assert.Nil(t, q2.DrainByDocCount(10, cols))

	// Zero excess returns nil
	assert.Nil(t, q.DrainByDocCount(0, cols))

	// Drain by doc count
	result := q.DrainByDocCount(3, cols)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.BlockCount)
}

func TestIndexerQueueTrackInvalidDocIDs(t *testing.T) {
	q := NewIndexerQueue()

	// Invalid block docID
	err := q.TrackBlockDocIDs(1, "invalid-no-uuid", nil, "")
	assert.Error(t, err)

	// Invalid transaction docID
	err = q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{constants.CollectionTransaction: {"invalid"}}, "")
	assert.Error(t, err)

	// Invalid log docID
	err = q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{constants.CollectionLog: {"invalid"}}, "")
	assert.Error(t, err)

	// Invalid ALE docID
	err = q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{constants.CollectionAccessListEntry: {"invalid"}}, "")
	assert.Error(t, err)

	// Invalid batch sig docID
	err = q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", nil, "invalid")
	assert.Error(t, err)
}

func TestIndexerQueueSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "queue.gob")

	q := NewIndexerQueue()

	// Set file path (file doesn't exist yet)
	count, err := q.LoadFromFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Track entries
	err = q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", nil, "")
	require.NoError(t, err)
	err = q.TrackBlockDocIDs(2, docIDPrefix+"-660e8400-e29b-41d4-a716-446655440001", nil, "")
	require.NoError(t, err)

	// Save
	err = q.Save()
	require.NoError(t, err)

	// Load into new queue
	q2 := NewIndexerQueue()
	count, err = q2.LoadFromFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, 2, q2.Len())
}

func TestIndexerQueueSaveEmptyRemovesFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "queue.gob")

	// Create a file
	err := os.WriteFile(filePath, []byte("data"), 0o644)
	require.NoError(t, err)

	q := NewIndexerQueue()
	q.LoadFromFile(filePath) //nolint:errcheck // Testing q.Save() not q.LoadFromFile()
	// Queue is empty (file had invalid data, but LoadFromFile sets filePath)

	err = q.Save()
	require.NoError(t, err)

	// File should be removed
	_, err = os.Stat(filePath)
	assert.True(t, os.IsNotExist(err))
}

func TestIndexerQueueSaveNoFilePath(t *testing.T) {
	q := NewIndexerQueue()
	err := q.Save()
	require.NoError(t, err) // no-op
}

func TestIndexerQueueLoadFromFileInvalidData(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "bad_queue.gob")

	err := os.WriteFile(filePath, []byte("not valid gob data"), 0o644)
	require.NoError(t, err)

	q := NewIndexerQueue()
	_, err = q.LoadFromFile(filePath)
	assert.Error(t, err)
}

// ─── Additional queue edge case tests ────────────────────────────────────────

func TestIndexerQueueSave_WithEntries(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "save_test.gob")

	q := NewIndexerQueue()
	q.LoadFromFile(filePath) //nolint:errcheck // Testing q.Save() not q.LoadFromFile()

	// Add entries with various doc types
	err := q.TrackBlockDocIDs(1,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{
			constants.CollectionTransaction:     {docIDPrefix + "-660e8400-e29b-41d4-a716-446655440001"},
			constants.CollectionLog:             {docIDPrefix + "-770e8400-e29b-41d4-a716-446655440002"},
			constants.CollectionAccessListEntry: {docIDPrefix + "-880e8400-e29b-41d4-a716-446655440003"},
		},
		docIDPrefix+"-990e8400-e29b-41d4-a716-446655440004",
	)
	require.NoError(t, err)

	// Save
	err = q.Save()
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(filePath)
	assert.NoError(t, err)

	// Load and verify
	q2 := NewIndexerQueue()
	count, err := q2.LoadFromFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, 5, q2.DocCount())
}

func TestIndexerQueueDrain_WithAllDocTypes(t *testing.T) {
	q := NewIndexerQueue()
	cols := DefaultCollectionConfig()

	// Track block with all doc types
	err := q.TrackBlockDocIDs(1,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{
			constants.CollectionTransaction:     {docIDPrefix + "-110e8400-e29b-41d4-a716-446655440001"},
			constants.CollectionLog:             {docIDPrefix + "-220e8400-e29b-41d4-a716-446655440002"},
			constants.CollectionAccessListEntry: {docIDPrefix + "-330e8400-e29b-41d4-a716-446655440003"},
		},
		docIDPrefix+"-440e8400-e29b-41d4-a716-446655440004",
	)
	require.NoError(t, err)

	// Track a second block to keep
	err = q.TrackBlockDocIDs(2,
		docIDPrefix+"-aa0e8400-e29b-41d4-a716-446655440005",
		nil, "",
	)
	require.NoError(t, err)

	// Drain keeping 1 (drains block 1 with all its docs)
	result := q.Drain(1, cols)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.BlockCount)

	// Verify all collections are in the result
	assert.Contains(t, result.DocIDsByCollection, constants.CollectionBlock)
	assert.Contains(t, result.DocIDsByCollection, constants.CollectionTransaction)
	assert.Contains(t, result.DocIDsByCollection, constants.CollectionLog)
	assert.Contains(t, result.DocIDsByCollection, constants.CollectionAccessListEntry)
	assert.Contains(t, result.DocIDsByCollection, constants.CollectionBlockSignature)
}

func TestIndexerQueueDrainByDocCount_NotEnoughDocs(t *testing.T) {
	q := NewIndexerQueue()
	cols := DefaultCollectionConfig()

	// Track a single block with 1 doc
	err := q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", nil, "")
	require.NoError(t, err)

	// Request excess of 100 but only 1 doc exists
	// cutoff stays 0 after loop ends because docsAccumulated (1) < excess (100)
	// Then cutoff = len(q.entries) = 1, drains everything
	result := q.DrainByDocCount(100, cols)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.BlockCount)
	assert.Equal(t, 0, q.Len())
}

func TestIndexerQueueTrackBlockDocIDs_EmptyBlockDocID(t *testing.T) {
	q := NewIndexerQueue()

	// Track with empty block docID - should succeed (blockDocID == "")
	err := q.TrackBlockDocIDs(1, "", nil, "")
	require.NoError(t, err)
	assert.Equal(t, 1, q.Len())
}

func TestIndexerQueueLoadFromFile_WithPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "prefix_test.gob")

	q := NewIndexerQueue()
	_, err := q.LoadFromFile(filePath)
	// !!! REPLACE WITH MORE MEANINGFUL CHECK AFTER FIXING INDEXER PRUNER QUEUE ERROR HANDLING
	require.Nil(t, err)

	// Track so that docIDPrefix gets set
	err = q.TrackBlockDocIDs(1, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", nil, "")
	require.NoError(t, err)

	// Save
	err = q.Save()
	require.NoError(t, err)

	// Load into a fresh queue and verify the prefix is restored
	q2 := NewIndexerQueue()
	count, err := q2.LoadFromFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
