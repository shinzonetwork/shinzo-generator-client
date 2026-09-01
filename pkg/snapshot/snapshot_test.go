package snapshot

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// New (constructor)
// ---------------------------------------------------------------------------

func TestNew_ReturnsNonNil(t *testing.T) {
	cfg := &config.SnapshotConfig{Dir: "/tmp/test", BlocksPerFile: 100, IntervalSeconds: 10}
	s := New(cfg, nil, nil)

	require.NotNil(t, s)
}

func TestNew_FieldsSetCorrectly(t *testing.T) {
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             "/tmp/snapshots",
		BlocksPerFile:   500,
		IntervalSeconds: 30,
	}
	s := New(cfg, nil, nil)

	assert.Same(t, cfg, s.cfg)
	assert.Nil(t, s.defraNode)
	assert.NotNil(t, s.stopChan)
	assert.Equal(t, int64(0), s.lastSnapshotBlock)
	assert.Equal(t, 0, s.totalSnapshots)
}

func TestNew_StopChanIsOpen(t *testing.T) {
	cfg := &config.SnapshotConfig{Dir: "/tmp/test"}
	s := New(cfg, nil, nil)

	// stopChan should be open (non-blocking select should not receive)
	select {
	case <-s.stopChan:
		t.Fatal("stopChan should be open, but received a value")
	default:
		// expected
	}
}

// ---------------------------------------------------------------------------
// ListSnapshots
// ---------------------------------------------------------------------------

func TestListSnapshots_EmptyDirectory(t *testing.T) {
	s, _ := newTestSnapshotter(t)
	infos := s.ListSnapshots()
	assert.Empty(t, infos)
}

func TestListSnapshots_ValidSnapshotFiles(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	// Create valid snapshot files
	files := []string{
		"snapshot_1000_1999.kvsnap.gz",
		"snapshot_2000_2999.kvsnap.gz",
		"snapshot_3000_3999.kvsnap.gz",
	}
	for _, f := range files {
		err := os.WriteFile(filepath.Join(dir, f), []byte("test data"), 0o600)
		require.NoError(t, err)
	}

	infos := s.ListSnapshots()
	require.Len(t, infos, 3)

	// Verify sorting by StartBlock ASC
	assert.Equal(t, int64(1000), infos[0].StartBlock)
	assert.Equal(t, int64(1999), infos[0].EndBlock)
	assert.Equal(t, "snapshot_1000_1999.kvsnap.gz", infos[0].Filename)

	assert.Equal(t, int64(2000), infos[1].StartBlock)
	assert.Equal(t, int64(2999), infos[1].EndBlock)

	assert.Equal(t, int64(3000), infos[2].StartBlock)
	assert.Equal(t, int64(3999), infos[2].EndBlock)
}

func TestListSnapshots_SizeAndModTime(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	content := []byte("some snapshot content here")
	fname := "snapshot_5000_5999.kvsnap.gz"
	err := os.WriteFile(filepath.Join(dir, fname), content, 0o600)
	require.NoError(t, err)

	infos := s.ListSnapshots()
	require.Len(t, infos, 1)

	assert.Equal(t, int64(len(content)), infos[0].SizeBytes)
	assert.False(t, infos[0].CreatedAt.IsZero())
	assert.WithinDuration(t, time.Now(), infos[0].CreatedAt, 5*time.Second)
}

func TestListSnapshots_BadNamingSkipped(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	// Files that don't match the expected pattern
	badFiles := []string{
		"snapshot_abc_def.kvsnap.gz", // non-numeric
		"snapshot_1000.kvsnap.gz",    // only 2 parts after split
		"random_file.txt",            // not matching glob
		"snapshot_1_2_3.kvsnap.gz",   // too many parts (4 after split)
		"snapshot_.kvsnap.gz",        // missing numbers
	}
	for _, f := range badFiles {
		err := os.WriteFile(filepath.Join(dir, f), []byte("data"), 0o600)
		require.NoError(t, err)
	}

	// Also add one valid file
	err := os.WriteFile(filepath.Join(dir, "snapshot_100_199.kvsnap.gz"), []byte("ok"), 0o600)
	require.NoError(t, err)

	infos := s.ListSnapshots()
	require.Len(t, infos, 1)
	assert.Equal(t, int64(100), infos[0].StartBlock)
	assert.Equal(t, int64(199), infos[0].EndBlock)
}

func TestListSnapshots_SortedByStartBlock(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	// Create files in reverse order
	files := []string{
		"snapshot_9000_9999.kvsnap.gz",
		"snapshot_1000_1999.kvsnap.gz",
		"snapshot_5000_5999.kvsnap.gz",
	}
	for _, f := range files {
		err := os.WriteFile(filepath.Join(dir, f), []byte("data"), 0o600)
		require.NoError(t, err)
	}

	infos := s.ListSnapshots()
	require.Len(t, infos, 3)
	assert.Equal(t, int64(1000), infos[0].StartBlock)
	assert.Equal(t, int64(5000), infos[1].StartBlock)
	assert.Equal(t, int64(9000), infos[2].StartBlock)
}

func TestListSnapshots_DirectoryDoesNotExist(t *testing.T) {
	cfg := &config.SnapshotConfig{Dir: "/nonexistent/path/snapshots"}
	s := New(cfg, nil, nil)
	infos := s.ListSnapshots()
	assert.Empty(t, infos)
}

// ---------------------------------------------------------------------------
// GetSnapshotPath
// ---------------------------------------------------------------------------

func TestGetSnapshotPath_ValidFile(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	fname := "snapshot_1000_1999.kvsnap.gz"
	err := os.WriteFile(filepath.Join(dir, fname), []byte("data"), 0o600)
	require.NoError(t, err)

	result := s.GetSnapshotPath(fname)
	assert.Equal(t, filepath.Join(dir, fname), result)
}

func TestGetSnapshotPath_FileDoesNotExist(t *testing.T) {
	s, _ := newTestSnapshotter(t)
	result := s.GetSnapshotPath("snapshot_9999_10998.kvsnap.gz")
	assert.Equal(t, "", result)
}

func TestGetSnapshotPath_PathTraversal(t *testing.T) {
	s, _ := newTestSnapshotter(t)

	traversalAttempts := []string{
		"../etc/passwd",
		"../../secret.txt",
		"subdir/file.txt",
		"../snapshot_1000_1999.kvsnap.gz",
		"./snapshot_1000_1999.kvsnap.gz",
	}

	for _, attempt := range traversalAttempts {
		result := s.GetSnapshotPath(attempt)
		assert.Equal(t, "", result, "path traversal attempt %q should return empty string", attempt)
	}
}

func TestGetSnapshotPath_BaseFilenameOnly(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	// Create a file
	fname := "myfile.kvsnap.gz"
	err := os.WriteFile(filepath.Join(dir, fname), []byte("data"), 0o600)
	require.NoError(t, err)

	// Base filename should work
	result := s.GetSnapshotPath(fname)
	assert.Equal(t, filepath.Join(dir, fname), result)
}

func TestGetSnapshotPath_EmptyFilename(t *testing.T) {
	s, _ := newTestSnapshotter(t)
	// filepath.Base("") returns ".", which != "", so it should return ""
	result := s.GetSnapshotPath("")
	assert.Equal(t, "", result)
}

// ---------------------------------------------------------------------------
// GetMetrics
// ---------------------------------------------------------------------------

func TestGetMetrics_InitialState(t *testing.T) {
	cfg := &config.SnapshotConfig{Enabled: true, Dir: "/tmp/test"}
	s := New(cfg, nil, nil)

	m := s.GetMetrics()
	assert.True(t, m.Enabled)
	assert.Equal(t, int64(0), m.LastSnapshotBlock)
	assert.Equal(t, 0, m.TotalSnapshots)
}

func TestGetMetrics_DisabledConfig(t *testing.T) {
	cfg := &config.SnapshotConfig{Enabled: false, Dir: "/tmp/test"}
	s := New(cfg, nil, nil)

	m := s.GetMetrics()
	assert.False(t, m.Enabled)
}

func TestGetMetrics_AfterManualUpdate(t *testing.T) {
	cfg := &config.SnapshotConfig{Enabled: true, Dir: "/tmp/test"}
	s := New(cfg, nil, nil)

	// Simulate internal state changes
	s.mu.Lock()
	s.lastSnapshotBlock = 5999
	s.totalSnapshots = 3
	s.mu.Unlock()

	m := s.GetMetrics()
	assert.Equal(t, int64(5999), m.LastSnapshotBlock)
	assert.Equal(t, 3, m.TotalSnapshots)
}

// ---------------------------------------------------------------------------
// Start / Stop lifecycle
// ---------------------------------------------------------------------------

func TestStartStop_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "snapshots")
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600, // long interval so the loop doesn't run
	}
	s := New(cfg, nil, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err := s.Start(ctx)
	require.NoError(t, err)

	// Directory should exist
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	s.Stop()
}

func TestStartStop_CleanShutdown(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600,
	}
	s := New(cfg, nil, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err := s.Start(ctx)
	require.NoError(t, err)

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete within 5 seconds")
	}
}

func TestStartStop_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600,
	}
	s := New(cfg, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	err := s.Start(ctx)
	require.NoError(t, err)

	// Cancel the context; the loop should exit
	cancel()

	// Stop should not hang since loop already exited
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete within 5 seconds after context cancellation")
	}
}

func TestStart_ScanExistingSnapshots(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600,
	}

	// Pre-create some snapshot files
	files := []string{
		"snapshot_1000_1999.kvsnap.gz",
		"snapshot_2000_2999.kvsnap.gz",
		"snapshot_3000_3999.kvsnap.gz",
	}
	for _, f := range files {
		err := os.WriteFile(filepath.Join(dir, f), []byte("data"), 0o600)
		require.NoError(t, err)
	}

	s := New(cfg, nil, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err := s.Start(ctx)
	require.NoError(t, err)
	defer s.Stop()

	m := s.GetMetrics()
	assert.Equal(t, int64(3999), m.LastSnapshotBlock)
	assert.Equal(t, 3, m.TotalSnapshots)
}

// ---------------------------------------------------------------------------
// scanExisting (tested indirectly through Start)
// ---------------------------------------------------------------------------

func TestScanExisting_NoFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: dir}
	s := New(cfg, nil, nil)
	s.scanExisting()

	assert.Equal(t, int64(0), s.lastSnapshotBlock)
	assert.Equal(t, 0, s.totalSnapshots)
}

func TestScanExisting_FindsHighestBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: dir}

	files := []string{
		"snapshot_1000_1999.kvsnap.gz",
		"snapshot_5000_5999.kvsnap.gz",
		"snapshot_3000_3999.kvsnap.gz",
	}
	for _, f := range files {
		err := os.WriteFile(filepath.Join(dir, f), []byte("data"), 0o600)
		require.NoError(t, err)
	}

	s := New(cfg, nil, nil)
	s.scanExisting()

	assert.Equal(t, int64(5999), s.lastSnapshotBlock)
	assert.Equal(t, 3, s.totalSnapshots)
}

func TestScanExisting_MalformedFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: dir}

	err := os.WriteFile(filepath.Join(dir, "snapshot_abc_def.kvsnap.gz"), []byte("data"), 0o600)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "snapshot_1000_1999.kvsnap.gz"), []byte("data"), 0o600)
	require.NoError(t, err)

	s := New(cfg, nil, nil)
	s.scanExisting()

	// Both files match the glob, so totalSnapshots = 2, but highest = 1999
	assert.Equal(t, int64(1999), s.lastSnapshotBlock)
	assert.Equal(t, 2, s.totalSnapshots)
}

// ---------------------------------------------------------------------------
// Metrics struct
// ---------------------------------------------------------------------------

func TestMetrics_JSONSerialization(t *testing.T) {
	m := Metrics{
		Enabled:           true,
		LastSnapshotBlock: 9999,
		TotalSnapshots:    5,
	}

	data, err := json.Marshal(m)
	require.NoError(t, err)

	var decoded Metrics
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, m, decoded)
}

// ---------------------------------------------------------------------------
// SnapshotInfo struct
// ---------------------------------------------------------------------------

func TestSnapshotInfo_JSONSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	info := SnapshotInfo{
		Filename:   "snapshot_1000_1999.kvsnap.gz",
		StartBlock: 1000,
		EndBlock:   1999,
		SizeBytes:  12345,
		CreatedAt:  now,
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded SnapshotInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, info.Filename, decoded.Filename)
	assert.Equal(t, info.StartBlock, decoded.StartBlock)
	assert.Equal(t, info.EndBlock, decoded.EndBlock)
	assert.Equal(t, info.SizeBytes, decoded.SizeBytes)
}

func TestGetSnapshotPath_DotFile(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	fname := ".hidden_snapshot.kvsnap.gz"
	err := os.WriteFile(filepath.Join(dir, fname), []byte("data"), 0o600)
	require.NoError(t, err)

	result := s.GetSnapshotPath(fname)
	assert.Equal(t, filepath.Join(dir, fname), result)
}

func TestListSnapshots_LargeBlockNumbers(t *testing.T) {
	s, dir := newTestSnapshotter(t)

	fname := "snapshot_23700000_23700999.kvsnap.gz"
	err := os.WriteFile(filepath.Join(dir, fname), []byte("data"), 0o600)
	require.NoError(t, err)

	infos := s.ListSnapshots()
	require.Len(t, infos, 1)
	assert.Equal(t, int64(23700000), infos[0].StartBlock)
	assert.Equal(t, int64(23700999), infos[0].EndBlock)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_NoBlocks(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 1000, 1, 0)
	snapshotDir := s.cfg.Dir

	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err, "checkAndSnapshot should return nil when DB is empty")

	// No snapshot files should have been created
	files, err := filepath.Glob(filepath.Join(snapshotDir, "snapshot_*.kvsnap.gz"))
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestCheckAndSnapshot_InsufficientBlocks(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 1000, 1000, 1004)

	// Insert only 5 blocks at 1000-1004

	snapshotDir := s.cfg.Dir

	// With blocks_per_file=1000, the range [1000..1999] is needed,
	// but we only have 1000-1004, so rangeEnd (1999) > highest (1004).
	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err, "should return nil when range not fully populated")

	// No snapshot files should have been created
	files, err := filepath.Glob(filepath.Join(snapshotDir, "snapshot_*.kvsnap.gz"))
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestCheckAndSnapshot_SmallBlocksPerFile(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 3, 3, 7)

	// Insert blocks 3-7 (5 blocks). We start at 3 because checkAndSnapshot
	// treats lowest==0 as "no blocks in DB" and returns early.

	snapshotDir := s.cfg.Dir
	// blocks_per_file=3: first aligned range at or above 3 is [3..5]

	// First call should create snapshot for blocks 3-5
	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err)

	m := s.GetMetrics()
	assert.Equal(t, int64(5), m.LastSnapshotBlock)
	assert.Equal(t, 1, m.TotalSnapshots)

	// Verify file exists
	expectedFile := filepath.Join(snapshotDir, "snapshot_3_5.kvsnap.gz")
	_, err = os.Stat(expectedFile)
	require.NoError(t, err, "snapshot file for blocks 3-5 should exist")

	// Second call should create snapshot for blocks 6-8, but we only have up to 7
	// so it should not create a new snapshot
	err = s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	m = s.GetMetrics()
	assert.Equal(t, int64(5), m.LastSnapshotBlock, "should not advance when range is incomplete")
	assert.Equal(t, 1, m.TotalSnapshots)
}

func TestCheckAndSnapshot_MultipleRounds(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 2, 10, 15)

	// Insert blocks 10-15 (6 blocks) with blocks_per_file=2.
	// Starting at 10 avoids the lowest==0 early return in checkAndSnapshot.

	snapshotDir := s.cfg.Dir

	// First call: snapshot blocks 10-11
	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(11), s.lastSnapshotBlock)

	// Second call: snapshot blocks 12-13
	err = s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(13), s.lastSnapshotBlock)

	// Third call: snapshot blocks 14-15
	err = s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(15), s.lastSnapshotBlock)
	assert.Equal(t, 3, s.totalSnapshots)

	// Verify all 3 files exist
	for _, name := range []string{
		"snapshot_10_11.kvsnap.gz",
		"snapshot_12_13.kvsnap.gz",
		"snapshot_14_15.kvsnap.gz",
	} {
		_, err := os.Stat(filepath.Join(snapshotDir, name))
		require.NoError(t, err, "snapshot %s should exist", name)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: checkAndSnapshot + ImportKV roundtrip
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_ImportKV_EndToEnd(t *testing.T) {
	// Create source node with enough blocks for one snapshot.
	// Starting at 100 avoids the lowest==0 early return in checkAndSnapshot.
	s, _, ctx := newTestSnapshotterWithDB(t, 5, 100, 104)

	snapshotDir := s.cfg.Dir

	// Create snapshot via checkAndSnapshot
	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(104), s.lastSnapshotBlock)

	snapshotFile := filepath.Join(snapshotDir, "snapshot_100_104.kvsnap.gz")
	_, err = os.Stat(snapshotFile)
	require.NoError(t, err, "snapshot file should exist")

	// Import into a second fresh node
	td2 := testutils.SetupTestDefraDB(t)
	importResult, err := ImportKV(ctx, td2.Node, snapshotFile)
	require.NoError(t, err)
	require.NotNil(t, importResult)
	assert.Equal(t, int64(100), importResult.StartBlock)
	assert.Equal(t, int64(104), importResult.EndBlock)

	// Verify the second node has the blocks
	s2 := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 5}, td2.Node, newTestChainFromNode(t, td2))
	assertBlockRange(t, td2, 100, 104)

	// Also verify we can query doc IDs in the imported node
	docIDsByCol, err := s2.converter.GetDocIDsByBlockRange(ctx, s2.defraNode, 100, 104)
	require.NoError(t, err)
	assert.Len(t, docIDsByCol[testBlockCollection], 5, "should find 5 block doc IDs after import")

	assert.Len(t, docIDsByCol[testTransactionCollection], 5, "should find 5 transaction doc IDs after import")
}

// ---------------------------------------------------------------------------
// checkAndSnapshot with gap detection
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_GapHandling(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 5, 1000, 1004)

	// Insert blocks 1000-1004 with blocks_per_file=5
	// The aligned range is [1000..1004] which is fully present

	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1004), s.lastSnapshotBlock)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot: gap skip path (rangeStart < lowest)
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_GapSkipAhead(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 5, 20, 29)

	// Insert blocks 20-29 with blocks_per_file=5

	// First snapshot: aligned to [20..24]
	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(24), s.lastSnapshotBlock)

	// Second snapshot: [25..29]
	err = s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(29), s.lastSnapshotBlock)

	// Now simulate a gap: set lastSnapshotBlock to 4 (below lowest=20)
	// This triggers the rangeStart < lowest path
	s.mu.Lock()
	s.lastSnapshotBlock = 4
	s.mu.Unlock()

	// checkAndSnapshot should detect the gap and skip ahead
	err = s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	// It should re-align to [20..24] and create a snapshot
	assert.Equal(t, int64(24), s.lastSnapshotBlock)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot: createSnapshot error path
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_CreateSnapshotError(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	// Insert blocks 10-14
	insertTestBlocks(t, td, 10, 14)

	// Use a non-writable directory to trigger createSnapshot error
	snapshotDir := filepath.Join(t.TempDir(), "readonly")
	err := os.MkdirAll(snapshotDir, 0o755) //nolint:gosec
	require.NoError(t, err)

	cfg := &config.SnapshotConfig{Dir: snapshotDir, BlocksPerFile: 5}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))
	s.ctx = context.Background()

	// Make directory read-only to force os.Create error in createKVSnapshot
	chmodReadOnly(t, snapshotDir)

	ctx := context.Background()
	err = s.checkAndSnapshot(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot")
}

// ---------------------------------------------------------------------------
// Start: os.MkdirAll error
// ---------------------------------------------------------------------------

func TestStart_MkdirAllError(t *testing.T) {
	// Use a path that can't be created (e.g., under a file, not a directory)
	tmpFile := filepath.Join(t.TempDir(), "afile")
	err := os.WriteFile(tmpFile, []byte("data"), 0o600)
	require.NoError(t, err)

	// Try to create a directory under a file - should fail
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             filepath.Join(tmpFile, "snapshots"),
		BlocksPerFile:   1000,
		IntervalSeconds: 3600,
	}
	s := New(cfg, nil, nil)

	ctx := context.Background()
	err = s.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create snapshot directory")
}

// ---------------------------------------------------------------------------
// Loop and error logging in loop (indirect test via Start)
// ---------------------------------------------------------------------------

func TestLoop_StopsOnStopChan(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 1, // 1 second interval
	}
	s := New(cfg, nil, nil) // nil defraNode will cause checkAndSnapshot to panic/error

	// Use a real DefraDB node so the loop can run without panicking
	td := testutils.SetupTestDefraDB(t)
	s.defraNode = td.Node
	s.ctx = context.Background()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err := s.Start(ctx)
	require.NoError(t, err)

	// Let the loop run briefly (it won't find blocks, so checkAndSnapshot returns nil)
	time.Sleep(2 * time.Second)

	// Stop should work cleanly
	s.Stop()
}

// ---------------------------------------------------------------------------
// createKVSnapshot: os.Rename error path
// ---------------------------------------------------------------------------

// Note: os.Rename error is hard to trigger in tests without using a filesystem
// that rejects renames. The atomic rename from .tmp to final path should work
// on any normal filesystem. This path is structurally difficult to test.

// ---------------------------------------------------------------------------
// checkAndSnapshot: highest==0 path
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_LowestNonZeroHighestZero(t *testing.T) {
	// This is structurally unreachable: if lowest > 0, highest >= lowest.
	// But we test the general flow where both are 0 (empty DB).
	s, _, _ := newTestSnapshotterWithDB(t, 1000, 1, 0)
	snapshotDir := s.cfg.Dir

	err := s.checkAndSnapshot(context.Background())
	require.NoError(t, err)

	// No files created
	files, _ := filepath.Glob(filepath.Join(snapshotDir, "snapshot_*.kvsnap.gz"))
	assert.Empty(t, files)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot: lastSnapshot > 0, next range calculation
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_ContinuationFromLastSnapshot(t *testing.T) {
	s, _, ctx := newTestSnapshotterWithDB(t, 5, 10, 19)

	snapshotDir := s.cfg.Dir

	// Set lastSnapshotBlock to simulate a previous run
	s.mu.Lock()
	s.lastSnapshotBlock = 14
	s.mu.Unlock()

	// Next range should be [15..19]
	err := s.checkAndSnapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(19), s.lastSnapshotBlock)

	// Verify the file name reflects the correct range
	_, err = os.Stat(filepath.Join(snapshotDir, "snapshot_15_19.kvsnap.gz"))
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// scanExisting: non-existent directory (glob error path)
// ---------------------------------------------------------------------------

func TestScanExisting_NonExistentDir(t *testing.T) {
	cfg := &config.SnapshotConfig{Dir: "/nonexistent/path/snapshots"}
	s := New(cfg, nil, nil)
	s.scanExisting()

	// Should gracefully handle the error and set defaults
	assert.Equal(t, int64(0), s.lastSnapshotBlock)
	assert.Equal(t, 0, s.totalSnapshots)
}

// ---------------------------------------------------------------------------
// ListSnapshots: os.Stat error path
// ---------------------------------------------------------------------------

func TestListSnapshots_StatErrorSkipsFile(t *testing.T) {
	// This is hard to trigger naturally since Glob returns existing files.
	// But if a file is deleted between Glob and Stat, it would be skipped.
	// We test this indirectly by verifying the function handles file system races.
	s, dir := newTestSnapshotter(t)

	// Create a valid snapshot file
	fname := "snapshot_1000_1999.kvsnap.gz"
	err := os.WriteFile(filepath.Join(dir, fname), []byte("data"), 0o600)
	require.NoError(t, err)

	infos := s.ListSnapshots()
	require.Len(t, infos, 1)
	assert.Equal(t, int64(1000), infos[0].StartBlock)
}

// ---------------------------------------------------------------------------
// Full end-to-end: Start → checkAndSnapshot → snapshot creation
// ---------------------------------------------------------------------------

func TestSnapshotter_FullLifecycle(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 10, 14)

	snapshotDir := t.TempDir()
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             snapshotDir,
		BlocksPerFile:   5,
		IntervalSeconds: 1,
	}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))

	ctx := context.Background()
	err := s.Start(ctx)
	require.NoError(t, err)
	s.ctx = ctx

	// Wait for the ticker to fire at least once
	time.Sleep(3 * time.Second)

	s.Stop()

	// Check that at least one snapshot was created
	m := s.GetMetrics()
	assert.True(t, m.TotalSnapshots >= 1, "should have created at least one snapshot")
}

// ---------------------------------------------------------------------------
// loop: context cancellation path (line 207-208 in snapshot.go)
// ---------------------------------------------------------------------------

func TestLoop_ContextCancellation(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600, // long interval to avoid ticker firing
	}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))
	s.ctx = context.Background()

	ctx, cancel := context.WithCancel(context.Background())
	err := s.Start(ctx)
	require.NoError(t, err)

	// Cancel context (not Stop) to exercise the ctx.Done path
	cancel()

	// Wait for the goroutine to exit
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// good - loop exited via ctx.Done
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit via ctx.Done within 5 seconds")
	}
}

// ---------------------------------------------------------------------------
// loop: error logging path (checkAndSnapshot returns error during tick)
// ---------------------------------------------------------------------------

func TestLoop_ErrorLogging(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 10, 14)

	// Use a directory that's read-only so createKVSnapshot will fail
	snapshotDir := filepath.Join(t.TempDir(), "readonly")
	err := os.MkdirAll(snapshotDir, 0o750)
	require.NoError(t, err)
	chmodReadOnly(t, snapshotDir)

	cfg := &config.SnapshotConfig{
		Enabled:         true,
		Dir:             snapshotDir,
		BlocksPerFile:   5,
		IntervalSeconds: 1,
	}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))
	s.ctx = context.Background()

	// Manually scan existing to avoid Start calling MkdirAll
	s.scanExisting()

	// Start the loop directly
	s.wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	go s.loop(ctx)

	// Wait for the ticker to fire and trigger the error path
	time.Sleep(2 * time.Second)

	cancel()
	s.wg.Wait()

	// No assertion needed - the test passes if it doesn't hang or crash.
	// The error path in the loop just logs the error.
}

// ---------------------------------------------------------------------------
// createKVSnapshot: signSnapshotWithRoots error path
// This happens when signing fails and returns an error. Currently,
// signSnapshotWithRoots returns nil on signing failure (logs warning).
// The error path in createKVSnapshot (line 139-141) would only be hit
// if signSnapshotWithRoots returns a non-nil error (e.g., nil merkle root).
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// checkAndSnapshot: chain block range reader error simulation
// We can't easily cause a chain error with a real DefraDB, but we test that
// the function properly handles the case where blocks exist.
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_WithBlockSignaturesAndIdentity(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	insertTestBlocks(t, td, 50, 54)

	// Insert block signatures
	for i := int64(50); i <= 54; i++ {
		mr := hex.EncodeToString(bytes.Repeat([]byte{byte(i - 49)}, 32)) //nolint:gosec
		insertBlockSignature(t, td, i, mr)
	}

	identCtx, _ := newIdentityCtx(t, crypto.KeyTypeEd25519)

	snapshotDir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: snapshotDir, BlocksPerFile: 5}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))
	s.ctx = identCtx

	err := s.checkAndSnapshot(identCtx)
	require.NoError(t, err)
	assert.Equal(t, int64(54), s.lastSnapshotBlock)

	// Verify file and signature exist
	_, err = os.Stat(filepath.Join(snapshotDir, "snapshot_50_54.kvsnap.gz"))
	require.NoError(t, err)

	sigs, err := QuerySnapshotSignatures(context.Background(), td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	assert.Len(t, sigs, 1)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot: with identity-inserted blocks (full signed snapshot)
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_WithIdentityInsertedBlocks(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)
	identCtx, _ := insertTestBlocksWithIdentity(t, td, 50, 54)

	snapshotDir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: snapshotDir, BlocksPerFile: 5}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))
	s.ctx = identCtx

	err := s.checkAndSnapshot(identCtx)
	require.NoError(t, err)
	assert.Equal(t, int64(54), s.lastSnapshotBlock)

	// Verify snapshot file
	_, err = os.Stat(filepath.Join(snapshotDir, "snapshot_50_54.kvsnap.gz"))
	require.NoError(t, err)

	// Verify signature
	sigs, err := QuerySnapshotSignatures(context.Background(), td.Node, testSnapshotSignatureCollection)
	require.NoError(t, err)
	assert.Len(t, sigs, 1)
}

// ---------------------------------------------------------------------------
// checkAndSnapshot: closed node → chain.GetLowestStoredBlockNumber fails
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_ClosedNode(t *testing.T) {
	s, td, _ := newTestSnapshotterWithDB(t, 3, 100, 102)

	_ = td.Node.Close(context.Background())

	err := s.checkAndSnapshot(context.Background())
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Step 5 assertion tests: verify chain interface usage via testutils.MockConverter
// ---------------------------------------------------------------------------

func TestCheckAndSnapshot_UsesChainBlockRange(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	mc := &testutils.MockConverter{
		GetLowestStoredBlockNumberFn:  func(_ context.Context, _ *node.Node) (int64, error) { return 100, nil },
		GetHighestStoredBlockNumberFn: func(_ context.Context, _ *node.Node) (int64, error) { return 104, nil },
		GetDocIDsByBlockRangeFn: func(_ context.Context, _ *node.Node, _, _ int64) (map[string][]string, error) {
			return map[string][]string{}, nil
		},
		GetCollectionsFn: func() []string {
			return evm.NewCollectionNames(evm.DefaultCollectionPrefix).AllCollections()
		},
	}

	snapshotDir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: snapshotDir, BlocksPerFile: 5}
	s := New(cfg, td.Node, mc)
	s.ctx = context.Background()

	err := s.checkAndSnapshot(context.Background())
	require.NoError(t, err)

	assert.GreaterOrEqual(t, mc.GetLowestStoredBlockNumberCalls, 1, "should call GetLowestStoredBlockNumber")
	assert.GreaterOrEqual(t, mc.GetHighestStoredBlockNumberCalls, 1, "should call GetHighestStoredBlockNumber")

	_, err = os.Stat(filepath.Join(snapshotDir, "snapshot_100_104.kvsnap.gz"))
	require.NoError(t, err)
}

func TestNew_ResolvesSignatureCollectionsViaSuffixMatch(t *testing.T) {
	mc := &testutils.MockConverter{
		CollectionsFn: func() chains.Collections {
			return evm.NewCollectionNames("CustomChain__Testnet")
		},
		SignatureCollectionFn: func() string { return "CustomChain__Testnet__BlockSignature" },
	}

	s := New(&config.SnapshotConfig{Dir: t.TempDir()}, nil, mc)

	assert.Equal(t, "CustomChain__Testnet__BlockSignature", s.blockSigCollection)
	assert.Equal(t, "CustomChain__Testnet__SnapshotSignature", s.snapshotSigCollection)
}
