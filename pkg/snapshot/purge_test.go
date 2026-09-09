package snapshot

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnforceSnapshotLimit(t *testing.T) {
	tests := []struct {
		name         string
		maxSnapshots int
		seedCount    int64
		seedTmpFile  bool
		wantCount    int
		wantKept     []string // when set, exactly these files must remain (ascending)
	}{
		{
			name:         "keeps newest files beyond limit",
			maxSnapshots: 2,
			seedCount:    5,
			wantCount:    2,
			wantKept:     []string{"snapshot_1300_1399.kvsnap.gz", "snapshot_1400_1499.kvsnap.gz"},
		},
		{
			name:         "within limit is a no-op",
			maxSnapshots: 5,
			seedCount:    3,
			wantCount:    3,
		},
		{
			name:         "negative limit means unlimited",
			maxSnapshots: -1,
			seedCount:    4,
			wantCount:    4,
		},
		{
			// MaxSnapshots unset (0): SetDefaults fills in the default of 100,
			// so anything at or below it must be untouched.
			name:         "zero limit falls back to default",
			maxSnapshots: 0,
			seedCount:    3,
			wantCount:    3,
		},
		{
			name:         "zero fallback caps at default",
			maxSnapshots: 0,
			seedCount:    101, // DefaultMaxSnapshots + 1
			wantCount:    100,
		},
		{
			name:         "tmp files are never purge candidates",
			maxSnapshots: 1,
			seedCount:    2,
			seedTmpFile:  true,
			wantCount:    1,
		},
		{
			name:         "files still purged without a DB node",
			maxSnapshots: 1,
			seedCount:    2,
			wantCount:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, dir := newTestSnapshotter(t)
			s.cfg.MaxSnapshots = tt.maxSnapshots
			seedSnapshotFiles(t, dir, 1000, tt.seedCount)

			var tmpPath string
			if tt.seedTmpFile {
				// A transient temp file must never be a purge candidate.
				tmpPath = filepath.Join(dir, "snapshot_1500_1599.kvsnap.gz.tmp")
				require.NoError(t, os.WriteFile(tmpPath, []byte("partial"), 0o600)) //nolint:gosec
			}

			require.NoError(t, s.enforceSnapshotLimit(context.Background()))

			assert.Equal(t, tt.wantCount, countSnapshotFiles(t, dir))
			if tt.wantKept != nil {
				infos := s.ListSnapshots()
				require.Len(t, infos, len(tt.wantKept))
				for i, name := range tt.wantKept {
					assert.Equal(t, name, infos[i].Filename)
				}
			}
			if tt.seedTmpFile {
				_, err := os.Stat(tmpPath)
				assert.NoError(t, err, "tmp file must not be touched")
			}
			// Metrics are only refreshed when a purge actually happened
			// (enforceSnapshotLimit returns early when within the limit).
			if int64(tt.wantCount) < tt.seedCount {
				assert.Equal(t, tt.wantCount, s.GetMetrics().TotalSnapshots, "metrics should reflect post-purge count")
			}
		})
	}
}

func TestEnforceSnapshotLimit_DeletesSignatureDocs(t *testing.T) {
	s, td, ctx := newTestSnapshotterWithDB(t, 1000, 1, 0) // DB + collections, no blocks needed.
	s.cfg.MaxSnapshots = 1

	purged := "snapshot_1000_1099.kvsnap.gz"
	kept := "snapshot_1100_1199.kvsnap.gz"
	writeKVSnapGz(t, s.cfg.Dir, purged, func(_ *gzip.Writer) {})
	writeKVSnapGz(t, s.cfg.Dir, kept, func(_ *gzip.Writer) {})

	for _, name := range []string{purged, kept} {
		sig := &SnapshotSignatureData{
			Version:             1,
			SnapshotFile:        name,
			StartBlock:          1000,
			EndBlock:            1199,
			MerkleRoot:          "root-" + name,
			BlockCount:          100,
			SignatureType:       constants.Secp256k1ValueString,
			SignatureIdentity:   "test-identity",
			SignatureValue:      "test-signature",
			CreatedAt:           "2024-01-01T00:00:00Z",
			BlockSigMerkleRoots: []string{"a", "b"},
		}
		require.NoError(t, s.createSnapshotSignatureDoc(ctx, sig))
	}

	sigsBefore, err := QuerySnapshotSignatures(ctx, td.Node, s.snapshotSigCollection)
	require.NoError(t, err)
	require.Len(t, sigsBefore, 2)

	require.NoError(t, s.enforceSnapshotLimit(ctx))

	assert.Equal(t, 1, countSnapshotFiles(t, s.cfg.Dir))
	sigsAfter, err := QuerySnapshotSignatures(ctx, td.Node, s.snapshotSigCollection)
	require.NoError(t, err)
	require.Len(t, sigsAfter, 1, "companion signature doc should be deleted with its file")
	assert.Contains(t, sigsAfter, kept)
	assert.NotContains(t, sigsAfter, purged)
}

func TestEnforceSnapshotLimit_RemoveFailuresSurfaceError(t *testing.T) {
	s, dir := newTestSnapshotter(t)
	s.cfg.MaxSnapshots = 1
	seedSnapshotFiles(t, dir, 1000, 3)
	chmodReadOnly(t, dir)

	err := s.enforceSnapshotLimit(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to remove")
	assert.Equal(t, 3, countSnapshotFiles(t, dir), "no files should disappear when removals fail")
}

func TestStart_EnforcesSnapshotLimit(t *testing.T) {
	s, dir := newTestSnapshotter(t)
	s.cfg.MaxSnapshots = 2
	seedSnapshotFiles(t, dir, 1000, 4)

	ctx := t.Context()
	require.NoError(t, s.Start(ctx))
	defer s.Stop()

	assert.Equal(t, 2, countSnapshotFiles(t, dir), "startup should enforce the limit")
	assert.Equal(t, 2, s.GetMetrics().TotalSnapshots)
}

func TestCheckAndSnapshot_EnforcesLimit(t *testing.T) {
	// 8 blocks with blocks_per_file=2 yield four aligned ranges:
	// [100..101], [102..103], [104..105], [106..107].
	s, _, ctx := newTestSnapshotterWithDB(t, 2, 100, 107)
	s.cfg.MaxSnapshots = 2

	for range 4 {
		require.NoError(t, s.checkAndSnapshot(ctx))
	}

	assert.Equal(t, 2, countSnapshotFiles(t, s.cfg.Dir))
	infos := s.ListSnapshots()
	require.Len(t, infos, 2)
	assert.Equal(t, int64(104), infos[0].StartBlock)
	assert.Equal(t, int64(106), infos[1].StartBlock)
	m := s.GetMetrics()
	assert.Equal(t, 2, m.TotalSnapshots)
	assert.Equal(t, int64(107), m.LastSnapshotBlock)
}
