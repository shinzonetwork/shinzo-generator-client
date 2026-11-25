package chains_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// queryTestSDL defines a minimal stand-in for a chain block collection: a
// nullable Int number field (indexed, as the real block schemas do) plus a
// tag field that keeps content-derived DocIDs unique. The shared query
// helpers take collection and field names as parameters, so the suite does
// not depend on either chain's schema.
const queryTestSDL = `
type QueryTestBlock {
	number: Int @index
	tag: String
}
`

const queryTestCollection = "QueryTestBlock"

// storeQueryBlockDoc writes a block document carrying a usable number.
func storeQueryBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, num int64) string {
	t.Helper()
	return addQueryTestDoc(ctx, t, td, map[string]any{
		constants.NumberFieldName: num,
		"tag":                     fmt.Sprintf("block-%d", num),
	})
}

// addQueryTestDoc inserts one document through the same production write
// path the converters rely on (NewDocFromMap, AddDocument).
func addQueryTestDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, data map[string]any) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, queryTestCollection)
	require.NoError(t, err)

	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}

// TestQueryBlockNumber_CustomLimits pins the parameterized query limit:
// every supported limit must build valid GraphQL, honor the ordering, and
// stay safe when the limit exceeds the number of stored blocks.
func TestQueryBlockNumber_CustomLimits(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeQueryBlockDoc(ctx, t, td, num)
	}

	tests := []struct {
		order string
		limit int
		want  int64
	}{
		{"ASC", 1, 100},
		{"ASC", 2, 100},
		{"ASC", 3, 100},
		{"ASC", 7, 100},
		{"ASC", 20, 100},
		{"ASC", 50, 100},
		{"DESC", 1, 104},
		{"DESC", 3, 104},
		{"DESC", 7, 104},
		{"DESC", 20, 104},
		{"DESC", 50, 104},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_limit%d", tc.order, tc.limit), func(t *testing.T) {
			t.Parallel()
			got, err := chains.QueryBlockNumber(ctx, td.Node, queryTestCollection,
				constants.NumberFieldName, tc.order, "TestQueryBlockNumber_CustomLimits", tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestQueryBlockNumber_UnknownCollection pins the GQL error path: querying a
// collection that does not exist surfaces an error instead of a not-found.
func TestQueryBlockNumber_UnknownCollection(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)

	_, err := chains.QueryBlockNumber(context.Background(), td.Node, "NoSuchCollection",
		constants.NumberFieldName, "ASC", "TestQueryBlockNumber_UnknownCollection", 1)
	require.Error(t, err)
	assert.NotErrorIs(t, err, chains.ErrBlockNumberCorrupt)
}

// TestQueryCollectionDocIDs_Ranges covers the chunked range query: hits in a
// single chunk, hits spanning several chunks, empty windows, and boundaries.
func TestQueryCollectionDocIDs_Ranges(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)
	ctx := context.Background()

	storeQueryBlockDoc(ctx, t, td, 150)
	storeQueryBlockDoc(ctx, t, td, 250)

	tests := []struct {
		name    string
		from    int64
		to      int64
		wantLen int
	}{
		{"single chunk hit", 100, 199, 1},
		{"range spanning chunks", 100, 300, 2},
		{"exact single block", 150, 150, 1},
		{"window without hits", 300, 400, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			docIDs, err := chains.QueryCollectionDocIDs(ctx, td.Node, queryTestCollection, constants.NumberFieldName, tc.from, tc.to)
			require.NoError(t, err)
			assert.Len(t, docIDs, tc.wantLen)
		})
	}
}

// TestQueryCollectionDocIDs_ReversedRange pins the loop guard: an inverted
// window runs zero chunks and returns no docIDs without error.
func TestQueryCollectionDocIDs_ReversedRange(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)

	docIDs, err := chains.QueryCollectionDocIDs(context.Background(), td.Node, queryTestCollection, constants.NumberFieldName, 5, 1)
	require.NoError(t, err)
	assert.Empty(t, docIDs)
}

// TestQueryCollectionDocIDs_UnknownCollection pins the GQL error path of the
// range query: a missing collection errors instead of returning nothing.
func TestQueryCollectionDocIDs_UnknownCollection(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)

	_, err := chains.QueryCollectionDocIDs(context.Background(), td.Node, "NoSuchCollection", constants.NumberFieldName, 1, 2)
	require.Error(t, err)
}
