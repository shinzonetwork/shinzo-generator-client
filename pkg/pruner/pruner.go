package pruner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// Pruner handles periodic removal of old blockchain documents from DefraDB.
// It supports two queue types:
//   - IndexerQueue: for indexers that track docIDs at creation time
//   - EventQueue: for hosts that track docIDs from P2P replication events
//
// When no queue is set or the queue is underfilled, falls back to filter-based pruning.
type Pruner struct {
	cfg         *Config
	collections CollectionConfig
	defraNode   *node.Node
	queue       PrunerQueue // IndexerQueue or EventQueue
	stopChan    chan struct{}
	wg          sync.WaitGroup
	mu          sync.RWMutex

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
func NewPruner(cfg *Config, defraNode *node.Node, collections ...CollectionConfig) *Pruner {
	cols := DefaultCollectionConfig()
	if len(collections) > 0 {
		cols = collections[0]
	}
	return &Pruner{
		cfg:         cfg,
		collections: cols,
		defraNode:   defraNode,
		stopChan:    make(chan struct{}),
	}
}

// SetQueue sets the queue implementation for queue-based pruning.
func (p *Pruner) SetQueue(queue PrunerQueue) {
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
			p.runStorageGC()
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
	blockCount := int64(q.Len())

	if blockCount <= p.cfg.MaxBlocks {
		return p.filterBasedPrune(ctx)
	}

	result := q.Drain(int(p.cfg.MaxBlocks), p.collections)
	if result == nil {
		return nil
	}

	logger.Sugar.Infof("Pruning %d blocks (queue had %d blocks, keeping %d)",
		result.BlockCount, blockCount, p.cfg.MaxBlocks)

	return p.purgeFromDrainResult(ctx, result)
}

// purgeFromDrainResult deletes documents from a DrainResult.
// Deletes dependent collections first, then the block collection last.
func (p *Pruner) purgeFromDrainResult(ctx context.Context, result *DrainResult) error {
	startTime := time.Now()
	totalPurged := int64(0)

	// Dependent collections first, block collection last
	for _, colName := range p.collections.DependentCollections {
		docIDs, ok := result.DocIDsByCollection[colName]
		if !ok || len(docIDs) == 0 {
			continue
		}
		purged, err := p.purgeByDocIDs(ctx, colName, docIDs)
		if err != nil {
			logger.Sugar.Errorf("Failed to purge %s: %v", colName, err)
		} else {
			totalPurged += purged
		}
	}

	if blockIDs, ok := result.DocIDsByCollection[p.collections.BlockCollection]; ok && len(blockIDs) > 0 {
		purged, err := p.purgeByDocIDs(ctx, p.collections.BlockCollection, blockIDs)
		if err != nil {
			logger.Sugar.Errorf("Failed to purge blocks: %v", err)
		} else {
			totalPurged += purged
		}
	}

	elapsed := time.Since(startTime)
	logger.Sugar.Infof("Prune complete: removed %d docs for %d blocks in %v",
		totalPurged, result.BlockCount, elapsed)

	p.mu.Lock()
	p.totalBlocksPruned += int64(result.BlockCount)
	p.totalDocsPruned += totalPurged
	p.lastPruneTime = time.Now()
	p.mu.Unlock()

	return nil
}

// startupCleanup removes blocks left over from previous runs that aren't in the queue.
func (p *Pruner) startupCleanup(ctx context.Context) error {
	lowest, err := p.getLowestBlockNumber(ctx)
	if err != nil {
		return err
	}

	highest, err := p.getHighestBlockNumber(ctx)
	if err != nil {
		return err
	}

	if lowest == 0 && highest == 0 {
		logger.Sugar.Debugf("No existing blocks in database")
		return nil
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

// runStorageGC reclaims disk space from deleted entries by running Badger value log GC.
func (p *Pruner) runStorageGC() {
	if p.defraNode == nil {
		return
	}
	startTime := time.Now()
	if err := p.defraNode.RunStorageGC(); err != nil {
		logger.Sugar.Debugf("Storage GC error (non-fatal): %v", err)
	}
	elapsed := time.Since(startTime)
	if elapsed > time.Second {
		logger.Sugar.Infof("Storage GC completed in %v", elapsed)
	}
}

// filterBasedPrune checks the actual DB block count and prunes excess blocks.
// Used by the indexer queue (no P2P) and as a fallback when the queue is underfilled.
func (p *Pruner) filterBasedPrune(ctx context.Context) error {
	highest, err := p.getHighestBlockNumber(ctx)
	if err != nil || highest == 0 {
		return nil
	}

	lowest, err := p.getLowestBlockNumber(ctx)
	if err != nil || lowest == 0 {
		return nil
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
// Uses order+limit queries to get docIDs, then purges them.
// Safe to call with concurrent P2P replication — merge handles missing blocks gracefully.
func (p *Pruner) pruneBlockRange(ctx context.Context, startBlock, endBlock int64) (int64, error) {
	totalPurged := int64(0)

	logger.Sugar.Infof("pruneBlockRange: deleting blocks %d-%d (%d blocks)",
		startBlock, endBlock, endBlock-startBlock+1)

	// Dependent collections first, block collection last
	for _, colName := range p.collections.DependentCollections {
		docIDs, err := p.queryOldestDocIDs(ctx, colName, "blockNumber", endBlock)
		if err != nil {
			logger.Sugar.Warnf("pruneBlockRange: query failed for %s (skipping): %v", colName, err)
			continue
		}
		if len(docIDs) > 0 {
			purged, err := p.purgeByDocIDs(ctx, colName, docIDs)
			if err != nil {
				logger.Sugar.Warnf("pruneBlockRange: failed to purge %s: %v", colName, err)
			} else {
				totalPurged += purged
			}
		}
	}

	blockDocIDs, err := p.queryOldestDocIDs(ctx, p.collections.BlockCollection, p.collections.BlockNumberField, endBlock)
	if err != nil {
		return totalPurged, fmt.Errorf("query failed for blocks: %w", err)
	}
	if len(blockDocIDs) > 0 {
		purged, err := p.purgeByDocIDs(ctx, p.collections.BlockCollection, blockDocIDs)
		if err != nil {
			return totalPurged, fmt.Errorf("failed to purge blocks: %w", err)
		}
		totalPurged += purged
	}

	logger.Sugar.Infof("pruneBlockRange: purged %d docs for blocks %d-%d", totalPurged, startBlock, endBlock)
	return totalPurged, nil
}

// ─── Document operations ─────────────────────────────────────────────────────

// queryOldestDocIDs queries for docIDs where fieldName <= maxBlockNumber using order+limit.
// Works on P2P-replicated data where filter queries return empty results.
func (p *Pruner) queryOldestDocIDs(ctx context.Context, collectionName, fieldName string, maxBlockNumber int64) ([]string, error) {
	limit := 50000
	query := fmt.Sprintf(`query {
		%s(order: { %s: ASC }, limit: %d) {
			_docID
			%s
		}
	}`, collectionName, fieldName, limit, fieldName)

	result := p.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return nil, fmt.Errorf("query failed for %s: %w", collectionName, result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil, nil
	}

	// DefraDB may return []map[string]interface{} or []interface{} depending on context.
	// In Go these are distinct types, so we must handle both.
	raw := data[collectionName]

	var docIDs []string

	switch docs := raw.(type) {
	case []map[string]any:
		for _, docMap := range docs {
			bn, err := parseBlockNumber(docMap[fieldName])
			if err != nil || bn > maxBlockNumber {
				break // ordered ASC, so once we exceed maxBlockNumber we're done
			}
			if docID, ok := docMap["_docID"].(string); ok {
				docIDs = append(docIDs, docID)
			}
		}
	case []any:
		for _, doc := range docs {
			docMap, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			bn, err := parseBlockNumber(docMap[fieldName])
			if err != nil || bn > maxBlockNumber {
				break
			}
			if docID, ok := docMap["_docID"].(string); ok {
				docIDs = append(docIDs, docID)
			}
		}
	default:
		return nil, nil
	}

	return docIDs, nil
}

// purgeByDocIDs deletes documents by their docIDs.
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

	result, err := col.PurgeByDocIDs(ctx, docIDs, p.cfg.PruneHistory)
	if err != nil {
		return 0, err
	}

	logger.Sugar.Infof("Purged %d/%d documents from %s in %v",
		result.Count, len(docIDs), collectionName, time.Since(startTime))
	return result.Count, nil
}

// ─── Block number queries ────────────────────────────────────────────────────

func (p *Pruner) getLowestBlockNumber(ctx context.Context) (int64, error) {
	query := `query {
		` + p.collections.BlockCollection + ` (order: {` + p.collections.BlockNumberField + `: ASC}, limit: 1) {
			` + p.collections.BlockNumberField + `
		}
	}`

	result := p.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, result.GQL.Errors[0]
	}

	return p.extractBlockNumber(result.GQL.Data)
}

func (p *Pruner) getHighestBlockNumber(ctx context.Context) (int64, error) {
	query := `query {
		` + p.collections.BlockCollection + ` (order: {` + p.collections.BlockNumberField + `: DESC}, limit: 1) {
			` + p.collections.BlockNumberField + `
		}
	}`

	result := p.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, result.GQL.Errors[0]
	}

	return p.extractBlockNumber(result.GQL.Data)
}

func (p *Pruner) extractBlockNumber(gqlData any) (int64, error) {
	data, ok := gqlData.(map[string]any)
	if !ok {
		return 0, nil
	}

	blocksRaw := data[p.collections.BlockCollection]

	if blocksTyped, ok := blocksRaw.([]map[string]any); ok {
		if len(blocksTyped) == 0 {
			return 0, nil
		}
		if number, ok := blocksTyped[0][p.collections.BlockNumberField]; ok {
			return parseBlockNumber(number)
		}
		return 0, nil
	}

	blocks, ok := blocksRaw.([]any)
	if !ok || len(blocks) == 0 {
		return 0, nil
	}

	block, ok := blocks[0].(map[string]any)
	if !ok {
		return 0, nil
	}

	if number, ok := block[p.collections.BlockNumberField]; ok {
		return parseBlockNumber(number)
	}
	return 0, nil
}

func parseBlockNumber(number any) (int64, error) {
	switch v := number.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	}
	return 0, nil
}
