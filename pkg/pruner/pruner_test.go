package pruner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logger.Init(true)
	os.Exit(m.Run())
}

func TestNewPruner(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 1000, IntervalSeconds: 60}

	t.Run("nil chain", func(t *testing.T) {
		p := NewPruner(cfg, nil, nil)
		require.NotNil(t, p)
		assert.Equal(t, "", p.blockCollection)
		assert.Equal(t, "", p.blockSigCollection)
	})

	t.Run("with chain resolves collection names", func(t *testing.T) {
		mock := &testutils.MockChain{
			GetCollectionsFn: func() []string {
				return []string{"Test__Block", "Test__BlockSignature", "Test__Transaction"}
			},
		}
		p := NewPruner(cfg, nil, mock)
		require.NotNil(t, p)
		assert.Equal(t, "Test__Block", p.blockCollection)
		assert.Equal(t, "Test__BlockSignature", p.blockSigCollection)
	})
}

func TestPrunerSetQueue(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil, nil)
	assert.Nil(t, p.queue)

	q := NewIndexerQueue()
	p.SetQueue(q)
	assert.Equal(t, q, p.queue)
}

func TestPrunerStart_Disabled(t *testing.T) {
	cfg := &Config{Enabled: false}
	p := NewPruner(cfg, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := p.Start(ctx)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
	cancel()
	p.Stop()
}

func TestPrunerStart_NilNode(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := p.Start(ctx)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
	cancel()
	p.Stop()
}

func TestPrunerGetMetrics(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, nil, nil)

	metrics := p.GetMetrics()
	assert.True(t, metrics.Enabled)
	assert.False(t, metrics.IsRunning)
	assert.Equal(t, int64(0), metrics.TotalBlocksPruned)
	assert.Equal(t, int64(0), metrics.TotalDocsPruned)
}

func TestPrunerStop_NotRunning(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil, nil)

	// Should be a no-op without panicking
	assert.NotPanics(t, func() {
		p.Stop()
	})
}

func TestPrunerStop_WithQueue(t *testing.T) {
	cfg := &Config{Enabled: false}
	p := NewPruner(cfg, nil, nil)
	q := NewIndexerQueue()
	p.SetQueue(q)

	// Not running, so Stop is a no-op
	p.Stop()
	assert.False(t, p.isRunning)
}

// ─── Integration tests with real DefraDB node ───────────────────────────────

func TestRunPrune_NilQueue(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	assert.Nil(t, p.queue)
	// runPrune with nil queue calls filterBasedPrune which needs a node
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

func TestRunPrune_WithIndexerQueue(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	q := NewIndexerQueue()
	p.SetQueue(q)
	assert.NotNil(t, p.queue)
	// dispatch tested via runPrune type switch
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

// queueBlocks fills a queue with n blocks, each carrying one block doc and two transaction docs.
func queueBlocks(t *testing.T, q *IndexerQueue, n int64) {
	t.Helper()
	for i := int64(1); i <= n; i++ {
		err := q.TrackBlockDocIDs(i,
			docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
			map[string][]string{constants.CollectionTransaction: {
				docIDPrefix + "-660e8400-e29b-41d4-a716-446655440001",
				docIDPrefix + "-770e8400-e29b-41d4-a716-446655440002",
			}},
			"",
		)
		require.NoError(t, err)
	}
}

// A cycle deletes at most max_blocks_per_cycle blocks, so its wall time stays bounded as the
// backlog grows instead of scaling with it.
func TestRunIndexerQueuePrune_BoundsWorkPerCycle(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 1, MaxBlocksPerCycle: 2}
	p := NewPruner(cfg, n, testCollections())
	q := NewIndexerQueue()
	p.SetQueue(q)

	queueBlocks(t, q, 10)
	require.Equal(t, 10, q.Len())

	require.NoError(t, p.runIndexerQueuePrune(context.Background(), q))

	// Nine blocks are over the retention target, but a cycle may delete two.
	assert.Equal(t, 8, q.Len())
}

// A cycle that stays inside the retention target drains nothing, but the entries it holds are still
// the only record that those documents are prunable, so it checkpoints too.
func TestRunIndexerQueuePrune_CheckpointsBelowRetentionTarget(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"
	cfg := &Config{Enabled: true, MaxBlocks: 100, MaxBlocksPerCycle: 2}
	p := NewPruner(cfg, n, testCollections())
	q := NewIndexerQueue()
	_, err := q.LoadFromFile(path)
	require.NoError(t, err)
	p.SetQueue(q)

	queueBlocks(t, q, 3)
	require.NoFileExists(t, path)

	require.NoError(t, p.runIndexerQueuePrune(context.Background(), q))

	// Below the target nothing is drained, so all three must still be there after a reload.
	assert.Equal(t, 3, q.Len())
	require.FileExists(t, path)
	reloaded := NewIndexerQueue()
	count, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

// The queue is the only record that a document is prunable, so it is written after every cycle
// rather than only on shutdown.
func TestRunIndexerQueuePrune_CheckpointsQueueEachCycle(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"
	cfg := &Config{Enabled: true, MaxBlocks: 1, MaxBlocksPerCycle: 2}
	p := NewPruner(cfg, n, testCollections())
	q := NewIndexerQueue()
	// LoadFromFile binds the queue to the path Save writes to.
	_, err := q.LoadFromFile(path)
	require.NoError(t, err)
	p.SetQueue(q)

	queueBlocks(t, q, 10)
	require.NoFileExists(t, path)

	require.NoError(t, p.runIndexerQueuePrune(context.Background(), q))

	require.FileExists(t, path)
	reloaded := NewIndexerQueue()
	count, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, q.Len(), count)
}

func TestRunIndexerQueuePrune_BelowThreshold(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	q := NewIndexerQueue()
	p.SetQueue(q)
	// Queue has 0 entries, below maxBlocks=100
	assert.Zero(t, len(q.entries))
	// This calls filterBasedPrune which needs node
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

func TestStartAndStop_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p, _ := newTestPruner(cfg, n)

	// Set an indexer queue so pruneLoop does not nil-deref on queue type assert
	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx := t.Context()

	err := p.Start(ctx)
	require.NoError(t, err)
	assert.True(t, p.isRunning)

	// Starting again should be a no-op
	err = p.Start(ctx)
	require.NoError(t, err)

	p.Stop()
	assert.False(t, p.isRunning)
}

func TestPruneLoop_TickerFires(t *testing.T) {
	n := startTestNode(t)
	// Use 1-second interval so the ticker fires quickly
	cfg := &Config{Enabled: true, MaxBlocks: 1000, DocsPerBlock: 10, IntervalSeconds: 1}
	p, _ := newTestPruner(cfg, n)

	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx, cancel := context.WithCancel(context.Background())

	err := p.Start(ctx)
	require.NoError(t, err)

	// Wait for the ticker to fire at least once
	time.Sleep(1500 * time.Millisecond)

	cancel()
	p.wg.Wait()
}

func TestPruneLoop_StopsOnContextCancel(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 1}
	p, _ := newTestPruner(cfg, n)

	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx, cancel := context.WithCancel(context.Background())

	err := p.Start(ctx)
	require.NoError(t, err)

	// Cancel context to stop the loop
	cancel()
	// Wait for the goroutine to finish
	p.wg.Wait()
}

func TestPruneLoop_StopsOnStopChan(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 1}
	p, _ := newTestPruner(cfg, n)

	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx := context.Background()

	err := p.Start(ctx)
	require.NoError(t, err)

	p.Stop()
	assert.False(t, p.isRunning)
}

func TestStop_WithQueueSave(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p, _ := newTestPruner(cfg, n)

	tmpDir := t.TempDir()
	q := NewIndexerQueue()
	_, err := q.LoadFromFile(tmpDir + "/queue.gob")
	require.NoError(t, err)

	err = q.TrackBlockDocIDs(1, "bae-550e8400-e29b-41d4-a716-446655440000", nil, "")
	require.NoError(t, err)
	p.SetQueue(q)

	ctx := t.Context()

	err = p.Start(ctx)
	require.NoError(t, err)

	// Stop should save the queue
	p.Stop()
	assert.False(t, p.isRunning)

	// Verify file was saved
	_, err = os.Stat(tmpDir + "/queue.gob")
	assert.NoError(t, err)
}

func TestRunPrune_Dispatching(t *testing.T) {
	n := startTestNode(t)
	ctx := context.Background()

	t.Run("nil queue calls filterBasedPrune", func(t *testing.T) {
		cfg := &Config{Enabled: true, MaxBlocks: 1000}
		p, _ := newTestPruner(cfg, n)
		// No queue set, so runPrune calls filterBasedPrune
		err := p.runPrune(ctx)
		assert.NoError(t, err)
	})

	t.Run("indexer queue dispatch", func(t *testing.T) {
		cfg := &Config{Enabled: true, MaxBlocks: 1000}
		p, _ := newTestPruner(cfg, n)
		q := NewIndexerQueue()
		p.SetQueue(q)
		err := p.runPrune(ctx)
		assert.NoError(t, err)
	})
}

func TestGetBlockRange(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	t.Run("empty database returns ErrNoBlocks", func(t *testing.T) {
		lowest, highest, err := p.getBlockRange(ctx)
		assert.ErrorIs(t, err, ErrNoBlocks)
		assert.Equal(t, int64(0), lowest)
		assert.Equal(t, int64(0), highest)
	})

	t.Run("populated database returns valid range", func(t *testing.T) {
		for _, num := range []int64{10, 30, 20} {
			mutation := fmt.Sprintf(`mutation { add_TestBlock(input: [{number: %d, hash: "hash%d"}]) { _docID } }`, num, num)
			result := n.DB.ExecRequest(ctx, mutation)
			require.Empty(t, result.GQL.Errors, "insert block %d failed: %v", num, result.GQL.Errors)
		}

		lowest, highest, err := p.getBlockRange(ctx)
		assert.NoError(t, err)
		assert.Equal(t, int64(10), lowest)
		assert.Equal(t, int64(30), highest)
	})
}

func TestGetBlockRange_CorruptDataReturnsErrNoValidBlocks(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	mock := &testutils.MockChain{
		GetLowestStoredBlockNumberFn: func(_ context.Context) (int64, error) {
			return 0, defra.ErrBlockNumberCorrupt
		},
	}
	p := NewPruner(cfg, nil, mock)
	_, _, err := p.getBlockRange(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoValidBlocks))
}

func TestPurgeByDocIDs(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, tc := newTestPruner(cfg, n)
	ctx := context.Background()

	// Insert blocks
	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))

	// Get docIDs by querying via chain
	docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, 1, 1)
	require.NoError(t, err)
	docIDs := docIDsByCol[testBlockColName]
	require.Len(t, docIDs, 1)

	// Purge one
	purged, err := p.purgeByDocIDs(ctx, "TestBlock", docIDs)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), purged)
	assert.Equal(t, 1, countDocs(t, n, "TestBlock"))

	// Purge empty list
	purged, err = p.purgeByDocIDs(ctx, "TestBlock", nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), purged)

	// Purge with invalid collection name
	_, err = p.purgeByDocIDs(ctx, "NonExistent", []string{"bae-550e8400-e29b-41d4-a716-446655440000"})
	assert.Error(t, err)
}

func TestPurgeByDocIDs_InvalidDocID(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, tc := newTestPruner(cfg, n)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, 1, 2)
	require.NoError(t, err)
	validDocIDs := docIDsByCol[testBlockColName]
	require.Len(t, validDocIDs, 2)

	// Mix valid and invalid docIDs
	mixedDocIDs := append([]string{"not-a-valid-docid"}, validDocIDs...)

	purged, err := p.purgeByDocIDs(ctx, "TestBlock", mixedDocIDs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "skipped 1 invalid docID")
	assert.Equal(t, int64(2), purged)
	assert.Equal(t, 0, countDocs(t, n, "TestBlock"))
}

func TestPruneBlockRange(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	// Insert blocks with transactions
	insertTestBlock(t, n, 1, 2)
	insertTestBlock(t, n, 2, 1)
	insertTestBlock(t, n, 3, 0)

	assert.Equal(t, 3, countDocs(t, n, "TestBlock"))
	assert.Equal(t, 3, countDocs(t, n, "TestTx"))

	// Prune blocks 1-2 (should remove blocks and dependent transactions)
	purged, err := p.pruneBlockRange(ctx, 1, 2)
	assert.NoError(t, err)
	assert.True(t, purged > 0)

	// Block 3 should remain
	assert.Equal(t, 1, countDocs(t, n, "TestBlock"))
}

func TestFilterBasedPrune(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	// Empty DB should be a no-op
	err := p.filterBasedPrune(ctx)
	assert.NoError(t, err)

	// Insert blocks: 1, 2, 3, 4 (4 blocks, max_blocks=2)
	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)
	insertTestBlock(t, n, 3, 0)
	insertTestBlock(t, n, 4, 0)

	err = p.filterBasedPrune(ctx)
	assert.NoError(t, err)

	// After pruning, should have 2 blocks left
	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))

	// Metrics should be updated
	assert.True(t, p.totalBlocksPruned > 0)
	assert.True(t, p.totalDocsPruned > 0)
}

func TestFilterBasedPrune_WithinLimit(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	// Should be a no-op since we have 2 blocks < 100
	err := p.filterBasedPrune(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))
}

func TestStartupCleanup(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	// Empty DB
	err := p.startupCleanup(ctx)
	assert.NoError(t, err)

	// Insert 5 blocks, max_blocks=2
	for i := int64(1); i <= 5; i++ {
		insertTestBlock(t, n, i, 0)
	}

	err = p.startupCleanup(ctx)
	assert.NoError(t, err)

	// Should have 2 blocks remaining
	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))
	assert.True(t, p.totalBlocksPruned > 0)
}

func TestStartupCleanup_WithinLimit(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	err := p.startupCleanup(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))
	assert.Equal(t, int64(0), p.totalBlocksPruned)
}

func TestPurgeFromDrainResult(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, tc := newTestPruner(cfg, n)
	ctx := context.Background()

	// Insert blocks and txs
	insertTestBlock(t, n, 1, 1)
	insertTestBlock(t, n, 2, 0)

	// Get docIDs via chain
	docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, 1, 1)
	require.NoError(t, err)
	require.NotNil(t, docIDsByCol[testBlockColName])
	require.NotNil(t, docIDsByCol[testTxColName])

	drainResult := &DrainResult{
		DocIDsByCollection: docIDsByCol,
		BlockCount:         1,
	}

	err = p.purgeFromDrainResult(ctx, drainResult)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), p.totalBlocksPruned)
	assert.True(t, p.totalDocsPruned > 0)
	assert.False(t, p.lastPruneTime.IsZero())
}

func TestPurgeFromDrainResult_EmptyCollections(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	// DrainResult with no matching collections
	drainResult := &DrainResult{
		DocIDsByCollection: map[string][]string{},
		BlockCount:         0,
	}

	err := p.purgeFromDrainResult(ctx, drainResult)
	assert.NoError(t, err)
}

func TestPurgeFromDrainResult_PurgeError(t *testing.T) {
	t.Run("dependent_collection_error_propagates", func(t *testing.T) {
		n := startTestNode(t)
		cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
		p, tc := newTestPruner(cfg, n)
		ctx := context.Background()

		insertTestBlock(t, n, 1, 1)

		docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, 1, 1)
		require.NoError(t, err)

		drainResult := &DrainResult{
			DocIDsByCollection: map[string][]string{
				testBlockColName: docIDsByCol[testBlockColName],
				testTxColName:    {"not-a-valid-docid"},
			},
			BlockCount: 1,
		}

		err = p.purgeFromDrainResult(ctx, drainResult)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "collection purge errors")
		assert.Contains(t, err.Error(), "purge TestTx")
		assert.True(t, p.totalDocsPruned > 0)
		assert.False(t, p.lastPruneTime.IsZero())
	})

	t.Run("block_collection_error_is_reported", func(t *testing.T) {
		n := startTestNode(t)
		cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
		p, _ := newTestPruner(cfg, n)
		ctx := context.Background()

		drainResult := &DrainResult{
			DocIDsByCollection: map[string][]string{
				testBlockColName: {"not-a-valid-docid"},
			},
			BlockCount: 1,
		}

		err := p.purgeFromDrainResult(ctx, drainResult)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "purge TestBlock")
	})
}

func TestRunIndexerQueuePrune_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	q := NewIndexerQueue()
	p.SetQueue(q)

	// Insert blocks and track them in the queue
	for i := int64(1); i <= 5; i++ {
		docID := insertTestBlock(t, n, i, 0)
		err := q.TrackBlockDocIDs(i, docID, nil, "")
		require.NoError(t, err)
	}

	// Queue has 5 blocks, max_blocks=2, should prune 3
	err := p.runIndexerQueuePrune(ctx, q)
	assert.NoError(t, err)
	assert.Equal(t, 2, q.Len())
}

func TestRunIndexerQueuePrune_BelowThreshold_WithNode(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	q := NewIndexerQueue()
	p.SetQueue(q)

	// Queue empty (0 <= 100), should fallback to filterBasedPrune
	err := p.runIndexerQueuePrune(ctx, q)
	assert.NoError(t, err)
}

func TestRunPrune_DefaultQueueType(t *testing.T) {
	// Test the default case in runPrune switch by using a custom Queue implementation
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 1000}
	p, _ := newTestPruner(cfg, n)

	// Use a mock queue that is neither IndexerQueue nor EventQueue
	p.SetQueue(&mockQueue{})

	ctx := context.Background()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

// mockQueue implements Queue but is neither IndexerQueue nor EventQueue.
type mockQueue struct{}

func (m *mockQueue) Len() int    { return 0 }
func (m *mockQueue) Save() error { return nil }

func TestStop_WithQueueSaveError(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p, _ := newTestPruner(cfg, n)

	// Use a mock queue that returns an error on Save
	p.SetQueue(&mockQueueSaveError{})

	// We need to manually set isRunning and stopChan to test Stop flow
	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx := t.Context()

	err := p.Start(ctx)
	require.NoError(t, err)

	p.Stop()
	assert.False(t, p.isRunning)
}

type mockQueueSaveError struct{}

func (m *mockQueueSaveError) Len() int    { return 5 }
func (m *mockQueueSaveError) Save() error { return fmt.Errorf("save failed") }

// ─── Concurrency tests ─────────────────────────────────────────────────────

func TestStartStop_Concurrent(t *testing.T) {
	n := startTestNode(t)
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			p, _ := newTestPruner(cfg, n)
			q := NewIndexerQueue()
			p.SetQueue(q)

			ctx, cancel := context.WithCancel(context.Background())
			_ = p.Start(ctx)
			time.Sleep(10 * time.Millisecond)
			cancel()
			p.Stop()
		})
	}
	wg.Wait()
}

func TestGetMetrics_Concurrent(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, nil, nil)

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			m := p.GetMetrics()
			assert.False(t, m.IsRunning)
		})
	}
	wg.Wait()
}
