package pruner

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/require"
)

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

// testBlockColName is the block collection name in the test schema.
// testTxColName is the dependent collection name.
const (
	testBlockColName = "TestBlock"
	testTxColName    = "TestTx"
)

// testChain implements BlockRangeReader against a real DefraDB node,
// using GQL queries to fetch block ranges and docIDs. It mirrors the
// pattern used by BlockHandler but with test-schema collection names.
type testChain struct {
	node *node.Node
}

func (tc *testChain) GetLowestStoredBlockNumber(ctx context.Context) (int64, error) {
	query := fmt.Sprintf(`query { %s (order: {number: ASC}, limit: 1) { number }}`, testBlockColName)
	return tc.queryBlockNumber(ctx, query)
}

func (tc *testChain) GetHighestStoredBlockNumber(ctx context.Context) (int64, error) {
	query := fmt.Sprintf(`query { %s (order: {number: DESC}, limit: 1) { number }}`, testBlockColName)
	return tc.queryBlockNumber(ctx, query)
}

func (tc *testChain) GetDocIDsByBlockRange(ctx context.Context, from, to int64) (map[string][]string, error) {
	result := make(map[string][]string)

	for _, col := range []struct {
		name  string
		field string
	}{
		{testBlockColName, "number"},
		{testTxColName, "blockNumber"},
	} {
		query := fmt.Sprintf(
			`query { %s(filter: {%s: {_geq: %d, _leq: %d}}) { _docID } }`,
			col.name, col.field, from, to,
		)
		res := tc.node.DB.ExecRequest(ctx, query)
		if len(res.GQL.Errors) > 0 {
			return nil, fmt.Errorf("query %s: %w", col.name, res.GQL.Errors[0])
		}

		data, ok := res.GQL.Data.(map[string]any)
		if !ok {
			continue
		}

		var docs []any
		switch typed := data[col.name].(type) {
		case []any:
			docs = typed
		case []map[string]any:
			docs = make([]any, len(typed))
			for i, d := range typed {
				docs[i] = d
			}
		default:
			continue
		}

		for _, doc := range docs {
			m, ok := doc.(map[string]any)
			if !ok {
				continue
			}
			if docID, ok := m["_docID"].(string); ok {
				result[col.name] = append(result[col.name], docID)
			}
		}
	}

	return result, nil
}

func (tc *testChain) GetCollections() []string {
	return []string{testBlockColName, testTxColName}
}

// Collections returns a StubCollections with the "Test" prefix so the pruner
// can resolve collection names via the chains.Collections interface.
func (tc *testChain) Collections() chains.Collections {
	return chains.NewStubCollections("Test")
}

func (tc *testChain) queryBlockNumber(ctx context.Context, query string) (int64, error) {
	result := tc.node.DB.ExecRequest(ctx, query)
	if len(result.GQL.Errors) > 0 {
		return 0, fmt.Errorf("query failed: %w", result.GQL.Errors[0])
	}

	data, ok := result.GQL.Data.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("block not found")
	}

	var block map[string]any
	switch arr := data[testBlockColName].(type) {
	case []any:
		if len(arr) == 0 {
			return 0, fmt.Errorf("block not found")
		}
		block, _ = arr[0].(map[string]any)
	case []map[string]any:
		if len(arr) == 0 {
			return 0, fmt.Errorf("block not found")
		}
		block = arr[0]
	default:
		return 0, fmt.Errorf("block not found")
	}

	switch v := block["number"].(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	}
	return 0, fmt.Errorf("invalid number type")
}

// startTestNode creates a real DefraDB node with the test schema.
func startTestNode(t *testing.T) *node.Node {
	t.Helper()
	td := testutils.SetupTestDefraDBWithSchema(t, testSchema)
	return td.Node
}

// newTestChain creates a testChain backed by the given node.
func newTestChain(n *node.Node) *testChain {
	return &testChain{node: n}
}

// newTestPruner creates a Pruner wired to a testChain and overrides the
// resolved block-collection name. testChain.Collections() returns
// StubCollections("Test") whose block name is "Test__Block", but the test
// schema uses "TestBlock" — so we override the field directly (accessible
// because tests are in package pruner).
// Returns the testChain as well for tests that need direct chain queries.
func newTestPruner(cfg *config.PrunerConfig, n *node.Node) (*Pruner, *testChain) {
	tc := newTestChain(n)
	p := NewPruner(cfg, n, tc)
	p.blockCollection = testBlockColName
	return p, tc
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
