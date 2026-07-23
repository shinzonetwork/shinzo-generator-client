package pruner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/node"
)

// ErrNoBlocks indicates that the query succeeded but no blocks were found.
var ErrNoBlocks = errors.New("no blocks found")

// ErrNoValidBlocks indicates that blocks exist in the database but none have
// a parseable block number field. This is a data-integrity issue, not an
// empty database — distinct from ErrNoBlocks.
var ErrNoValidBlocks = errors.New("blocks exist but none have a valid block number")

// ErrNoValidDocs indicates that documents exist in a collection but none have
// a parseable block number field. This is a data-integrity issue, not an
// empty result set — distinct from the (nil, nil) "no docs in range" return.
var ErrNoValidDocs = errors.New("docs exist but none have a valid block number")

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
	queue       Queue
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
	blockCount := int64(q.Len())

	if blockCount <= p.cfg.MaxBlocks {
		return p.filterBasedPrune(ctx)
	}

	result := q.Drain(int(p.cfg.MaxBlocks), p.collections)
	if result == nil {
		return nil
	}

	logger.Sugar.Infof("Pruning %d blocks (queue had %d blocks, keeping %d, prune_history=%v)",
		result.BlockCount, blockCount, p.cfg.MaxBlocks, p.cfg.PruneHistory)

	return p.purgeFromDrainResult(ctx, result)
}

// purgeFromDrainResult deletes documents from a DrainResult.
// Deletes dependent collections first, then the block collection last.
func (p *Pruner) purgeFromDrainResult(ctx context.Context, result *DrainResult) error {
	startTime := time.Now()
	totalPurged := int64(0)
	var depErrs []error

	// Dependent collections first, block collection last
	for _, colName := range p.collections.DependentCollections {
		docIDs, ok := result.DocIDsByCollection[colName]
		if !ok || len(docIDs) == 0 {
			continue
		}
		purged, err := p.purgeByDocIDs(ctx, colName, docIDs)
		totalPurged += purged
		if err != nil {
			depErrs = append(depErrs, fmt.Errorf("purge %s: %w", colName, err))
		}
	}

	if blockIDs, ok := result.DocIDsByCollection[p.collections.BlockCollection]; ok && len(blockIDs) > 0 {
		purged, err := p.purgeByDocIDs(ctx, p.collections.BlockCollection, blockIDs)
		totalPurged += purged
		if err != nil {
			return fmt.Errorf("failed to purge blocks: %w", err)
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

	if len(depErrs) > 0 {
		return fmt.Errorf("dependent collection errors: %w", errors.Join(depErrs...))
	}
	return nil
}

// startupCleanup removes blocks left over from previous runs that aren't in the queue.
func (p *Pruner) startupCleanup(ctx context.Context) error {
	lowest, highest, err := p.getBlockRange(ctx)
	if err != nil {
		if errors.Is(err, ErrNoBlocks) {
			logger.Sugar.Debugf("No existing blocks in database")
			return nil
		}
		if errors.Is(err, ErrNoValidBlocks) {
			logger.Sugar.Warnf("Blocks exist but none have valid block numbers, skipping cleanup")
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
		if errors.Is(err, ErrNoValidBlocks) {
			logger.Sugar.Warnf("Blocks exist but none have valid block numbers, skipping prune")
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
// Uses order+limit queries to get docIDs, then purges them.
// Safe to call with concurrent P2P replication — merge handles missing blocks gracefully.
func (p *Pruner) pruneBlockRange(ctx context.Context, startBlock, endBlock int64) (int64, error) {
	totalPurged := int64(0)
	var depErrs []error

	logger.Sugar.Infof("pruneBlockRange: deleting blocks %d-%d (%d blocks)",
		startBlock, endBlock, endBlock-startBlock+1)

	// Dependent collections first, block collection last
	for _, colName := range p.collections.DependentCollections {
		docIDs, err := p.queryOldestDocIDs(ctx, colName, constants.BlockNumberKeyValue, endBlock)
		if err != nil {
			depErrs = append(depErrs, fmt.Errorf("query %s: %w", colName, err))
			continue
		}
		if len(docIDs) > 0 {
			purged, err := p.purgeByDocIDs(ctx, colName, docIDs)
			totalPurged += purged
			if err != nil {
				depErrs = append(depErrs, fmt.Errorf("purge %s: %w", colName, err))
			}
		}
	}

	blockDocIDs, err := p.queryOldestDocIDs(ctx, p.collections.BlockCollection, p.collections.BlockNumberField, endBlock)
	if err != nil {
		return totalPurged, fmt.Errorf("query failed for blocks: %w", err)
	}
	if len(blockDocIDs) > 0 {
		purged, err := p.purgeByDocIDs(ctx, p.collections.BlockCollection, blockDocIDs)
		totalPurged += purged
		if err != nil {
			return totalPurged, fmt.Errorf("failed to purge blocks: %w", err)
		}
	}

	logger.Sugar.Infof("pruneBlockRange: purged %d docs for blocks %d-%d", totalPurged, startBlock, endBlock)

	if len(depErrs) > 0 {
		return totalPurged, fmt.Errorf("dependent collection errors: %w", errors.Join(depErrs...))
	}
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

	// Data is nil when the query returns no result set at all (no errors, no data key).
	if result.GQL.Data == nil {
		return nil, nil // This is a legitimate empty result, not an error.
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected GQL data type %T for %s", result.GQL.Data, collectionName)
	}

	// DefraDB may return []map[string]interface{} or []interface{} depending on context.
	// In Go these are distinct types, so we must handle both.
	raw, ok := data[collectionName]
	if !ok {
		return nil, fmt.Errorf("collection %s not found in GQL response", collectionName)
	}
	// The collection key exists but maps to nil — DefraDB returns nil for
	// collections that exist in the schema but contain zero documents.
	if raw == nil {
		return nil, nil // This is a legitimate empty result, not an error.
	}

	switch docs := raw.(type) {
	case []map[string]any:
		return extractDocIDs(docs, fieldName, maxBlockNumber, collectionName)
	case []any:
		typed := make([]map[string]any, 0, len(docs))
		for _, doc := range docs {
			docMap, ok := doc.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unexpected element type %T in %s", doc, collectionName)
			}
			typed = append(typed, docMap)
		}
		return extractDocIDs(typed, fieldName, maxBlockNumber, collectionName)
	default:
		return nil, fmt.Errorf("unexpected collection result type %T for %s", raw, collectionName)
	}
}

// extractDocIDs collects docIDs for documents whose block number is within the
// allowed range. Docs with nil or unparseable block numbers, or non-string
// _docID fields, are counted and skipped — a single corrupt doc must not
// prevent pruning of all other valid docs. A single summary warning is logged
// if any docs were skipped, instead of one log line per corrupt doc.
//
// Returns (nil, ErrNoValidDocs) when docs exist but none have a parseable
// block number — this is a data-integrity issue distinct from "no docs in
// range", which returns (nil, nil).
func extractDocIDs(docs []map[string]any, fieldName string, maxBlockNumber int64, collectionName string) ([]string, error) {
	var docIDs []string
	skipped := 0
	parsedAny := false
	for _, docMap := range docs {
		blockNumber, err := parseBlockNumber(docMap[fieldName])
		if err != nil {
			skipped++
			continue
		}
		parsedAny = true
		if blockNumber > maxBlockNumber {
			break
		}
		docID, ok := docMap["_docID"].(string)
		if !ok {
			skipped++
			continue
		}
		docIDs = append(docIDs, docID)
	}
	if skipped > 0 {
		logger.Sugar.Warnf("Skipped %d doc(s) in %s with missing or unparseable fields", skipped, collectionName)
	}
	if len(docs) > 0 && !parsedAny {
		return nil, fmt.Errorf("%s: %w", collectionName, ErrNoValidDocs)
	}
	return docIDs, nil
}

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

// getBlockRange returns the lowest and highest block numbers from the database.
// Returns (0, 0, ErrNoBlocks) if the database has no blocks.
// Returns (0, 0, ErrNoValidBlocks) if blocks exist but none have parseable numbers.
// Returns (0, 0, err) for query or type errors.
// Returns (lowest, highest, nil) when valid block numbers are found.
func (p *Pruner) getBlockRange(ctx context.Context) (lowest, highest int64, err error) {
	lowest, err = p.getLowestBlockNumber(ctx)
	if err != nil {
		return 0, 0, err
	}
	highest, err = p.getHighestBlockNumber(ctx)
	if err != nil {
		return 0, 0, err
	}
	return lowest, highest, nil
}

func (p *Pruner) getLowestBlockNumber(ctx context.Context) (int64, error) {
	query := `query {
		` + p.collections.BlockCollection + ` (order: {` + p.collections.BlockNumberField + `: ASC}, limit: 7) {
			` + p.collections.BlockNumberField + `
		}
	}`

	result := p.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, fmt.Errorf("lowest block query failed: %w", result.GQL.Errors[0])
	}

	return p.extractBlockNumber(result.GQL.Data)
}

func (p *Pruner) getHighestBlockNumber(ctx context.Context) (int64, error) {
	query := `query {
		` + p.collections.BlockCollection + ` (order: {` + p.collections.BlockNumberField + `: DESC}, limit: 7) {
			` + p.collections.BlockNumberField + `
		}
	}`

	result := p.defraNode.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, fmt.Errorf("highest block query failed: %w", result.GQL.Errors[0])
	}

	return p.extractBlockNumber(result.GQL.Data)
}

func (p *Pruner) extractBlockNumber(gqlData any) (int64, error) {
	if gqlData == nil {
		return 0, ErrNoBlocks
	}

	data, ok := gqlData.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("unexpected GQL data type %T", gqlData)
	}

	blocksRaw, ok := data[p.collections.BlockCollection]
	if !ok {
		return 0, fmt.Errorf("collection %s not found in GQL response", p.collections.BlockCollection)
	}

	if blocksTyped, ok := blocksRaw.([]map[string]any); ok {
		if len(blocksTyped) == 0 {
			return 0, ErrNoBlocks
		}
		return p.findFirstValidBlockNumber(blocksTyped)
	}

	blocks, ok := blocksRaw.([]any)
	if !ok {
		if blocksRaw == nil {
			return 0, ErrNoBlocks
		}
		return 0, fmt.Errorf("unexpected blocks type %T", blocksRaw)
	}
	if len(blocks) == 0 {
		return 0, ErrNoBlocks
	}

	typed := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("unexpected block element type %T", block)
		}
		typed = append(typed, blockMap)
	}
	return p.findFirstValidBlockNumber(typed)
}

// findFirstValidBlockNumber iterates through blocks and returns the first valid
// block number. Blocks with missing or unparseable number fields are counted and
// skipped — this is not propagated as an error because a single corrupt block
// (e.g. from P2P replication with a nil number field) must not prevent pruning
// of all other valid blocks. A single summary warning is logged if any blocks
// were skipped, instead of one log line per corrupt block.
func (p *Pruner) findFirstValidBlockNumber(blocks []map[string]any) (int64, error) {
	skipped := 0
	for _, block := range blocks {
		number, ok := block[p.collections.BlockNumberField]
		if !ok {
			skipped++
			continue
		}
		blockNumber, err := parseBlockNumber(number)
		if err != nil {
			skipped++
			continue
		}
		if skipped > 0 {
			logger.Sugar.Warnf("Skipped %d block(s) with missing or unparseable number fields", skipped)
		}
		return blockNumber, nil
	}
	return 0, ErrNoValidBlocks
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
	return 0, fmt.Errorf("unexpected block number type %T", number)
}
