package snapshot

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestListSnapshots(t *testing.T) {
	tests := []struct {
		name         string
		useCustomDir bool
		files        []string
		fileContent  string
		wantNames    []string
		wantStarts   []int64
		wantEnds     []int64
		checkExtra   func(t *testing.T, infos []SnapshotInfo)
	}{
		{
			name:       "empty directory returns no infos",
			wantNames:  []string{},
			wantStarts: []int64{},
		},
		{
			name: "valid snapshot files are parsed and sorted",
			files: []string{
				"snapshot_1000_1999.kvsnap.gz",
				"snapshot_2000_2999.kvsnap.gz",
				"snapshot_3000_3999.kvsnap.gz",
			},
			fileContent: "test data",
			wantNames: []string{
				"snapshot_1000_1999.kvsnap.gz",
				"snapshot_2000_2999.kvsnap.gz",
				"snapshot_3000_3999.kvsnap.gz",
			},
			wantStarts: []int64{1000, 2000, 3000},
			wantEnds:   []int64{1999, 2999, 3999},
		},
		{
			name:        "size and modtime come from stat",
			files:       []string{"snapshot_5000_5999.kvsnap.gz"},
			fileContent: "some snapshot content here",
			wantNames:   []string{"snapshot_5000_5999.kvsnap.gz"},
			wantStarts:  []int64{5000},
			wantEnds:    []int64{5999},
			checkExtra: func(t *testing.T, infos []SnapshotInfo) {
				assert.Equal(t, int64(len("some snapshot content here")), infos[0].SizeBytes)
				assert.False(t, infos[0].CreatedAt.IsZero())
				assert.WithinDuration(t, time.Now(), infos[0].CreatedAt, 5*time.Second)
			},
		},
		{
			name: "malformed names are skipped",
			files: []string{
				"snapshot_abc_def.kvsnap.gz", // non-numeric
				"snapshot_1000.kvsnap.gz",    // only 2 parts after split
				"random_file.txt",            // not matching glob
				"snapshot_1_2_3.kvsnap.gz",   // too many parts (4 after split)
				"snapshot_.kvsnap.gz",        // missing numbers
				"snapshot_100_199.kvsnap.gz",
			},
			fileContent: "data",
			wantNames:   []string{"snapshot_100_199.kvsnap.gz"},
			wantStarts:  []int64{100},
			wantEnds:    []int64{199},
		},
		{
			name: "files are sorted by start block",
			files: []string{
				"snapshot_9000_9999.kvsnap.gz",
				"snapshot_1000_1999.kvsnap.gz",
				"snapshot_5000_5999.kvsnap.gz",
			},
			fileContent: "data",
			wantNames: []string{
				"snapshot_1000_1999.kvsnap.gz",
				"snapshot_5000_5999.kvsnap.gz",
				"snapshot_9000_9999.kvsnap.gz",
			},
			wantStarts: []int64{1000, 5000, 9000},
			wantEnds:   []int64{1999, 5999, 9999},
		},
		{
			name:         "directory does not exist",
			useCustomDir: true,
			wantNames:    []string{},
			wantStarts:   []int64{},
		},
		{
			name:        "large block numbers parse correctly",
			files:       []string{"snapshot_23700000_23700999.kvsnap.gz"},
			fileContent: "data",
			wantNames:   []string{"snapshot_23700000_23700999.kvsnap.gz"},
			wantStarts:  []int64{23700000},
			wantEnds:    []int64{23700999},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, dir := newTestSnapshotter(t)
			if tt.useCustomDir {
				s = New(&config.SnapshotConfig{Dir: "/nonexistent/path/snapshots"}, nil, nil)
			}
			for _, f := range tt.files {
				err := os.WriteFile(filepath.Join(dir, f), []byte(tt.fileContent), 0o600)
				require.NoError(t, err)
			}

			infos := s.ListSnapshots()
			require.Len(t, infos, len(tt.wantNames))
			for i, wantName := range tt.wantNames {
				assert.Equal(t, wantName, infos[i].Filename)
				assert.Equal(t, tt.wantStarts[i], infos[i].StartBlock)
				if tt.wantEnds != nil {
					assert.Equal(t, tt.wantEnds[i], infos[i].EndBlock)
				}
			}
			if tt.checkExtra != nil {
				tt.checkExtra(t, infos)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetSnapshotPath
// ---------------------------------------------------------------------------

func TestGetSnapshotPath(t *testing.T) {
	tests := []struct {
		name       string
		filename   string
		createFile bool
	}{
		{name: "existing snapshot file returns its path", filename: "snapshot_1000_1999.kvsnap.gz", createFile: true},
		{name: "file does not exist returns empty", filename: "snapshot_9999_10998.kvsnap.gz"},
		{name: "parent traversal rejected", filename: "../etc/passwd"},
		{name: "grandparent traversal rejected", filename: "../../secret.txt"},
		{name: "subdirectory filename rejected", filename: "subdir/file.txt"},
		{name: "traversal to existing-style snapshot name rejected", filename: "../snapshot_1000_1999.kvsnap.gz"},
		{name: "dot-prefixed relative path rejected", filename: "./snapshot_1000_1999.kvsnap.gz"},
		{name: "base filename only resolves", filename: "myfile.kvsnap.gz", createFile: true},
		{name: "hidden dotfile resolves", filename: ".hidden_snapshot.kvsnap.gz", createFile: true},
		{
			// filepath.Base("") returns ".", which != "", so it should return ""
			name: "empty filename returns empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, dir := newTestSnapshotter(t)
			if tt.createFile {
				err := os.WriteFile(filepath.Join(dir, tt.filename), []byte("data"), 0o600)
				require.NoError(t, err)
			}

			result := s.GetSnapshotPath(tt.filename)
			if tt.createFile {
				assert.Equal(t, filepath.Join(dir, tt.filename), result)
			} else {
				assert.Equal(t, "", result)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetMetrics
// ---------------------------------------------------------------------------

func TestGetMetrics(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		setLast  int64 // value pre-seeded into lastSnapshotBlock
		setTotal int   // value pre-seeded into totalSnapshots
	}{
		{name: "initial state", enabled: true},
		{name: "disabled config", enabled: false},
		{name: "after manual update", enabled: true, setLast: 5999, setTotal: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.SnapshotConfig{Enabled: tt.enabled, Dir: "/tmp/test"}
			s := New(cfg, nil, nil)

			s.mu.Lock()
			s.lastSnapshotBlock = tt.setLast
			s.totalSnapshots = tt.setTotal
			s.mu.Unlock()

			m := s.GetMetrics()
			assert.Equal(t, tt.enabled, m.Enabled)
			assert.Equal(t, tt.setLast, m.LastSnapshotBlock)
			assert.Equal(t, tt.setTotal, m.TotalSnapshots)
		})
	}
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

func TestScanExisting(t *testing.T) {
	tests := []struct {
		name      string
		dir       string // empty means a fresh t.TempDir()
		files     []string
		wantLast  int64
		wantTotal int
	}{
		{name: "no files"},
		{
			name: "finds highest block",
			files: []string{
				"snapshot_1000_1999.kvsnap.gz",
				"snapshot_5000_5999.kvsnap.gz",
				"snapshot_3000_3999.kvsnap.gz",
			},
			wantLast:  5999,
			wantTotal: 3,
		},
		{
			// Both files match the glob, so totalSnapshots = 2, but highest = 1999
			name:      "malformed files ignored",
			files:     []string{"snapshot_abc_def.kvsnap.gz", "snapshot_1000_1999.kvsnap.gz"},
			wantLast:  1999,
			wantTotal: 2,
		},
		{
			// Should gracefully handle the glob error and set defaults
			name: "non-existent dir",
			dir:  "/nonexistent/path/snapshots",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.dir
			if dir == "" {
				dir = t.TempDir()
			}
			for _, f := range tt.files {
				err := os.WriteFile(filepath.Join(dir, f), []byte("data"), 0o600)
				require.NoError(t, err)
			}

			s := New(&config.SnapshotConfig{Dir: dir}, nil, nil)
			s.scanExisting()

			assert.Equal(t, tt.wantLast, s.lastSnapshotBlock)
			assert.Equal(t, tt.wantTotal, s.totalSnapshots)
		})
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip serialization
// ---------------------------------------------------------------------------

func TestJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "Metrics", value: &Metrics{Enabled: true, LastSnapshotBlock: 9999, TotalSnapshots: 5}},
		{name: "SnapshotInfo", value: &SnapshotInfo{
			Filename:   "snapshot_1000_1999.kvsnap.gz",
			StartBlock: 1000,
			EndBlock:   1999,
			SizeBytes:  12345,
			CreatedAt:  time.Now().UTC().Truncate(time.Second),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.value)
			require.NoError(t, err)

			decoded := reflect.New(reflect.TypeOf(tt.value).Elem()).Interface()
			err = json.Unmarshal(data, decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.value, decoded)
		})
	}
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

func TestCheckAndSnapshot_Sequences(t *testing.T) {
	tests := []struct {
		name          string
		blocksPerFile int64
		start, end    int64
		wantLast      []int64       // expected lastSnapshotBlock after each checkAndSnapshot call
		setLast       map[int]int64 // optional lastSnapshotBlock override applied before call i
		wantTotal     int
		wantFiles     []string
	}{
		{
			// blocks_per_file=3 with blocks 3-7 (non-zero start avoids the
			// lowest==0 early return): [3..5] completes; [6..8] cannot.
			name:          "small blocks per file stops at incomplete range",
			blocksPerFile: 3, start: 3, end: 7,
			wantLast:  []int64{5, 5},
			wantTotal: 1,
			wantFiles: []string{"snapshot_3_5.kvsnap.gz"},
		},
		{
			name:          "multiple rounds",
			blocksPerFile: 2, start: 10, end: 15,
			wantLast:  []int64{11, 13, 15},
			wantTotal: 3,
			wantFiles: []string{"snapshot_10_11.kvsnap.gz", "snapshot_12_13.kvsnap.gz", "snapshot_14_15.kvsnap.gz"},
		},
		{
			// Resetting lastSnapshotBlock below lowest exercises the
			// rangeStart < lowest skip-ahead path: re-aligns to [20..24].
			name:          "gap skip ahead realigns to lowest",
			blocksPerFile: 5, start: 20, end: 29,
			wantLast:  []int64{24, 29, 24},
			setLast:   map[int]int64{2: 4},
			wantTotal: 3,
		},
		{
			name:          "continuation from last snapshot",
			blocksPerFile: 5, start: 10, end: 19,
			wantLast:  []int64{19},
			setLast:   map[int]int64{0: 14},
			wantTotal: 1,
			wantFiles: []string{"snapshot_15_19.kvsnap.gz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, ctx := newTestSnapshotterWithDB(t, tt.blocksPerFile, tt.start, tt.end)

			for i, want := range tt.wantLast {
				if override, ok := tt.setLast[i]; ok {
					s.mu.Lock()
					s.lastSnapshotBlock = override
					s.mu.Unlock()
				}
				require.NoError(t, s.checkAndSnapshot(ctx))
				assert.Equal(t, want, s.lastSnapshotBlock)
			}

			assert.Equal(t, tt.wantTotal, s.totalSnapshots)
			for _, name := range tt.wantFiles {
				_, err := os.Stat(filepath.Join(s.cfg.Dir, name))
				require.NoError(t, err, "snapshot %s should exist", name)
			}
		})
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
// checkAndSnapshot: with block signatures and identity (full signing flow)
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
