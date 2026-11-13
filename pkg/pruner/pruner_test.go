package pruner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logger.Init(true)
	os.Exit(m.Run())
}

func TestNewPruner(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 1000, IntervalSeconds: 60}

	t.Run("nil chain", func(t *testing.T) {
		p := NewPruner(cfg, nil, nil)
		require.NotNil(t, p)
		assert.Equal(t, "", p.blockCollection)
		assert.Equal(t, "", p.blockSigCollection)
	})

	t.Run("with chain resolves collection names", func(t *testing.T) {
		mock := &testutils.MockConverter{
			CollectionsFn: func() chains.Collections {
				return evm.NewCollectionNames("Test")
			},
		}
		p := NewPruner(cfg, nil, mock)
		require.NotNil(t, p)
		assert.Equal(t, "Test__Block", p.blockCollection)
		assert.Equal(t, "Test__BlockSignature", p.blockSigCollection)
	})
}

func TestPrunerSetQueue(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: true}
	p := NewPruner(cfg, nil, nil)
	assert.Nil(t, p.queue)

	q := NewIndexerQueue()
	p.SetQueue(q)
	assert.Equal(t, q, p.queue)
}

func TestPrunerStart_Disabled(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: false}
	p := NewPruner(cfg, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := p.Start(ctx)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
	cancel()
	p.Stop()
}

func TestPrunerStart_NilNode(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: true}
	p := NewPruner(cfg, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := p.Start(ctx)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
	cancel()
	p.Stop()
}

func TestPrunerGetMetrics(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, nil, nil)

	metrics := p.GetMetrics()
	assert.True(t, metrics.Enabled)
	assert.False(t, metrics.IsRunning)
	assert.Equal(t, int64(0), metrics.TotalBlocksPruned)
	assert.Equal(t, int64(0), metrics.TotalDocsPruned)
}

func TestPrunerStop_NotRunning(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: true}
	p := NewPruner(cfg, nil, nil)

	// Should be a no-op without panicking
	assert.NotPanics(t, func() {
		p.Stop()
	})
}

func TestPrunerStop_WithQueue(t *testing.T) {
	cfg := &config.PrunerConfig{Enabled: false}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	assert.Nil(t, p.queue)
	// runPrune with nil queue calls filterBasedPrune which needs a node
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

func TestRunPrune_WithIndexerQueue(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
			map[string][]string{testTxColName: {
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 1, MaxBlocksPerCycle: 2}
	p := NewPruner(cfg, n, nil)
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, MaxBlocksPerCycle: 2}
	p := NewPruner(cfg, n, nil)
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 1, MaxBlocksPerCycle: 2}
	p := NewPruner(cfg, n, nil)
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 1000, DocsPerBlock: 10, IntervalSeconds: 1}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 1}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 1}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p, _ := newTestPruner(cfg, n)

	tmpDir := t.TempDir()
	q := NewIndexerQueue()
	_, err := q.LoadFromFile(tmpDir + "/queue.gob")
	require.NoError(t, err)

	err = q.TrackBlockDocIDs(1, "bae-550e8400-e29b-41d4-a716-446655440000", nil, "")
	require.NoError(t, err)
	p.SetQueue(q)

	// The entry must not be stale: startup cleanup discards queue entries for
	// blocks the db does not hold, and a resulting removed-on-empty Save would
	// erase the file before the persistence check below.
	insertTestBlock(t, n, 1, 0)

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
		cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 1000}
		p, _ := newTestPruner(cfg, n)
		// No queue set, so runPrune calls filterBasedPrune
		err := p.runPrune(ctx)
		assert.NoError(t, err)
	})

	t.Run("indexer queue dispatch", func(t *testing.T) {
		cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 1000}
		p, _ := newTestPruner(cfg, n)
		q := NewIndexerQueue()
		p.SetQueue(q)
		err := p.runPrune(ctx)
		assert.NoError(t, err)
	})
}

// SetCycleObserver is the seam the replay harness reads cycle facts through:
// every completed cycle reports its deltas and wall time, and the consumer
// owns which cycles count as work.
func TestSetCycleObserver_ReportsCycleFacts(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 5, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	// Retention exceeded (8 blocks, cap 5): the startup cleanup path deletes
	// the 3 lowest blocks, so this cycle reports a real purge.
	for i := int64(1); i <= 8; i++ {
		insertTestBlock(t, n, i, 0)
	}

	var events []CycleEvent
	p.SetCycleObserver(func(ev CycleEvent) { events = append(events, ev) })

	require.NoError(t, p.timedCycle(ctx, p.startupCleanup))
	require.Len(t, events, 1)
	assert.Equal(t, int64(3), events[0].Blocks)
	assert.Equal(t, int64(3), events[0].Docs)
	assert.Greater(t, events[0].Duration, time.Duration(0))

	// Retention now holds (5 blocks remain, cap 5): the check-only cycle
	// still reports, with zero deltas. Filtering is the consumer's policy,
	// not the package's.
	require.NoError(t, p.timedCycle(ctx, p.runPrune))
	require.Len(t, events, 2)
	assert.Equal(t, int64(0), events[1].Blocks)
	assert.Equal(t, int64(0), events[1].Docs)
}

func TestGetBlockRange(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
	mock := &testutils.MockConverter{
		CollectionsFn: func() chains.Collections {
			return evm.NewCollectionNames(evm.DefaultCollectionPrefix)
		},
		GetLowestStoredBlockNumberFn: func(_ context.Context, _ *node.Node) (int64, error) {
			return 0, chains.ErrBlockNumberCorrupt
		},
	}
	p := NewPruner(cfg, nil, mock)
	_, _, err := p.getBlockRange(context.Background())
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoValidBlocks))
}

func TestPurgeByDocIDs(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, tc := newTestPruner(cfg, n)
	ctx := context.Background()

	// Insert blocks
	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))

	// Get docIDs by querying via chain
	docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, n, 1, 1)
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, tc := newTestPruner(cfg, n)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, n, 1, 2)
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: false}
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

// Rows without a number must not stop the pruner reaching the block range and pruning.
func TestFilterBasedPrune_WithRowsWithNoNumber(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	for i := range 10 {
		res := n.DB.ExecRequest(ctx, fmt.Sprintf(`mutation { add_TestBlock(input: [{hash: "no-number-%d"}]) { _docID } }`, i))
		require.Empty(t, res.GQL.Errors, "insert failed: %v", res.GQL.Errors)
	}
	for _, num := range []int64{1, 2, 3, 4} {
		insertTestBlock(t, n, num, 0)
	}

	require.NoError(t, p.filterBasedPrune(ctx))
	assert.True(t, p.totalBlocksPruned > 0)
}

func TestFilterBasedPrune_WithinLimit(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: false}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
	p, _ := newTestPruner(cfg, n)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	err := p.startupCleanup(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))
	assert.Equal(t, int64(0), p.totalBlocksPruned)
}

// The empty-DB discard must run before the loop goroutine: pruneLoop races the
// indexer's first block stores, so an in-loop empty-DB check could drop a
// freshly tracked live entry. Nothing is tracked before Start returns, so the
// synchronous discard there is race-free, and its persistence is observable
// right away: a Save with no entries removes the file entirely.
func TestStart_DiscardsStaleQueueWhenDBEmpty(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	seedQueueFile(t, path, 1, 2)
	q := NewIndexerQueue()
	count, err := q.LoadFromFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false, IntervalSeconds: 3600}
	p, _ := newTestPruner(cfg, n)
	p.SetQueue(q)

	// The discard is complete the moment Start returns.
	require.NoError(t, p.Start(context.Background()))
	t.Cleanup(p.Stop)

	assert.Equal(t, 0, q.Len())
	require.NoFileExists(t, path)

	// A later restart cannot load the stale entries back.
	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 0, persisted)
}

// When the DB holds no visible block, queue entries can still name
// soft-deleted reorg orphans: the range query only returns default-visible
// docs, so dropping the entries would strand the orphans forever. The empty-DB
// discard must hard-purge what it drops, and the purge stays uncounted.
func TestDiscardStaleQueueEntries_PurgesSoftDeletedOrphansOnEmptyDB(t *testing.T) {
	n := startTestNode(t)
	ctx := context.Background()
	p, _ := newTestPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: true}, n)

	orphanBlock := insertTestBlock(t, n, 1, 0)
	orphanTx := insertTestTx(t, n, 1, 0)
	softDeleteDocs(t, n, testBlockColName, []string{orphanBlock})
	softDeleteDocs(t, n, testTxColName, []string{orphanTx})

	// Precondition mirroring the rollback: byte-identical content is refused
	// while the orphan docs are soft-deleted.
	errText, refused := reAddBlockDoc(t, n, 1)
	require.True(t, refused, "the soft-deleted content-derived docID must be refused before the discard")
	assert.Contains(t, strings.ToLower(errText), "deleted")

	q := NewIndexerQueue()
	require.NoError(t, q.TrackBlockDocIDs(1, orphanBlock, map[string][]string{testTxColName: {orphanTx}}, ""))
	p.SetQueue(q)

	require.NoError(t, p.discardStaleQueueEntries(ctx))

	assert.Equal(t, 0, q.Len())
	errText, refused = reAddBlockDoc(t, n, 1)
	require.False(t, refused, "after the discard's uncounted purge the identical content must store again, got: %s", errText)
	assert.Equal(t, int64(0), p.totalBlocksPruned, "the empty-DB purge does not count")
	assert.Equal(t, int64(0), p.totalDocsPruned, "the empty-DB purge does not count")
}

// Since the empty-DB discard moved to Start, the loop-side cleanup must not
// drop entries when the DB appears empty: its getBlockRange can race the
// indexer's first block stores, and a block stored between the query and a
// discard would be dropped while logged as stale. Stale-entry handling for an
// empty DB belongs to Start's synchronous discard, where nothing is tracked
// yet and the DB state cannot have moved.
func TestStartupCleanup_DoesNotDiscardWhenDBEmpty(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)

	q := NewIndexerQueue()
	for i := int64(1); i <= 2; i++ {
		require.NoError(t, q.TrackBlockDocIDs(i, docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000", nil, ""))
	}
	p.SetQueue(q)

	require.NoError(t, p.startupCleanup(context.Background()))

	assert.Equal(t, 2, q.Len())
}

// The within-limit early return is the path that would strand stale entries:
// the db holds 3-5 (under retention) while the queue file predates the last
// purge of blocks 1 and 2, so the next Drain would purge and count those
// already-deleted blocks again.
func TestStartupCleanup_DiscardsStaleQueueEntriesBelowDBMinimum(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	for i := int64(3); i <= 5; i++ {
		insertTestBlock(t, n, i, 0)
	}
	seedQueueFile(t, path, 1, 4)
	p, q := loadPrunerWithQueue(t, n, path, 100, 4)

	require.NoError(t, p.startupCleanup(context.Background()))

	// Entries at or below the db's lowest block are gone; live entries survive.
	assert.Equal(t, 2, q.Len())
	for _, entry := range q.entries {
		assert.Greater(t, entry.BlockNumber, int64(2))
	}

	// The cleanup is persisted, so a later restart cannot load the stale entries back.
	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 2, persisted)
}

// The over-retention startup prune deletes blocks 1-3 and pruneBlockRange
// discards the matching queue entries, but startupCleanup has no deferred
// checkpoint the way the tick path does: the discard must reach the file here,
// or a kill before the first tick leaves the file holding entries for blocks
// that were just deleted.
func TestStartupCleanup_PersistsQueueAfterPrune(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	for i := int64(1); i <= 5; i++ {
		insertTestBlock(t, n, i, 0)
	}
	seedQueueFile(t, path, 1, 5)
	p, q := loadPrunerWithQueue(t, n, path, 2, 5)

	require.NoError(t, p.startupCleanup(context.Background()))

	assert.Equal(t, 2, q.Len())

	// Blocks 4-5, the ones above the cutoff, remain in the db.
	lowest, highest, err := p.getBlockRange(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(4), lowest)
	assert.Equal(t, int64(5), highest)

	// The discard survives a restart: the file holds only the live entries.
	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 2, persisted)
	for _, entry := range reloaded.entries {
		assert.Greater(t, entry.BlockNumber, int64(3))
	}
}

func TestPurgeFromDrainResult(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p, tc := newTestPruner(cfg, n)
	ctx := context.Background()

	// Insert blocks and txs
	insertTestBlock(t, n, 1, 1)
	insertTestBlock(t, n, 2, 0)

	// Get docIDs via chain
	docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, n, 1, 1)
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
		cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
		p, tc := newTestPruner(cfg, n)
		ctx := context.Background()

		insertTestBlock(t, n, 1, 1)

		docIDsByCol, err := tc.GetDocIDsByBlockRange(ctx, n, 1, 1)
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
		cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}
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

// Regression test for the strategy race between the queue read and the DB
// measurement: the queue sits at retention when the cycle picks the filter
// path, but the DB has grown past it, so the filter path deletes a block
// while its queue entry survives. A later Drain would purge that stale entry
// a second time and double count the block.
func TestPrunerDoesNotDoubleCountRangePrunedBlocks(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 20, PruneHistory: false}
	p, _ := newTestPruner(cfg, n)
	q := NewIndexerQueue()
	p.SetQueue(q)
	ctx := context.Background()

	// DB holds blocks 1..21 (one over retention) while the queue tracks
	// exactly 1..20: the queue read picks the filter path.
	docIDs := make(map[int64]string)
	for i := int64(1); i <= 21; i++ {
		docIDs[i] = insertTestBlock(t, n, i, 0)
	}
	for i := int64(1); i <= 20; i++ {
		err := q.TrackBlockDocIDs(i, docIDs[i], nil, "")
		require.NoError(t, err)
	}
	require.Equal(t, 20, q.Len())

	// The filter path prunes block 1 by DB range and must discard its queue
	// entry, even though the queue length read chose the filter path.
	require.NoError(t, p.runPrune(ctx))
	assert.Equal(t, int64(1), p.totalBlocksPruned)

	// New blocks arrive: the DB is again one over retention and the queue
	// holds one more entry than a Drain needs to remove.
	docIDs[22] = insertTestBlock(t, n, 22, 0)
	for _, i := range []int64{21, 22} {
		err := q.TrackBlockDocIDs(i, docIDs[i], nil, "")
		require.NoError(t, err)
	}
	require.Equal(t, 21, q.Len())

	// This cycle drains only the live block-2 entry. Without the discard,
	// the stale block-1 entry is drained too, purged (the doc no longer
	// exists) and counted again, pushing the counter to 3.
	require.NoError(t, p.runPrune(ctx))

	// The counter equals the distinct deleted blocks: 1 by range plus 2 by
	// drain. No block was counted under both strategies.
	assert.Equal(t, int64(2), p.totalBlocksPruned)

	// The queue holds no entry at or below the old range-prune cutoff.
	for _, entry := range q.entries {
		assert.Greater(t, entry.BlockNumber, int64(1), "stale entry for block %d", entry.BlockNumber)
	}
}

// brokenRangeChain forces the range purge to fail: GetDocIDsByBlockRange
// returns an invalid docID, so purgeByDocIDs skips it and reports an error
// without deleting anything.
type brokenRangeChain struct {
	*testChain
}

func (b brokenRangeChain) GetDocIDsByBlockRange(_ context.Context, _ *node.Node, _, _ int64) (map[string][]string, error) {
	return map[string][]string{testBlockColName: {"not-a-valid-docid"}}, nil
}

// flakyLowestChain fails the first block-range query the way a transient
// start-time error would and serves the real chain afterwards. The call
// counter pins that the loop actually retries instead of giving up.
type flakyLowestChain struct {
	*testChain

	failing bool
	calls   int
}

func (c *flakyLowestChain) GetLowestStoredBlockNumber(ctx context.Context, n *node.Node) (int64, error) {
	c.calls++
	if c.failing {
		c.failing = false
		return 0, fmt.Errorf("transient range query failure")
	}
	return c.testChain.GetLowestStoredBlockNumber(ctx, n)
}

// A purge dying between collections deletes collections in sorted-name
// order, so the block docs go first and their live txs are left behind. On
// restart the DB's lowest block sits above those leftovers, so a range pass
// can never reach them again; the entries below the lowest block are the
// only record of their docIDs and the within-limit path must purge, not
// just drop them.
func TestStartupCleanup_PurgesLeftoverDocsBelowDBMinimum(t *testing.T) {
	n := startTestNode(t)
	ctx := context.Background()
	p, _ := newTestPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}, n)

	// Crash leftovers: the block docs at 1-2 are gone, their txs stayed live.
	leftoverTx1 := insertTestTx(t, n, 1, 0)
	leftoverTx2 := insertTestTx(t, n, 2, 0)
	insertTestBlock(t, n, 3, 1)

	seedDocID := docIDPrefix + "-550e8400-e29b-41d4-a716-446655440000"
	q := NewIndexerQueue()
	require.NoError(t, q.TrackBlockDocIDs(1, seedDocID, map[string][]string{testTxColName: {leftoverTx1}}, ""))
	require.NoError(t, q.TrackBlockDocIDs(2, seedDocID, map[string][]string{testTxColName: {leftoverTx2}}, ""))
	p.SetQueue(q)

	require.NoError(t, p.startupCleanup(ctx))

	assert.Equal(t, 0, q.Len(), "entries below the DB's minimum must be drained")
	assert.Equal(t, 1, countDocs(t, n, testTxColName),
		"the live block's tx must survive and both crash-leftover txs must be purged")
	assert.Equal(t, int64(0), p.totalBlocksPruned, "the within-limit drain purges without counting")
	assert.Equal(t, int64(0), p.totalDocsPruned, "the within-limit drain purges without counting")
}

// The rollback soft-deletes an orphan and keeps its queue entry as the only
// record of its docIDs; range queries only return default-visible docs, so
// once retention moves past the orphan height, dropping the entry without
// purging would strand the soft-deleted docs forever. The drain at or below
// the range cutoff must hard-purge them.
func TestStartupCleanup_PurgesSoftDeletedOrphansOfDiscardedEntries(t *testing.T) {
	n := startTestNode(t)
	ctx := context.Background()
	p, _ := newTestPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: true}, n)

	orphanBlock := insertTestBlock(t, n, 1, 0)
	orphanTx := insertTestTx(t, n, 1, 0)
	softDeleteDocs(t, n, testBlockColName, []string{orphanBlock})
	softDeleteDocs(t, n, testTxColName, []string{orphanTx})

	// Precondition mirroring the rollback: byte-identical content is refused
	// while the orphan docs are soft-deleted.
	errText, refused := reAddBlockDoc(t, n, 1)
	require.True(t, refused, "the soft-deleted content-derived docID must be refused before the cleanup")
	assert.Contains(t, strings.ToLower(errText), "deleted")

	// The DB holds blocks 2-5, so retention keeps only the two newest and
	// startupCleanup ranges over [2, 3], discarding entries at or below 3.
	docIDs := make(map[int64]string, 4)
	for i := int64(2); i <= 5; i++ {
		docIDs[i] = insertTestBlock(t, n, i, 0)
	}

	q := NewIndexerQueue()
	require.NoError(t, q.TrackBlockDocIDs(1, orphanBlock, map[string][]string{testTxColName: {orphanTx}}, ""))
	for i := int64(2); i <= 5; i++ {
		require.NoError(t, q.TrackBlockDocIDs(i, docIDs[i], nil, ""))
	}
	p.SetQueue(q)

	require.NoError(t, p.startupCleanup(ctx))

	// The soft-deleted orphan docs are reclaimed: identical content stores again.
	errText, refused = reAddBlockDoc(t, n, 1)
	require.False(t, refused, "after the discarded entry's uncounted purge the identical content must store again, got: %s", errText)

	assert.Equal(t, int64(2), p.totalBlocksPruned, "only the range-counted blocks 2-3 are counted")
	assert.Equal(t, int64(2), p.totalDocsPruned, "the orphan's docs are purged without counting")
	assert.Equal(t, 2, q.Len(), "only the entries above the cutoff survive")
	for _, entry := range q.entries {
		assert.Greater(t, entry.BlockNumber, int64(3), "entries above the cutoff survive")
	}
}

// A joined purge error means at least one collection may still hold live
// documents, so the entries must survive for a later pass or Drain, which
// purges the documents and counts each block exactly once.
func TestPruneBlockRange_KeepsEntriesOnPurgeFailure(t *testing.T) {
	n := startTestNode(t)
	p := NewPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 1, PruneHistory: false}, n, brokenRangeChain{testChain: newTestChain(n)})
	p.blockCollection = testBlockColName

	docIDs := make(map[int64]string, 3)
	for i := int64(1); i <= 3; i++ {
		docIDs[i] = insertTestBlock(t, n, i, 0)
	}
	q := NewIndexerQueue()
	for i := int64(1); i <= 3; i++ {
		require.NoError(t, q.TrackBlockDocIDs(i, docIDs[i], nil, ""))
	}
	p.SetQueue(q)

	totalPurged, err := p.pruneBlockRange(context.Background(), 1, 2)
	require.Error(t, err, "the invalid range docID must fail the purge")
	assert.Equal(t, int64(0), totalPurged)
	assert.Equal(t, 3, q.Len(), "a failed purge keeps the entries")
	assert.Equal(t, 3, countDocs(t, n, testBlockColName), "nothing may be deleted when the only range docID is invalid")
}

// An over-retention startup prune that fails must still drain the entries
// at or below the DB's lowest block: those blocks predate the DB's floor and
// were deleted by an earlier run, so keeping their entries would let the
// next Drain purge and count blocks that were never live. The entries inside
// the failed range stay kept, since their documents may still be live.
func TestStartupCleanup_OverRetentionPruneFailure_DrainsBelowDBMinimum(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	// A live leftover tx at block 2, whose block doc is already gone.
	leftoverTx := insertTestTx(t, n, 2, 0)
	seedDocID := docIDPrefix + "-550e8400-e29b-41d4-a716-446655440000"
	q := NewIndexerQueue()
	_, err := q.LoadFromFile(path)
	require.NoError(t, err)
	require.NoError(t, q.TrackBlockDocIDs(1, seedDocID, nil, ""))
	require.NoError(t, q.TrackBlockDocIDs(2, seedDocID, map[string][]string{testTxColName: {leftoverTx}}, ""))
	require.NoError(t, q.TrackBlockDocIDs(3, seedDocID, nil, ""))
	// Blocks 4-8 go in after the stale entries are tracked: block 4 is then
	// the DB's floor, which is what the expected failure branch must use.
	docIDs := make(map[int64]string, 5)
	for i := int64(4); i <= 8; i++ {
		docIDs[i] = insertTestBlock(t, n, i, 0)
	}
	for i := int64(4); i <= 8; i++ {
		require.NoError(t, q.TrackBlockDocIDs(i, docIDs[i], nil, ""))
	}
	p := NewPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: false}, n, brokenRangeChain{testChain: newTestChain(n)})
	p.blockCollection = testBlockColName
	p.SetQueue(q)

	// The over-retention prune of 4-6 fails on the invalid range docID, so
	// the entries 4-8 stay kept and the error branch drains 1-3.
	err = p.startupCleanup(context.Background())
	require.Error(t, err)

	assert.Equal(t, 5, q.Len())
	for _, entry := range q.entries {
		assert.GreaterOrEqual(t, entry.BlockNumber, int64(4))
	}
	assert.Equal(t, 0, countDocs(t, n, testTxColName), "the live leftover below the DB floor is purged uncounted")
	assert.Equal(t, 5, countDocs(t, n, testBlockColName), "the failed prune deletes nothing")

	// The counters stay zero: the failed prune counted nothing, and the
	// below-floor purge is uncounted by design.
	assert.Equal(t, int64(0), p.totalBlocksPruned)
	assert.Equal(t, int64(0), p.totalDocsPruned)

	// The discard reaches the file, so a later restart cannot load the
	// stale entries back.
	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 5, persisted)

	// The next tick with q.Len() > max_blocks drains entries 4-6. Only
	// those blocks count: the stale 1-3 were already drained by the error
	// branch, so a block a previous run deleted can no longer be counted
	// again through this restart path.
	require.NoError(t, p.runIndexerQueuePrune(context.Background(), q))
	assert.Equal(t, int64(3), p.totalBlocksPruned, "only the live drained blocks count, not the stale below-floor ones")
	assert.Equal(t, 2, q.Len())
}

// A failed startup cleanup must change nothing by itself: no entries drained,
// nothing counted, and the queue file left exactly as the restart loaded it.
// This is the state a transient range query failure at startup leaves behind,
// which loopCycle's retry must recover from before any counted drain runs.
func TestStartupCleanup_QueryFailure_KeepsEverythingAndCountsNothing(t *testing.T) {
	n := startTestNode(t)
	ctx := context.Background()
	path := t.TempDir() + "/prune_queue.gob"

	docIDs := make(map[int64]string, 5)
	for i := int64(1); i <= 5; i++ {
		docIDs[i] = insertTestBlock(t, n, i, 0)
	}
	seedQueueFile(t, path, 1, 5)
	p, q := loadPrunerWithQueue(t, n, path, 2, 5)
	flaky := &flakyLowestChain{testChain: newTestChain(n), failing: true}
	p.converter = flaky

	err := p.startupCleanup(ctx)
	require.Error(t, err)

	assert.Equal(t, 5, q.Len(), "a failed startup cleanup must not drain anything")
	assert.Equal(t, int64(0), p.totalBlocksPruned)
	assert.Equal(t, int64(0), p.totalDocsPruned)
	assert.Equal(t, 5, countDocs(t, n, testBlockColName), "nothing may be deleted when the block range cannot be read")

	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 5, persisted, "the queue file must be untouched by the failed cleanup")
}

// The startup cleanup runs before any counted drain and is retried until its
// first success. Its failure keeps the entries at or below the DB's floor in
// place; without the retry, the oldest-first queue drain would purge and
// count blocks that an earlier run already deleted and counted.
func TestPrunerLoopCycle_RetriesStartupCleanupUntilSuccess(t *testing.T) {
	n := startTestNode(t)
	ctx := context.Background()
	path := t.TempDir() + "/prune_queue.gob"

	// Blocks 4-8 are live; the queue still tracks 1-3 as stale below-floor
	// entries (1-3 were range-pruned by an earlier run), with a live
	// leftover tx at block 2 recording only through its entry. The real
	// docIDs go through the queue file, the way a previous run's checkpoint
	// would have.
	leftoverTx := insertTestTx(t, n, 2, 0)
	docIDs := make(map[int64]string, 5)
	for i := int64(4); i <= 8; i++ {
		docIDs[i] = insertTestBlock(t, n, i, 0)
	}
	seedDocID := docIDPrefix + "-550e8400-e29b-41d4-a716-446655440000"
	written := NewIndexerQueue()
	_, err := written.LoadFromFile(path)
	require.NoError(t, err)
	require.NoError(t, written.TrackBlockDocIDs(1, seedDocID, nil, ""))
	require.NoError(t, written.TrackBlockDocIDs(2, seedDocID, map[string][]string{testTxColName: {leftoverTx}}, ""))
	require.NoError(t, written.TrackBlockDocIDs(3, seedDocID, nil, ""))
	for i := int64(4); i <= 8; i++ {
		require.NoError(t, written.TrackBlockDocIDs(i, docIDs[i], nil, ""))
	}
	require.NoError(t, written.Save())
	p, q := loadPrunerWithQueue(t, n, path, 2, 8)
	flaky := &flakyLowestChain{testChain: newTestChain(n), failing: true}
	p.converter = flaky

	// First cycle: the startup cleanup fails on the range query, so nothing
	// is drained and nothing is counted.
	err = p.loopCycle(ctx)
	require.Error(t, err)
	assert.Equal(t, 8, q.Len())
	assert.Equal(t, int64(0), p.totalBlocksPruned)
	assert.False(t, p.startupCleanupDone)

	// Second cycle: the startup cleanup is retried and succeeds. The
	// over-retention range prune of 4-6 counts those three blocks once, and
	// the success drain drops every entry at or below the cutoff, stale
	// ones included, purging their docIDs uncounted.
	err = p.loopCycle(ctx)
	require.NoError(t, err)
	assert.True(t, p.startupCleanupDone)
	assert.Equal(t, int64(3), p.totalBlocksPruned, "only the live range-pruned blocks 4-6 counted")
	assert.Equal(t, 0, countDocs(t, n, testTxColName), "the live leftover below the DB floor is purged uncounted")
	assert.Equal(t, 2, q.Len(), "only entries above the cutoff survive")

	// The recovered state reached the file, so a restart cannot load the
	// stale entries back.
	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 2, persisted)

	// Third cycle: startup cleanup is done, so the regular prune runs and
	// the stale 1-3 can no longer be counted through any path.
	err = p.loopCycle(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), p.totalBlocksPruned, "no block may be counted through the post-recovery cycles")
	assert.Equal(t, 2, q.Len())
	assert.GreaterOrEqual(t, flaky.calls, 2, "the failed startup must be retried, not dropped")
}

// A cancelled context must leave the queue untouched: the purge would fail
// for the same reason, and dropping the entries on the way out would erase
// the only record of docIDs, including soft-deleted reorg orphans whose
// absence blocks that height from ever being re-indexed. The next run
// retries them uncounted.
func TestDiscardAndPurge_SkipsOnCancelledContext(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	seedQueueFile(t, path, 1, 2)
	p, q := loadPrunerWithQueue(t, n, path, 100, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.discardAndPurge(ctx, q, 1, "(test, cancelled)")

	assert.Equal(t, 2, q.Len(), "a cancelled run must not drain entries")
	assert.Equal(t, 0, q.PendingCount())
	assert.Equal(t, int64(0), p.totalBlocksPruned)

	reloaded := NewIndexerQueue()
	persisted, err := reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 2, persisted, "the queue file must be untouched by a cancelled run")
}

// When the uncounted purge of dropped entries fails, the docIDs are parked on
// the queue and persisted: the entries are already gone, so the parked list
// becomes the only record of what may still need purging.
func TestDiscardAndPurge_ParksOnFailedPurge(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	// A valid docID under a collection name that does not exist fails the
	// purge without touching anything purgeable.
	parkedTx := docIDPrefix + "-660e8400-e29b-41d4-a716-446655440001"
	p, _ := newTestPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 100, PruneHistory: false}, n)
	q := NewIndexerQueue()
	_, err := q.LoadFromFile(path)
	require.NoError(t, err)
	require.NoError(t, q.TrackBlockDocIDs(1,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440000",
		map[string][]string{"BogusCol": {parkedTx}},
		""))

	p.discardAndPurge(context.Background(), q, 1, "(test, failing purge)")

	assert.Equal(t, 0, q.Len(), "the failed purge drops the entries either way")
	assert.Equal(t, 2, q.PendingCount(), "the docIDs must be parked, not lost")
	pending := q.TakePendingDocIDs()
	assert.Equal(t, []string{docIDPrefix + "-550e8400-e29b-41d4-a716-446655440000"}, pending[testBlockColName])
	assert.Equal(t, []string{parkedTx}, pending["BogusCol"])
	q.ParkDocIDs(pending)
	assert.Equal(t, int64(0), p.totalBlocksPruned, "the uncounted purge must not count")
	assert.Equal(t, int64(0), p.totalDocsPruned)

	// The parked list must survive a restart like the entries do.
	reloaded := NewIndexerQueue()
	_, err = reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 2, reloaded.PendingCount(), "the parked docIDs must be persisted")
}

// Soft-deleted reorg orphans whose purge failed must be recoverable through
// the parked list: the retry hard-purges them, which is what lifts the
// indexer's refusal to re-store identical content at that height.
func TestRetryPendingPurges_PurgesOrphansOfDiscardedEntries(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	orphanBlock := insertTestBlock(t, n, 1, 0)
	orphanTx := insertTestTx(t, n, 1, 0)
	softDeleteDocs(t, n, testBlockColName, []string{orphanBlock})
	softDeleteDocs(t, n, testTxColName, []string{orphanTx})

	errText, refused := reAddBlockDoc(t, n, 1)
	require.True(t, refused, "the soft-deleted content-derived docID must be refused before the retry")
	assert.Contains(t, strings.ToLower(errText), "deleted")

	// The parked list is what a failed purge left behind: the entries are
	// gone and the docIDs persist only here. The queue file is checkpointed
	// with it the same way discardAndPurge's save does.
	seedQueueFile(t, path, 5, 6)
	p := NewPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: true}, n, newTestChain(n))
	p.blockCollection = testBlockColName
	q := NewIndexerQueue()
	_, err := q.LoadFromFile(path)
	require.NoError(t, err)
	p.SetQueue(q)
	q.ParkDocIDs(map[string][]string{
		testBlockColName: {orphanBlock},
		testTxColName:    {orphanTx},
	})
	require.NoError(t, q.Save())

	p.retryPendingPurges(context.Background(), q)

	_, refused = reAddBlockDoc(t, n, 1)
	require.False(t, refused, "the uncounted retry must hard-purge the soft-deleted orphans")
	assert.Equal(t, 0, q.PendingCount(), "a successful retry empties the parked list")
	assert.Equal(t, int64(0), p.totalBlocksPruned, "the retry must not count")
	assert.Equal(t, int64(0), p.totalDocsPruned)

	reloaded := NewIndexerQueue()
	_, err = reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 0, reloaded.PendingCount(), "the empty parked list must be persisted")
}

// A counted drain whose purge fails counts its blocks once but must not
// erase the docID record: parking them keeps the stranding window to the
// failed purge instead of losing the documents forever, and the uncycled
// retry must not count a second time.
func TestRunIndexerQueuePrune_ParksOnPurgeFailure(t *testing.T) {
	n := startTestNode(t)
	path := t.TempDir() + "/prune_queue.gob"

	// The queue tracks blocks 1-3, with entry 2 carrying a docID under a
	// collection that does not exist, so the drained purge fails after the
	// block docs may already have been purged.
	block2Tx := docIDPrefix + "-660e8400-e29b-41d4-a716-446655440012"
	p, q := loadPrunerWithQueue(t, n, path, 1, 0)
	require.NoError(t, q.TrackBlockDocIDs(1,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440001", nil, ""))
	require.NoError(t, q.TrackBlockDocIDs(2,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440002",
		map[string][]string{"BogusCol": {block2Tx}},
		""))
	require.NoError(t, q.TrackBlockDocIDs(3,
		docIDPrefix+"-550e8400-e29b-41d4-a716-446655440003", nil, ""))

	err := p.runIndexerQueuePrune(context.Background(), q)
	require.Error(t, err, "the bogus-collection purge must fail the cycle")

	assert.Equal(t, 1, q.Len(), "only the newest entry survives the drain")
	assert.Equal(t, int64(2), p.totalBlocksPruned, "the drained blocks were counted exactly once")
	assert.Equal(t, 3, q.PendingCount(), "the failed purge's docIDs must be parked")
	assert.Equal(t, int64(2), p.totalDocsPruned, "the count reflects the requested docIDs, not the failed collection")

	// The deferred checkpoint persisted both the drained removal and the
	// parked list.
	reloaded := NewIndexerQueue()
	_, err = reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, reloaded.Len())
	assert.Equal(t, 3, reloaded.PendingCount())

	// The next cycle retries the parked docIDs uncounted, fails again on the
	// same collection and parks them back, without counting anything twice.
	err = p.runIndexerQueuePrune(context.Background(), q)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), p.totalBlocksPruned, "the retry must not count")
	assert.Equal(t, int64(2), p.totalDocsPruned)
	assert.Equal(t, 3, q.PendingCount(), "a failed retry keeps the docIDs parked")

	reloaded = NewIndexerQueue()
	_, err = reloaded.LoadFromFile(path)
	require.NoError(t, err)
	assert.Equal(t, 3, reloaded.PendingCount(), "the re-parked docIDs must reach the file")
}

func TestRunIndexerQueuePrune_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: false}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 1000}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}

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
	cfg := &config.PrunerConfig{Enabled: true, MaxBlocks: 100}
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
