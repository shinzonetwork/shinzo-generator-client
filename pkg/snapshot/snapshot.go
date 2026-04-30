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

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// queryChunkSize is the number of blocks queried per GraphQL request
// to avoid memory pressure from large result sets.
const queryChunkSize int64 = 100

// Config holds snapshot configuration.
type Config struct {
	Enabled         bool   `yaml:"enabled"`
	Dir             string `yaml:"dir"`
	BlocksPerFile   int64  `yaml:"blocks_per_file"`
	IntervalSeconds int    `yaml:"interval_seconds"`
}

// SetDefaults applies default values for unset fields.
func (c *Config) SetDefaults() {
	if c.Dir == "" {
		c.Dir = "./snapshots"
	}
	if c.BlocksPerFile <= 0 {
		c.BlocksPerFile = 1000
	}
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = 60
	}
}

// SnapshotInfo describes a snapshot file on disk.
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
	cfg       *Config
	defraNode *node.Node
	ctx       context.Context // stored from Start(), carries identity for signing

	mu                sync.RWMutex
	lastSnapshotBlock int64
	totalSnapshots    int
	stopChan          chan struct{}
	wg                sync.WaitGroup
}

// New creates a new Snapshotter.
func New(cfg *Config, defraNode *node.Node) *Snapshotter {
	return &Snapshotter{
		cfg:       cfg,
		defraNode: defraNode,
		stopChan:  make(chan struct{}),
	}
}

// Start begins the background snapshot loop.
func (s *Snapshotter) Start(ctx context.Context) error {
	if err := os.MkdirAll(s.cfg.Dir, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	s.ctx = ctx // store context with identity for signing
	s.scanExisting()

	s.wg.Add(1)
	go s.loop(ctx)

	logger.Sugar.Infof("Snapshotter started (dir=%s, blocks_per_file=%d, interval=%ds)",
		s.cfg.Dir, s.cfg.BlocksPerFile, s.cfg.IntervalSeconds)
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
		if len(parts) != 3 {
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
		if len(parts) == 3 {
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
	lowest, err := s.getBlockNumber(ctx, "ASC")
	if err != nil {
		logger.Sugar.Warnf("Snapshot: getBlockNumber(ASC) failed: %v", err)
		return err
	}
	if lowest == 0 {
		return nil
	}
	highest, err := s.getBlockNumber(ctx, "DESC")
	if err != nil {
		logger.Sugar.Warnf("Snapshot: getBlockNumber(DESC) failed: %v", err)
		return err
	}
	if highest == 0 {
		return nil
	}

	s.mu.RLock()
	lastSnapshot := s.lastSnapshotBlock
	s.mu.RUnlock()

	bpf := s.cfg.BlocksPerFile

	// Determine the next aligned range to snapshot.
	// Ranges are aligned to multiples of blocks_per_file:
	//   e.g. with bpf=1000: [23700000..23700999], [23701000..23701999], ...
	var rangeStart int64
	if lastSnapshot == 0 {
		// First snapshot: align to the nearest boundary at or above lowest.
		rangeStart = ((lowest + bpf - 1) / bpf) * bpf
	} else {
		rangeStart = lastSnapshot + 1
	}

	// If pruner removed blocks we haven't snapshotted, skip ahead.
	if rangeStart < lowest {
		logger.Sugar.Warnf("Snapshot gap: expected range starting %d but lowest in DB is %d", rangeStart, lowest)
		rangeStart = ((lowest + bpf - 1) / bpf) * bpf
	}

	rangeEnd := rangeStart + bpf - 1

	// The entire aligned range must be present in the DB.
	if rangeEnd > highest {
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

	logger.Sugar.Infof("Snapshot created: blocks %d to %d", rangeStart, rangeEnd)
	return nil
}

func (s *Snapshotter) createSnapshot(ctx context.Context, startBlock, endBlock int64) error {
	return s.createKVSnapshot(ctx, startBlock, endBlock)
}

func (s *Snapshotter) getBlockNumber(ctx context.Context, order string) (int64, error) {
	query := fmt.Sprintf(`query { %s(order: {number: %s}, limit: 1) { number } }`,
		constants.CollectionBlock, order)

	result := s.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, fmt.Errorf("getBlockNumber(%s): %v", order, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, nil
	}

	raw := data[constants.CollectionBlock]
	if raw == nil {
		return 0, nil
	}

	// DefraDB may return []map[string]any or []any depending on the code path.
	var block map[string]any
	switch typed := raw.(type) {
	case []any:
		if len(typed) == 0 {
			return 0, nil
		}
		block, _ = typed[0].(map[string]any)
	case []map[string]any:
		if len(typed) == 0 {
			return 0, nil
		}
		block = typed[0]
	default:
		return 0, nil
	}

	if block == nil {
		return 0, nil
	}

	switch v := block["number"].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	}
	return 0, nil
}
