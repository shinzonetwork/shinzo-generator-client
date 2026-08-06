package pruner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	pkgerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/node"
)

// BlockRangeReader provides block-range queries and collection metadata.
// It is a subset of chain.Chain; any chain.Chain implementer satisfies it.
//
// Defined locally (rather than importing pkg/chain.Chain) to break an import
// cycle: pkg/pruner → pkg/chain → config → pkg/pruner (config embeds
// pruner.Config). Go's structural interfaces mean chain.Adapter,
// testutils.MockChain, and any other chain.Chain implementer satisfy
// BlockRangeReader without an explicit implements clause.
type BlockRangeReader interface {
	GetLowestStoredBlockNumber(ctx context.Context) (int64, error)
	GetHighestStoredBlockNumber(ctx context.Context) (int64, error)
	GetDocIDsByBlockRange(ctx context.Context, from, to int64) (map[string][]string, error)
	GetCollections() []string
}

// Suffixes used to resolve the block and block-signature collection names from
// the chain's GetCollections() list. Collection names follow the convention
// prefix + "__" + shortName (e.g. "Ethereum__Mainnet__Block").
const (
	blockCollectionSuffix          = "__Block"
	blockSignatureCollectionSuffix = "__BlockSignature"
)

// ErrNoBlocks indicates that the query succeeded but no blocks were found.
var ErrNoBlocks = errors.New("no blocks found")

// ErrNoValidBlocks indicates that block documents exist in the store but none
// have a valid, parseable block number field (data corruption). Unlike
// ErrNoBlocks, this is a hard error — pruning cannot safely proceed when the
// block range is uncomputable.
var ErrNoValidBlocks = errors.New("blocks exist but none have a valid block number")

// Pruner handles periodic removal of old blockchain documents from DefraDB.
// It supports two queue types:
//   - IndexerQueue: for indexers that track docIDs at creation time
//   - EventQueue: for hosts that track docIDs from P2P replication events
//
// When no queue is set or the queue is underfilled, falls back to filter-based pruning.
type Pruner struct {
	cfg                *Config
	defraNode          *node.Node
	chain              BlockRangeReader
	blockCollection    string
	blockSigCollection string
	queue              Queue
	stopChan           chan struct{}
	wg                 sync.WaitGroup
	mu                 sync.RWMutex

	// Metrics
	lastPruneTime     time.Time
	totalBlocksPruned int64
	totalDocsPruned   int64
	isRunning         bool
}

// Metrics holds pruning statistics.
type Metrics struct {
	Enabled           bool      `json:"enabled"`
	IsRunning         bool      `json:"is_running"`
	LastPruneTime     time.Time `json:"last_prune_time"`
	TotalBlocksPruned int64     `json:"total_blocks_pruned"`
	TotalDocsPruned   int64     `json:"total_docs_pruned"`
}

// NewPruner creates a new Pruner instance.
// The chain parameter provides block-range queries and collection names;
// any chain.Chain implementer satisfies the local BlockRangeReader interface.
func NewPruner(cfg *Config, defraNode *node.Node, chain BlockRangeReader) *Pruner {
	p := &Pruner{
		cfg:       cfg,
		defraNode: defraNode,
		chain:     chain,
		stopChan:  make(chan struct{}),
	}
	if chain != nil {
		p.resolveCollectionNames(chain.GetCollections())
	}
	return p
}

// resolveCollectionNames identifies the block and block-signature collection
// names from the chain's collection list via suffix matching. Collection names
// follow the convention prefix + "__" + shortName, so "__Block" uniquely
// matches the block collection (not BlockSignature).
func (p *Pruner) resolveCollectionNames(collections []string) {
	for _, name := range collections {
		switch {
		case strings.HasSuffix(name, blockSignatureCollectionSuffix):
			p.blockSigCollection = name
		case strings.HasSuffix(name, blockCollectionSuffix):
			p.blockCollection = name
		}
	}
}

// SetQueue sets the queue implementation for queue-based pruning.
func (p *Pruner) SetQueue(queue Queue) {
	p.queue = queue
}

// Start begins the pruning loop in a background goroutine.
func (p *Pruner) Start(ctx context.Context) error {
	if !p.cfg.Enabled {
		logger.Sugar.Info("Pruner is disabled")
		return nil
	}

	if p.defraNode == nil {
		logger.Sugar.Warn("Pruner requires embedded DefraDB node, skipping")
		return nil
	}

	p.mu.Lock()
	if p.isRunning {
		p.mu.Unlock()
		return nil
	}
	p.isRunning = true
	p.mu.Unlock()

	logger.Sugar.Debugf("Starting pruner (max_blocks=%d, docs_per_block=%d, max_docs=%d, interval=%ds)",
		p.cfg.MaxBlocks, p.cfg.DocsPerBlock, p.cfg.MaxDocs(), p.cfg.IntervalSeconds)

	p.wg.Add(1)
	go p.pruneLoop(ctx)

	return nil
}

// Stop signals the pruner to stop and waits for it to complete.
func (p *Pruner) Stop() {
	p.mu.Lock()
	if !p.isRunning {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	logger.Sugar.Infof("Pruner stopping, waiting for current operation to finish...")
	close(p.stopChan)
	p.wg.Wait()

	// Save queue to disk for fast restart
	if p.queue != nil {
		queueLen := p.queue.Len()
		logger.Sugar.Infof("Saving prune queue to disk (%d entries)...", queueLen)
		if err := p.queue.Save(); err != nil {
			logger.Sugar.Errorf("Failed to save prune queue: %v", err)
		} else {
			logger.Sugar.Infof("Prune queue saved successfully")
		}
	}

	p.mu.Lock()
	p.isRunning = false
	p.mu.Unlock()

	logger.Sugar.Info("Pruner stopped")
}

// GetMetrics returns current pruning statistics.
func (p *Pruner) GetMetrics() Metrics {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return Metrics{
		Enabled:           p.cfg.Enabled,
		IsRunning:         p.isRunning,
		LastPruneTime:     p.lastPruneTime,
		TotalBlocksPruned: p.totalBlocksPruned,
		TotalDocsPruned:   p.totalDocsPruned,
	}
}

// pruneLoop runs the periodic pruning check.
func (p *Pruner) pruneLoop(ctx context.Context) {
	defer p.wg.Done()

	// Run startup cleanup only for indexer queues (no P2P) or when no queue is set.
	// Only queue-tracked data gets pruned.

	logger.Sugar.Debugf("Running startup cleanup for pre-existing blocks...")
	if err := p.startupCleanup(ctx); err != nil {
		logger.Sugar.Errorf("Startup cleanup failed: %v", err)
	}

	ticker := time.NewTicker(time.Duration(p.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopChan:
			return
		case <-ticker.C:
			if err := p.runPrune(ctx); err != nil {
				logger.Sugar.Errorf("Prune failed: %v", err)
			}
		}
	}
}

// runPrune executes the appropriate pruning strategy based on queue type and state.
func (p *Pruner) runPrune(ctx context.Context) error {
	if p.queue == nil {
		return p.filterBasedPrune(ctx)
	}

	switch q := p.queue.(type) {
	case *IndexerQueue:
		return p.runIndexerQueuePrune(ctx, q)
	default:
		return p.filterBasedPrune(ctx)
	}
}

// runIndexerQueuePrune drains the IndexerQueue and purges by docIDs.
// No P2P pause needed — the indexer has no concurrent P2P replication.
func (p *Pruner) runIndexerQueuePrune(ctx context.Context, q *IndexerQueue) error {
	// The queue is the only record that a document is prunable, so it is written after every cycle
	// rather than only on shutdown. A kill between checkpoints strands whatever the queue holds,
	// including entries still inside the retention target that no cycle has drained yet.
	defer func() {
		if err := q.Save(); err != nil {
			logger.Sugar.Warnf("Failed to checkpoint prune queue: %v", err)
		}
	}()

	blockCount := int64(q.Len())

	if blockCount <= p.cfg.MaxBlocks {
		return p.filterBasedPrune(ctx)
	}

	// Keep more than the retention target when the backlog exceeds what one cycle should delete.
	// The excess is drained over subsequent cycles, which bounds cycle wall time without changing
	// how many blocks are ultimately retained.
	keep := p.cfg.MaxBlocks
	if p.cfg.MaxBlocksPerCycle > 0 && blockCount-keep > p.cfg.MaxBlocksPerCycle {
		keep = blockCount - p.cfg.MaxBlocksPerCycle
	}
	result := q.Drain(int(p.cfg.MaxBlocks), p.blockCollection, p.blockSigCollection)
	if result == nil {
		return nil
	}

	logger.Sugar.Infof("Pruning %d blocks (queue had %d blocks, keeping %d, target %d, prune_history=%v)",
		result.BlockCount, blockCount, keep, p.cfg.MaxBlocks, p.cfg.PruneHistory)

	return p.purgeFromDrainResult(ctx, result)
}

// purgeFromDrainResult deletes documents from a DrainResult.
// Collections are purged in sorted collection-name order — DefraDB does not
// enforce foreign keys, so purge order does not affect correctness; any
// orphan documents are cleaned by the next prune run's startupCleanup.
func (p *Pruner) purgeFromDrainResult(ctx context.Context, result *DrainResult) error {
	totalPurged, err := p.purgeByDocIDsByCollection(ctx, result.DocIDsByCollection)

	elapsed := time.Since(time.Now())
	logger.Sugar.Infof("Prune complete: removed %d docs for %d blocks in %v",
		totalPurged, result.BlockCount, elapsed)

	p.mu.Lock()
	p.totalBlocksPruned += int64(result.BlockCount)
	p.totalDocsPruned += totalPurged
	p.lastPruneTime = time.Now()
	p.mu.Unlock()

	return err
}

// startupCleanup removes blocks left over from previous runs that aren't in the queue.
func (p *Pruner) startupCleanup(ctx context.Context) error {
	lowest, highest, err := p.getBlockRange(ctx)
	if err != nil {
		if errors.Is(err, ErrNoBlocks) {
			logger.Sugar.Debugf("No existing blocks in database")
			return nil
		}
		return fmt.Errorf("startup cleanup: get block range: %w", err)
	}

	currentCount := highest - lowest + 1
	if currentCount <= p.cfg.MaxBlocks {
		logger.Sugar.Debugf("Existing blocks %d-%d (count=%d) within limit (max_blocks=%d), no cleanup needed",
			lowest, highest, currentCount, p.cfg.MaxBlocks)
		return nil
	}

	toPrune := currentCount - p.cfg.MaxBlocks
	cutoffBlock := lowest + toPrune - 1

	logger.Sugar.Infof("Startup cleanup: pruning blocks %d-%d (%d blocks, keeping %d-%d)",
		lowest, cutoffBlock, toPrune, cutoffBlock+1, highest)

	totalPurged, err := p.pruneBlockRange(ctx, lowest, cutoffBlock)
	if err != nil {
		logger.Sugar.Errorf("Startup: failed to prune blocks %d-%d: %v", lowest, cutoffBlock, err)
		return err
	}

	logger.Sugar.Infof("Startup cleanup complete: purged %d documents", totalPurged)

	p.mu.Lock()
	p.totalBlocksPruned += toPrune
	p.totalDocsPruned += totalPurged
	p.lastPruneTime = time.Now()
	p.mu.Unlock()

	return nil
}

// filterBasedPrune checks the actual DB block count and prunes excess blocks.
// Used by the indexer queue (no P2P) and as a fallback when the queue is underfilled.
func (p *Pruner) filterBasedPrune(ctx context.Context) error {
	lowest, highest, err := p.getBlockRange(ctx)
	if err != nil {
		if errors.Is(err, ErrNoBlocks) {
			return nil
		}
		return fmt.Errorf("filter-based prune: get block range: %w", err)
	}

	dbBlockCount := highest - lowest + 1
	if dbBlockCount <= p.cfg.MaxBlocks {
		return nil
	}

	excess := dbBlockCount - p.cfg.MaxBlocks
	cutoff := lowest + excess - 1

	logger.Sugar.Infof("Filter-based prune: %d excess blocks (%d-%d), pruning %d-%d",
		excess, lowest, highest, lowest, cutoff)

	purged, err := p.pruneBlockRange(ctx, lowest, cutoff)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.totalBlocksPruned += excess
	p.totalDocsPruned += purged
	p.lastPruneTime = time.Now()
	p.mu.Unlock()

	return nil
}

// pruneBlockRange removes all documents for blocks in [startBlock, endBlock].
// Queries docIDs from the chain, then purges each collection in sorted-name
// order (DefraDB has no FK enforcement; any orphans are cleaned by the next
// prune run).
func (p *Pruner) pruneBlockRange(ctx context.Context, startBlock, endBlock int64) (int64, error) {
	logger.Sugar.Infof("pruneBlockRange: deleting blocks %d-%d (%d blocks)",
		startBlock, endBlock, endBlock-startBlock+1)

	docIDsByCollection, err := p.chain.GetDocIDsByBlockRange(ctx, startBlock, endBlock)
	if err != nil {
		return 0, fmt.Errorf("get docIDs by block range: %w", err)
	}

	totalPurged, purgeErr := p.purgeByDocIDsByCollection(ctx, docIDsByCollection)

	logger.Sugar.Infof("pruneBlockRange: purged %d docs for blocks %d-%d", totalPurged, startBlock, endBlock)
	return totalPurged, purgeErr
}

// purgeByDocIDsByCollection purges documents from each collection in the map,
// iterating collection names in sorted order. Returns the total purged count
// and a joined error of any per-collection failures.
func (p *Pruner) purgeByDocIDsByCollection(ctx context.Context, docIDsByCollection map[string][]string) (int64, error) {
	startTime := time.Now()
	totalPurged := int64(0)
	var errs []error

	cols := make([]string, 0, len(docIDsByCollection))
	for col := range docIDsByCollection {
		cols = append(cols, col)
	}
	sort.Strings(cols)

	for _, colName := range cols {
		docIDs := docIDsByCollection[colName]
		if len(docIDs) == 0 {
			continue
		}
		purged, err := p.purgeByDocIDs(ctx, colName, docIDs)
		totalPurged += purged
		if err != nil {
			errs = append(errs, fmt.Errorf("purge %s: %w", colName, err))
		}
	}

	logger.Sugar.Infof("Purge complete: removed %d docs in %v", totalPurged, time.Since(startTime))

	if len(errs) > 0 {
		return totalPurged, fmt.Errorf("collection purge errors: %w", errors.Join(errs...))
	}
	return totalPurged, nil
}

// ─── Document operations ─────────────────────────────────────────────────────

// purgeByDocIDs deletes documents by their docIDs.
// Returns the count of successfully purged documents and an error if any
// docIDs were invalid (skipped) or if the purge operation itself failed.
func (p *Pruner) purgeByDocIDs(ctx context.Context, collectionName string, docIDs []string) (int64, error) {
	if len(docIDs) == 0 {
		return 0, nil
	}

	startTime := time.Now()
	logger.Sugar.Infof("Purging %d documents from %s", len(docIDs), collectionName)

	col, err := p.defraNode.DB.GetCollectionByName(ctx, collectionName)
	if err != nil {
		return 0, fmt.Errorf("failed to get collection %s: %w", collectionName, err)
	}

	clientDocIDs := make([]client.DocID, 0, len(docIDs))
	skipped := 0
	for _, id := range docIDs {
		docID, err := client.NewDocIDFromString(id)
		if err != nil {
			logger.Sugar.Warnf("Skipping invalid docID %s: %v", id, err)
			skipped++
			continue
		}
		clientDocIDs = append(clientDocIDs, docID)
	}

	if err := col.PurgeByDocIDs(ctx, clientDocIDs, p.cfg.PruneHistory); err != nil {
		return 0, err
	}

	count := int64(len(clientDocIDs))
	logger.Sugar.Infof("Purged %d/%d documents from %s in %v",
		count, len(docIDs), collectionName, time.Since(startTime))

	if skipped > 0 {
		return count, fmt.Errorf("skipped %d invalid docID(s) out of %d", skipped, len(docIDs))
	}
	return count, nil
}

// ─── Block number queries ────────────────────────────────────────────────────

// getBlockRange returns the lowest and highest block numbers from the chain.
// Returns (0, 0, ErrNoBlocks) if the database has no blocks (chain returns
// errors.IsErrNotFound on an empty DB).
func (p *Pruner) getBlockRange(ctx context.Context) (lowest, highest int64, err error) {
	lowest, err = p.chain.GetLowestStoredBlockNumber(ctx)
	if err != nil {
		if pkgerrors.IsErrNotFound(err) {
			return 0, 0, ErrNoBlocks
		}
		if errors.Is(err, defra.ErrBlockNumberCorrupt) {
			return 0, 0, fmt.Errorf("get lowest block: %w", ErrNoValidBlocks)
		}
		return 0, 0, fmt.Errorf("get lowest block: %w", err)
	}
	highest, err = p.chain.GetHighestStoredBlockNumber(ctx)
	if err != nil {
		if pkgerrors.IsErrNotFound(err) {
			return 0, 0, ErrNoBlocks
		}
		if errors.Is(err, defra.ErrBlockNumberCorrupt) {
			return 0, 0, fmt.Errorf("get highest block: %w", ErrNoValidBlocks)
		}
		return 0, 0, fmt.Errorf("get highest block: %w", err)
	}
	return lowest, highest, nil
}
