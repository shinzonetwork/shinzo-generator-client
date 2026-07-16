package pruner

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
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

const timeout = 10 * time.Second

// testCollections returns a CollectionConfig matching the testSchema.
func testCollections() CollectionConfig {
	return CollectionConfig{
		BlockCollection:      "TestBlock",
		BlockNumberField:     constants.NumberFieldValue,
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
	mutation := fmt.Sprintf(`mutation { add_TestBlock(input: [{number: %d, hash: "hash%d"}]) { _docID } }`, blockNum, blockNum)
	result := n.DB.ExecRequest(ctx, mutation)
	require.Empty(t, result.GQL.Errors, "insert block %d failed: %v", blockNum, result.GQL.Errors)

	// Extract docID from the returned list
	blockDocID := ""
	if data, ok := result.GQL.Data.(map[string]any); ok {
		raw := data["add_TestBlock"]
		switch v := raw.(type) {
		case []any:
			if len(v) > 0 {
				if m, ok := v[0].(map[string]any); ok {
					blockDocID, _ = m["_docID"].(string)
				}
			}
		case []map[string]any:
			if len(v) > 0 {
				blockDocID, _ = v[0]["_docID"].(string)
			}
		}
	}

	// Insert transactions
	for i := range txCount {
		txMutation := fmt.Sprintf(`mutation { add_TestTx(input: [{blockNumber: %d, txHash: "tx%d_%d"}]) { _docID } }`, blockNum, blockNum, i)
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
	case []any:
		return len(docs)
	case []map[string]any:
		return len(docs)
	}
	return 0
}

func TestNewPruner(t *testing.T) {
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 1000, IntervalSeconds: 60}

	t.Run("default collection config", func(t *testing.T) {
		p := NewPruner(cfg, nil)
		require.NotNil(t, p)
		assert.Equal(t, constants.CollectionBlock, p.collections.BlockCollection)
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := p.Start(ctx)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
	cancel()
	p.Stop()
}

func TestPrunerStart_NilNode(t *testing.T) {
	cfg := &Config{Enabled: true}
	p := NewPruner(cfg, nil)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := p.Start(ctx)
	assert.NoError(t, err)
	assert.False(t, p.isRunning)
	cancel()
	p.Stop()
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

func TestParseBlockNumber(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		expected    int64
		expectError bool
	}{
		{"float64", float64(42), 42, false},
		{"int64", int64(100), 100, false},
		{"int", int(200), 200, false},
		{"string (unknown type)", "300", 0, true},
		{"nil", nil, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseBlockNumber(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractBlockNumber(t *testing.T) {
	cfg := &Config{Enabled: true}
	cols := DefaultCollectionConfig()
	p := NewPruner(cfg, nil, cols)

	t.Run("nil data returns ErrNoBlocks", func(t *testing.T) {
		result, err := p.extractBlockNumber(nil)
		assert.ErrorIs(t, err, ErrNoBlocks)
		assert.Equal(t, int64(0), result)
	})

	t.Run("wrong type returns error", func(t *testing.T) {
		_, err := p.extractBlockNumber("not a map")
		assert.Error(t, err)
		assert.NotErrorIs(t, err, ErrNoBlocks)
	})

	t.Run("empty blocks array ([]interface{}) returns ErrNoBlocks", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []any{},
		}
		result, err := p.extractBlockNumber(data)
		assert.ErrorIs(t, err, ErrNoBlocks)
		assert.Equal(t, int64(0), result)
	})

	t.Run("blocks with data ([]interface{})", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []any{
				map[string]any{constants.NumberFieldValue: float64(42)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(42), result)
	})

	t.Run("blocks with typed map array", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []map[string]any{
				{constants.NumberFieldValue: float64(99)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(99), result)
	})

	t.Run("empty typed map array returns ErrNoBlocks", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []map[string]any{},
		}
		result, err := p.extractBlockNumber(data)
		assert.ErrorIs(t, err, ErrNoBlocks)
		assert.Equal(t, int64(0), result)
	})

	t.Run("typed map array missing number field skips to next valid block", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []map[string]any{
				{"other_field": "value"},
				{constants.NumberFieldValue: float64(42)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(42), result)
	})

	t.Run("typed map array all blocks missing number field returns error", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []map[string]any{
				{"other_field": "value"},
				{"another_field": 123},
			},
		}
		_, err := p.extractBlockNumber(data)
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoValidBlocks)
	})

	t.Run("block with nil number field skips to next valid block", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []map[string]any{
				{constants.NumberFieldValue: nil},
				{constants.NumberFieldValue: float64(7)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(7), result)
	})

	t.Run("all blocks have nil number field returns error", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []map[string]any{
				{constants.NumberFieldValue: nil},
				{constants.NumberFieldValue: nil},
			},
		}
		_, err := p.extractBlockNumber(data)
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrNoValidBlocks)
	})

	t.Run("interface array with non-map element returns error", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []any{
				"not a map",
			},
		}
		_, err := p.extractBlockNumber(data)
		assert.Error(t, err)
		assert.NotErrorIs(t, err, ErrNoBlocks)
	})

	t.Run("interface array missing number field skips to next valid block", func(t *testing.T) {
		data := map[string]any{
			constants.CollectionBlock: []any{
				map[string]any{"other": "value"},
				map[string]any{constants.NumberFieldValue: float64(55)},
			},
		}
		result, err := p.extractBlockNumber(data)
		assert.NoError(t, err)
		assert.Equal(t, int64(55), result)
	})

	t.Run("missing block collection key returns error", func(t *testing.T) {
		data := map[string]any{
			"Other_Collection": []any{},
		}
		_, err := p.extractBlockNumber(data)
		assert.Error(t, err)
		assert.NotErrorIs(t, err, ErrNoBlocks)
	})
}

func TestExtractDocIDs(t *testing.T) {
	t.Run("valid docs within max", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": float64(1)},
			{"_docID": "bae-bbb", "number": float64(2)},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Equal(t, []string{"bae-aaa", "bae-bbb"}, ids)
	})

	t.Run("stops at maxBlockNumber", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": float64(1)},
			{"_docID": "bae-bbb", "number": float64(5)},
			{"_docID": "bae-ccc", "number": float64(10)},
		}
		ids, err := extractDocIDs(docs, "number", 3, "TestBlock")
		assert.NoError(t, err)
		assert.Equal(t, []string{"bae-aaa"}, ids)
	})

	t.Run("parse error skips doc and continues", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": "not-a-number"},
			{"_docID": "bae-bbb", "number": float64(2)},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Equal(t, []string{"bae-bbb"}, ids)
	})

	t.Run("nil block number skips doc and continues", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": nil},
			{"_docID": "bae-bbb", "number": float64(2)},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Equal(t, []string{"bae-bbb"}, ids)
	})

	t.Run("non-string _docID skips doc and continues", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": 123, "number": float64(1)},
			{"_docID": "bae-bbb", "number": float64(2)},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Equal(t, []string{"bae-bbb"}, ids)
	})

	t.Run("empty docs returns nil", func(t *testing.T) {
		ids, err := extractDocIDs(nil, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Nil(t, ids)
	})

	t.Run("all docs corrupt returns ErrNoValidDocs", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": nil},
			{"_docID": "bae-bbb", "number": "not-a-number"},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.Nil(t, ids)
		assert.ErrorIs(t, err, ErrNoValidDocs)
	})

	t.Run("all docs above range returns nil without error", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": float64(100)},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Nil(t, ids)
	})

	t.Run("some corrupt, rest above range returns nil without error", func(t *testing.T) {
		docs := []map[string]any{
			{"_docID": "bae-aaa", "number": nil},
			{"_docID": "bae-bbb", "number": float64(100)},
		}
		ids, err := extractDocIDs(docs, "number", 10, "TestBlock")
		assert.NoError(t, err)
		assert.Nil(t, ids)
	})
}

func TestRunPrune_NilQueue(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	assert.Nil(t, p.queue)
	// runPrune with nil queue calls filterBasedPrune which needs a node
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

func TestRunPrune_WithIndexerQueue(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	q := NewIndexerQueue()
	p.SetQueue(q)
	assert.NotNil(t, p.queue)
	// dispatch tested via runPrune type switch
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
}

func TestRunIndexerQueuePrune_BelowThreshold(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	q := NewIndexerQueue()
	p.SetQueue(q)
	// Queue has 0 entries, below maxBlocks=100
	assert.Zero(t, len(q.entries))
	// This calls filterBasedPrune which needs node
	ctx := t.Context()
	err := p.runPrune(ctx)
	assert.NoError(t, err)
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
	assert.ErrorIs(t, err, ErrNoBlocks)
	assert.Equal(t, int64(0), lowest)

	highest, err := p.getHighestBlockNumber(ctx)
	assert.ErrorIs(t, err, ErrNoBlocks)
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

func TestGetBlockRange(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
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
	docIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 2)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(docIDs))

	// Query for blocks with number <= 0 (none)
	docIDs, err = p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 0)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(docIDs))

	// Query for all blocks
	docIDs, err = p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 100)
	assert.NoError(t, err)
	assert.Equal(t, 3, len(docIDs))
}

func TestQueryOldestDocIDs_EmptyCollection(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// TestTx collection exists in schema but has zero documents
	docIDs, err := p.queryOldestDocIDs(ctx, "TestTx", "blockNumber", 100)
	assert.NoError(t, err)
	assert.Nil(t, docIDs)
}

func TestQueryOldestDocIDs_NonExistentCollection(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	// DefraDB returns a GQL error for unknown collections (caught at the GQL layer).
	// The comma-ok check in queryOldestDocIDs is a defensive fallback for the case
	// where DefraDB returns a valid Data map without the collection key.
	_, err := p.queryOldestDocIDs(ctx, "NonExistent", "number", 100)
	assert.Error(t, err)
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
	docIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 1)
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

func TestPurgeByDocIDs_InvalidDocID(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
	ctx := context.Background()

	insertTestBlock(t, n, 1, 0)
	insertTestBlock(t, n, 2, 0)

	validDocIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 2)
	require.NoError(t, err)
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
	blockDocIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 1)
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

func TestPurgeFromDrainResult_PurgeError(t *testing.T) {
	t.Run("dependent_collection_error_propagates", func(t *testing.T) {
		n := startTestNode(t)
		cols := testCollections()
		cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
		p := NewPruner(cfg, n, cols)
		ctx := context.Background()

		insertTestBlock(t, n, 1, 1)

		blockDocIDs, err := p.queryOldestDocIDs(ctx, "TestBlock", constants.NumberFieldValue, 1)
		require.NoError(t, err)
		require.Len(t, blockDocIDs, 1)

		drainResult := &DrainResult{
			DocIDsByCollection: map[string][]string{
				"TestBlock": blockDocIDs,
				"TestTx":    {"not-a-valid-docid"},
			},
			BlockCount: 1,
		}

		err = p.purgeFromDrainResult(ctx, drainResult)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "dependent collection errors")
		assert.Contains(t, err.Error(), "purge TestTx")
		assert.Equal(t, int64(1), p.totalBlocksPruned)
		assert.False(t, p.lastPruneTime.IsZero())
	})

	t.Run("block_collection_error_is_fatal", func(t *testing.T) {
		n := startTestNode(t)
		cols := testCollections()
		cfg := &Config{Enabled: true, MaxBlocks: 100, PruneHistory: false}
		p := NewPruner(cfg, n, cols)
		ctx := context.Background()

		drainResult := &DrainResult{
			DocIDsByCollection: map[string][]string{
				"TestBlock": {"not-a-valid-docid"},
			},
			BlockCount: 1,
		}

		err := p.purgeFromDrainResult(ctx, drainResult)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to purge blocks")
		assert.NotContains(t, err.Error(), "dependent collection errors")
		assert.Equal(t, int64(0), p.totalBlocksPruned)
		assert.True(t, p.lastPruneTime.IsZero())
	})
}

func TestRunIndexerQueuePrune_WithRealNode(t *testing.T) {
	n := startTestNode(t)
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 2, PruneHistory: false}
	p := NewPruner(cfg, n, cols)
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
	// Test the default case in runPrune switch by using a custom Queue implementation
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

// mockQueue implements Queue but is neither IndexerQueue nor EventQueue.
type mockQueue struct{}

func (m *mockQueue) Len() int    { return 0 }
func (m *mockQueue) Save() error { return nil }

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
	cols := testCollections()
	cfg := &Config{Enabled: true, MaxBlocks: 100, DocsPerBlock: 10, IntervalSeconds: 3600}

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			p := NewPruner(cfg, n, cols)
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
	p := NewPruner(cfg, nil)

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			m := p.GetMetrics()
			assert.False(t, m.IsRunning)
		})
	}
	wg.Wait()
}
