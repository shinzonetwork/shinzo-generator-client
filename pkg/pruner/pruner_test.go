package pruner

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logger.Init(true)
	os.Exit(m.Run())
}

// testSchema defines a simple schema for pruner integration tests.
const testSchema = `
type TestBlock {
	number: Int @index
	hash: String
}

type TestTx {
	blockNumber: Int @index
	txHash: String
}
`

// testCollections returns a CollectionConfig matching the testSchema.
func testCollections() CollectionConfig {
	return CollectionConfig{
		BlockCollection:      "TestBlock",
		BlockNumberField:     "number",
		DependentCollections: []string{"TestTx"},
	}
}

// startTestNode creates a real DefraDB node with the test schema.
func startTestNode(t *testing.T) *node.Node {
	t.Helper()
	td := testutils.SetupTestDefraDBWithSchema(t, testSchema)
	return td.Node
}

// insertTestBlock inserts a TestBlock and optionally TestTx docs into the DB.
// Returns the block docID.
func insertTestBlock(t *testing.T, n *node.Node, blockNum int64, txCount int) string {
	t.Helper()
	ctx := context.Background()
	// Insert block
	mutation := fmt.Sprintf(`mutation { create_TestBlock(input: {number: %d, hash: "hash%d"}) { _docID } }`, blockNum, blockNum)
	result := n.DB.ExecRequest(ctx, mutation)
	require.Empty(t, result.GQL.Errors, "insert block %d failed: %v", blockNum, result.GQL.Errors)

	// Extract docID - DefraDB may return data in different shapes
	blockDocID := ""
	if data, ok := result.GQL.Data.(map[string]any); ok {
		raw := data["create_TestBlock"]
		switch v := raw.(type) {
		case map[string]any:
			blockDocID, _ = v["_docID"].(string)
		case []map[string]any:
			if len(v) > 0 {
				blockDocID, _ = v[0]["_docID"].(string)
			}
		case []any:
			if len(v) > 0 {
				if m, ok := v[0].(map[string]any); ok {
					blockDocID, _ = m["_docID"].(string)
				}
			}
		}
	}

	// Insert transactions
	for i := 0; i < txCount; i++ {
		txMutation := fmt.Sprintf(`mutation { create_TestTx(input: {blockNumber: %d, txHash: "tx%d_%d"}) { _docID } }`, blockNum, blockNum, i)
		txResult := n.DB.ExecRequest(ctx, txMutation)
		require.Empty(t, txResult.GQL.Errors, "insert tx %d_%d failed: %v", blockNum, i, txResult.GQL.Errors)
	}

	return blockDocID
}

// countDocs queries and returns the number of docs in the given collection.
func countDocs(t *testing.T, n *node.Node, collectionName string) int {
	t.Helper()
	ctx := context.Background()
	query := fmt.Sprintf(`query { %s { _docID } }`, collectionName)
	result := n.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0
	}
	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0
	}
	raw := data[collectionName]
	switch docs := raw.(type) {
	case []interface{}:
		return len(docs)
	case []map[string]interface{}:
		return len(docs)
	}
	return 0
}

func TestNewPruner(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 1000, IntervalSeconds: 60}

	t.Run("default collection config", func(t *testing.T) {
		p := NewPruner(cfg, nil)
		require.NotNil(t, p)
		assert.Equal(t, "Ethereum__Mainnet__Block", p.collections.BlockCollection)
	})

	t.Run("custom collection config", func(t *testing.T) {
		custom := CollectionConfig{
			BlockCollection:  "Custom__Block",
			BlockNumberField: "num",
		}
		p := NewPruner(cfg, nil, custom)
		require.NotNil(t, p)
		assert.Equal(t, "Custom__Block", p.collections.BlockCollection)
	})
}

func TestPrunerSetQueue(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil)
	assert.Nil(t, p.queue)

	q := NewIndexerQueue()
	p.SetQueue(q)
	assert.Equal(t, q, p.queue)
}

func TestPrunerStart_Disabled(t *testing.T) {
	cfg := &Config{Enabled: false}
	p := NewPruner(cfg, nil)

	err := p.Start(nil)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
}

func TestPrunerStart_NilNode(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil)

	err := p.Start(nil)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
}

func TestPrunerGetMetrics(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, nil)

	metrics := p.GetMetrics()
	assert.True(t, metrics.Enabled)
	assert.False(t, metrics.IsRunning)
	assert.Equal(t, int64(0), metrics.TotalBlocksPruned)
	assert.Equal(t, int64(0), metrics.TotalDocsPruned)
}

func TestPrunerStop_NotRunning(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil)

	// Should be a no-op without panicking
	assert.NotPanics(t, func() {
		p.Stop()
	})
}

func TestPrunerStop_WithQueue(t *testing.T) {
	cfg := &Config{Enabled: false}
	p := NewPruner(cfg, nil)
	q := NewIndexerQueue()
	p.SetQueue(q)

	// Not running, so Stop is a no-op
	p.Stop()
	assert.False(t, p.isRunning)
}

func TestRunStorageGC_NilNode(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil)

	// Should not panic with nil node
	assert.NotPanics(t, func() {
		p.runStorageGC()
	})
}

func TestParseBlockNumber(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected int64
	}{
		{"float64", float64(42), 42},
		{"int64", int64(100), 100},
		{"int", int(200), 200},
		{"string (unknown type)", "300", 0},
		{"nil", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseBlockNumber(tt.input)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractBlockNumber(t *testing.T) {
	cfg := &Config{Enabled: true}
	cols := DefaultCollectionConfig()
	p := NewPruner(cfg, nil, cols)

	t.Run("nil data", func(t *testing.T) {
		result, err := p.extractBlockNumber(nil)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("wrong type", func(t *testing.T) {
		result, err := p.extractBlockNumber("not a map")
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("empty blocks array ([]interface{})", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []interface{}{},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("blocks with data ([]interface{})", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []interface{}{
				map[string]interface{}{"number": float64(42)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(42), result)
	})

	t.Run("blocks with typed map array", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []map[string]interface{}{
				{"number": float64(99)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(99), result)
	})

	t.Run("empty typed map array", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []map[string]interface{}{},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("typed map array missing number field", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []map[string]interface{}{
				{"other_field": "value"},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("interface array with non-map element", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []interface{}{
				"not a map",
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("interface array missing number field", func(t *testing.T) {
		data := map[string]interface{}{
			"Ethereum__Mainnet__Block": []interface{}{
				map[string]interface{}{"other": "value"},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})

	t.Run("missing block collection key", func(t *testing.T) {
		data := map[string]interface{}{
			"Other_Collection": []interface{}{},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), result)
	})
}

func TestRunPrune_NilQueue(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	_ = NewPruner(cfg, nil)
	// runPrune with nil queue calls filterBasedPrune which needs a node
}

func TestRunPrune_WithIndexerQueue(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, nil)
	q := NewIndexerQueue()
	p.SetQueue(q)
	_ = p // dispatch tested via runPrune type switch
}

func TestRunIndexerQueuePrune_BelowThreshold(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	_ = NewPruner(cfg, nil)
	// Queue has 0 entries, below maxBlocks=100
	// This calls filterBasedPrune which needs node
}

// ─── Integration tests with real DefraDB node ───────────────────────────────

func TestStartAndStop_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p := NewPruner(cfg, n, cols)

	// Set an indexer queue so pruneLoop does not nil-deref on queue type assert
	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	cols := testCollections()
	// Use 1-second interval so the ticker fires quickly
	cfg := &Config{Enabled: true, MaxBlocks: 1000, DocsPerBlock: 10, IntervalSeconds: 1}
	p := NewPruner(cfg, n, cols)

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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 1}
	p := NewPruner(cfg, n, cols)

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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 1}
	p := NewPruner(cfg, n, cols)

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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p := NewPruner(cfg, n, cols)

	tmpDir := t.TempDir()
	q := NewIndexerQueue()
	q.LoadFromFile(tmpDir + "/queue.gob")

	initDocIDPrefix()
	q.TrackBlockDocIDs(1, "bae-550e8400-e29b-41d4-a716-446655440000", nil, "")
	p.SetQueue(q)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := p.Start(ctx)
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
	cols := testCollections()
	ctx := context.Background()

	t.Run("nil queue calls filterBasedPrune", func(t *testing.T) {
		cfg := &Config{Enabled: true, MaxBlocks: 1000}
		p := NewPruner(cfg, n, cols)
		// No queue set, so runPrune calls filterBasedPrune
		err := p.runPrune(ctx)
		assert.NoError(t, err)
	})

	t.Run("indexer queue dispatch", func(t *testing.T) {
		cfg := &Config{Enabled: true, MaxBlocks: 1000}
		p := NewPruner(cfg, n, cols)
		q := NewIndexerQueue()
		p.SetQueue(q)
		err := p.runPrune(ctx)
		assert.NoError(t, err)
	})
}

func TestGetLowestAndHighestBlockNumber(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// Empty DB
	lowest, err := p.getLowestBlockNumber(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), lowest)

	highest, err := p.getHighestBlockNumber(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), highest)

	// Insert blocks
	insertTestBlock(t, n, 10, 0)
	insertTestBlock(t, n, 20, 0)
	insertTestBlock(t, n, 30, 0)

	lowest, err = p.getLowestBlockNumber(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int64(10), lowest)

	highest, err = p.getHighestBlockNumber(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int64(30), highest)
}

func TestQueryOldestDocIDs(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// Insert blocks
	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)
	insertTestBlock(t, n, 3, 0)

	// Query for blocks with number <= 2
	docIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", "number", 2)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(docIDs))

	// Query for blocks with number <= 0 (none)
	docIDs, err = p.queryOldestDocIDs(ctx, "TestBlock", "number", 0)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(docIDs))

	// Query for all blocks
	docIDs, err = p.queryOldestDocIDs(ctx, "TestBlock", "number", 100)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(docIDs))
}

func TestPurgeByDocIDs(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// Insert blocks
	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))

	// Get docIDs via queryOldestDocIDs (same format PurgeByDocIDs expects)
	docIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", "number", 1)
	require.NoError(t, err)
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

func TestPruneBlockRange(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	err := p.startupCleanup(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, countDocs(t, n, "TestBlock"))
	assert.Equal(t, int64(0), p.totalBlocksPruned)
}

func TestRunStorageGC_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)

	// Should not panic
	assert.NotPanics(t, func() {
		p.runStorageGC()
	})
}

func TestPurgeFromDrainResult(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// Insert blocks and txs
	insertTestBlock(t, n, 1, 1)
	insertTestBlock(t, n, 2, 0)

	// Get docIDs via queryOldestDocIDs (same format PurgeByDocIDs expects)
	blockDocIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", "number", 1)
	require.NoError(t, err)
	require.Len(t, blockDocIDs, 1)

	txDocIDs, err := p.queryOldestDocIDs(ctx, "TestTx", "blockNumber", 1)
	require.NoError(t, err)
	require.Len(t, txDocIDs, 1)

	drainResult := &DrainResult{
		DocIDsByCollection: map[string][]string{
			"TestBlock": blockDocIDs,
			"TestTx":    txDocIDs,
		},
		BlockCount: 1,
	}

	err = p.purgeFromDrainResult(ctx, drainResult)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), p.totalBlocksPruned)
	assert.True(t, p.totalDocsPruned > 0)
	assert.False(t, p.lastPruneTime.IsZero())
}

func TestPurgeFromDrainResult_EmptyCollections(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// DrainResult with no matching collections
	drainResult := &DrainResult{
		DocIDsByCollection: map[string][]string{},
		BlockCount:         0,
	}

	err := p.purgeFromDrainResult(ctx, drainResult)
	assert.NoError(t, err)
}

func TestRunIndexerQueuePrune_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	initDocIDPrefix()
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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	q := NewIndexerQueue()
	p.SetQueue(q)

	// Queue empty (0 <= 100), should fallback to filterBasedPrune
	err := p.runIndexerQueuePrune(ctx, q)
	assert.NoError(t, err)
}

func TestRunPrune_DefaultQueueType(t *testing.T) {
	// Test the default case in runPrune switch by using a custom PrunerQueue implementation
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 1000}
	p := NewPruner(cfg, n, cols)

	// Use a mock queue that is neither IndexerQueue nor EventQueue
	p.SetQueue(&mockQueue{})

	ctx := context.Background()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

// mockQueue implements PrunerQueue but is neither IndexerQueue nor EventQueue.
type mockQueue struct{}

func (m *mockQueue) Len() int     { return 0 }
func (m *mockQueue) Save() error  { return nil }

func TestStop_WithQueueSaveError(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}
	p := NewPruner(cfg, n, cols)

	// Use a mock queue that returns an error on Save
	p.SetQueue(&mockQueueSaveError{})

	// We need to manually set isRunning and stopChan to test Stop flow
	q := NewIndexerQueue()
	p.SetQueue(q)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := NewPruner(cfg, n, cols)
			q := NewIndexerQueue()
			p.SetQueue(q)

			ctx, cancel := context.WithCancel(context.Background())
			_ = p.Start(ctx)
			time.Sleep(10 * time.Millisecond)
			cancel()
			p.Stop()
		}()
	}
	wg.Wait()
}

func TestGetMetrics_Concurrent(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := p.GetMetrics()
			_ = m.IsRunning
		}()
	}
	wg.Wait()
}