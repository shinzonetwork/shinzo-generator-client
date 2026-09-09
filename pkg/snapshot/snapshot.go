package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// queryChunkSize is the number of blocks queried per GraphQL request.
// to avoid memory pressure from large result sets.
const (
	// numFileParts is the expected number of parts in a snapshot filename when split by "_".
	numFileParts = 3
)

// SnapshotInfo describes a snapshot file on disk.
//
//nolint:revive // name is intentionally descriptive; renaming would break external references.
type SnapshotInfo struct {
	Filename   string    `json:"filename"`
	StartBlock int64     `json:"start_block"`
	EndBlock   int64     `json:"end_block"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

// Metrics reports snapshot component status.
type Metrics struct {
	Enabled           bool  `json:"enabled"`
	LastSnapshotBlock int64 `json:"last_snapshot_block"`
	TotalSnapshots    int   `json:"total_snapshots"`
}

// Snapshotter exports block data to gzip'd KV snapshot files before they are pruned.
type Snapshotter struct {
	cfg       *config.SnapshotConfig
	defraNode *node.Node
	converter chains.Converter
	ctx       context.Context //nolint:containedctx // stored from Start(), carries identity for signing

	// blockSigCollection and snapshotSigCollection are the chain-specific
	// collection names resolved from converter.Collections() by New().
	blockSigCollection    string
	snapshotSigCollection string

	mu                sync.RWMutex
	lastSnapshotBlock int64
	totalSnapshots    int
	stopChan          chan struct{}
	wg                sync.WaitGroup
}

// New creates a new Snapshotter. The converter supplies block-range
// queries and resolves chain-specific collection names; it may be nil for
// tests that never invoke checkAndSnapshot.
func New(cfg *config.SnapshotConfig, defraNode *node.Node, converter chains.Converter) *Snapshotter {
	s := &Snapshotter{
		cfg:       cfg,
		defraNode: defraNode,
		converter: converter,
		stopChan:  make(chan struct{}),
	}
	if converter != nil {
		cols := converter.Collections()
		s.blockSigCollection = converter.SignatureCollection()
		s.snapshotSigCollection, _ = cols.GetCollection(chains.TypeSnapshotSignature)
		if s.blockSigCollection == "" || s.snapshotSigCollection == "" {
			logger.Sugar.Warnf("Snapshot: could not resolve signature collections from chain (blockSig=%q, snapshotSig=%q); signing will be skipped",
				s.blockSigCollection, s.snapshotSigCollection)
		}
	}
	return s
}

// Start begins the background snapshot loop.
func (s *Snapshotter) Start(ctx context.Context) error {
	if err := os.MkdirAll(s.cfg.Dir, 0o750); err != nil { // nolint:mnd
		return fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	s.ctx = ctx // store context with identity for signing.
	s.scanExisting()

	// Enforce the retention limit once at startup so a lowered max_snapshots
	// takes effect immediately. Failures are non-fatal.
	if err := s.enforceSnapshotLimit(ctx); err != nil {
		logger.Sugar.Warnf("Snapshotter failed to enforce max_snapshots at startup: %v", err)
	}

	s.wg.Add(1)
	go s.loop(ctx)

	logger.Sugar.Infof("Snapshotter started (dir=%s, blocks_per_file=%d, interval=%ds, max_snapshots=%d)",
		s.cfg.Dir, s.cfg.BlocksPerFile, s.cfg.IntervalSeconds, s.cfg.MaxSnapshots)
	return nil
}

// Stop gracefully stops the snapshotter.
func (s *Snapshotter) Stop() {
	close(s.stopChan)
	s.wg.Wait()
	logger.Sugar.Info("Snapshotter stopped")
}

// GetMetrics returns current snapshot metrics.
func (s *Snapshotter) GetMetrics() Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Metrics{
		Enabled:           s.cfg.Enabled,
		LastSnapshotBlock: s.lastSnapshotBlock,
		TotalSnapshots:    s.totalSnapshots,
	}
}

// ListSnapshots returns information about all snapshot files.
func (s *Snapshotter) ListSnapshots() []SnapshotInfo {
	files, err := filepath.Glob(filepath.Join(s.cfg.Dir, "snapshot_*.kvsnap.gz"))
	if err != nil {
		return nil
	}

	var infos []SnapshotInfo
	for _, f := range files {
		base := filepath.Base(f)
		parts := strings.Split(strings.TrimSuffix(base, ".kvsnap.gz"), "_")
		if len(parts) != numFileParts {
			continue
		}
		start, err1 := strconv.ParseInt(parts[1], 10, 64)
		end, err2 := strconv.ParseInt(parts[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}

		stat, err := os.Stat(f)
		if err != nil {
			continue
		}

		infos = append(infos, SnapshotInfo{
			Filename:   base,
			StartBlock: start,
			EndBlock:   end,
			SizeBytes:  stat.Size(),
			CreatedAt:  stat.ModTime(),
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].StartBlock < infos[j].StartBlock
	})

	return infos
}

// GetSnapshotPath returns the full path to a snapshot file, or empty string if not found.
func (s *Snapshotter) GetSnapshotPath(filename string) string {
	// Sanitize: only allow base filenames
	if filepath.Base(filename) != filename {
		return ""
	}
	p := filepath.Join(s.cfg.Dir, filename)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// SnapshotSigCollection returns the resolved SnapshotSignature collection name.
// The name is resolved once in New from chain.GetCollections(); empty when chain is nil.
func (s *Snapshotter) SnapshotSigCollection() string {
	return s.snapshotSigCollection
}

// scanExisting reads the snapshot directory to find the highest snapshotted block.
func (s *Snapshotter) scanExisting() {
	files, err := filepath.Glob(filepath.Join(s.cfg.Dir, "snapshot_*.kvsnap.gz"))
	if err != nil {
		return
	}

	var highest int64
	for _, f := range files {
		base := filepath.Base(f)
		parts := strings.Split(strings.TrimSuffix(base, ".kvsnap.gz"), "_")
		if len(parts) == numFileParts {
			if end, err := strconv.ParseInt(parts[2], 10, 64); err == nil && end > highest {
				highest = end
			}
		}
	}

	s.mu.Lock()
	s.lastSnapshotBlock = highest
	s.totalSnapshots = len(files)
	s.mu.Unlock()

	if highest > 0 {
		logger.Sugar.Infof("Snapshotter: found %d existing snapshots up to block %d", len(files), highest)
	}
}

func (s *Snapshotter) loop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Duration(s.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			if err := s.checkAndSnapshot(ctx); err != nil {
				logger.Sugar.Errorf("Snapshot check failed: %v", err)
			}
		}
	}
}

func (s *Snapshotter) checkAndSnapshot(ctx context.Context) error {
	if s.converter == nil {
		return nil
	}

	lowest, err := s.converter.GetLowestStoredBlockNumber(ctx, s.defraNode)
	if err != nil {
		if errors.IsErrNotFound(err) {
			return nil
		}
		logger.Sugar.Warnf("Snapshot: GetLowestStoredBlockNumber failed: %v", err)
		return err
	}
	if lowest == 0 {
		return nil
	}
	highest, err := s.converter.GetHighestStoredBlockNumber(ctx, s.defraNode)
	if err != nil {
		if errors.IsErrNotFound(err) {
			return nil
		}
		logger.Sugar.Warnf("Snapshot: GetHighestStoredBlockNumber failed: %v", err)
		return err
	}
	if highest == 0 {
		return nil
	}

	s.mu.RLock()
	lastSnapshot := s.lastSnapshotBlock
	s.mu.RUnlock()

	rangeStart, rangeEnd, ok := s.nextSnapshotRange(lastSnapshot, lowest, highest)
	if !ok {
		return nil
	}

	logger.Sugar.Infof("Snapshotting blocks %d to %d", rangeStart, rangeEnd)

	if err := s.createSnapshot(ctx, rangeStart, rangeEnd); err != nil {
		return fmt.Errorf("snapshot %d-%d failed: %w", rangeStart, rangeEnd, err)
	}

	s.mu.Lock()
	s.lastSnapshotBlock = rangeEnd
	s.totalSnapshots++
	s.mu.Unlock()

	// Enforce the retention limit after each successful snapshot. Failures are
	// non-fatal: purge problems must never block snapshot creation.
	if err := s.enforceSnapshotLimit(ctx); err != nil {
		logger.Sugar.Warnf("Snapshot: failed to enforce max_snapshots limit: %v", err)
	}

	logger.Sugar.Infof("Snapshot created: blocks %d to %d", rangeStart, rangeEnd)
	return nil
}

// nextSnapshotRange determines the next aligned block range to snapshot.
// Ranges are aligned to multiples of blocks_per_file:
//
//	e.g. with bpf=1000: [23700000..23700999], [23701000..23701999], ...
//
// It skips ahead if the pruner removed blocks that were never snapshotted,
// and reports ok=false when the DB does not yet hold the full range.
func (s *Snapshotter) nextSnapshotRange(lastSnapshot, lowest, highest int64) (start, end int64, ok bool) {
	bpf := s.cfg.BlocksPerFile

	if lastSnapshot == 0 {
		// First snapshot: align to the nearest boundary at or above lowest.
		start = ((lowest + bpf - 1) / bpf) * bpf
	} else {
		start = lastSnapshot + 1
	}

	// If pruner removed blocks we haven't snapshotted, skip ahead.
	if start < lowest {
		logger.Sugar.Warnf("Snapshot gap: expected range starting %d but lowest in DB is %d", start, lowest)
		start = ((lowest + bpf - 1) / bpf) * bpf
	}

	end = start + bpf - 1
	if end > highest {
		// The entire aligned range must be present in the DB.
		return 0, 0, false
	}
	return start, end, true
}

func (s *Snapshotter) createSnapshot(ctx context.Context, startBlock, endBlock int64) error {
	return s.createKVSnapshot(ctx, startBlock, endBlock)
}
