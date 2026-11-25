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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
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

// storeQueryResidueDoc writes a block document without a number field,
// simulating the numberless rows left behind by purge residue or P2P
// replication that the block-number queries must exclude.
func storeQueryResidueDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, seed int64) string {
	t.Helper()
	return addQueryTestDoc(ctx, t, td, map[string]any{
		"tag": fmt.Sprintf("residue-%d", seed),
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

// TestQueryBlockNumber_ResidueRows exercises the block-number query against
// numberless rows (purge residue / P2P replication): the `_geq: 0` filter
// must exclude them so the query returns the next real block at the default
// limit of 1, and a collection holding only numberless rows must surface
// the corruption sentinel rather than "not found", so the pruner hard-fails
// instead of silently skipping pruning.
func TestQueryBlockNumber_ResidueRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB)
		wantLow  int64
		wantHigh int64
		wantErr  bool
	}{
		{
			name: "residue rows around valid blocks are skipped",
			setup: func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB) {
				storeQueryResidueDoc(ctx, t, td, 1)
				for _, num := range []int64{100, 101, 102} {
					storeQueryBlockDoc(ctx, t, td, num)
				}
				storeQueryResidueDoc(ctx, t, td, 2)
			},
			wantLow:  100,
			wantHigh: 102,
		},
		{
			name: "only numberless rows yields the corruption sentinel",
			setup: func(ctx context.Context, t *testing.T, td *testutils.TestDefraDB) {
				for i := range 3 {
					storeQueryResidueDoc(ctx, t, td, int64(i))
				}
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)
			ctx := context.Background()

			tc.setup(ctx, t, td)

			lowest, errLow := chains.QueryBlockNumber(ctx, td.Node, queryTestCollection,
				constants.NumberFieldName, "ASC", "TestQueryBlockNumber_ResidueRows", 1)
			highest, errHigh := chains.QueryBlockNumber(ctx, td.Node, queryTestCollection,
				constants.NumberFieldName, "DESC", "TestQueryBlockNumber_ResidueRows", chains.HighestBlockQueryLimit)

			if tc.wantErr {
				require.Error(t, errLow)
				assert.ErrorIs(t, errLow, chains.ErrBlockNumberCorrupt)
				assert.False(t, errors.IsErrNotFound(errLow), "corruption must not read as not-found")
				require.Error(t, errHigh)
				assert.ErrorIs(t, errHigh, chains.ErrBlockNumberCorrupt)
				return
			}
			require.NoError(t, errLow)
			assert.Equal(t, tc.wantLow, lowest)
			require.NoError(t, errHigh)
			assert.Equal(t, tc.wantHigh, highest)
		})
	}
}

// TestQueryBlockNumber_EmptyCollection pins the benign branch: with no
// documents at all the query reports not-found, not corruption.
func TestQueryBlockNumber_EmptyCollection(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)

	_, err := chains.QueryBlockNumber(context.Background(), td.Node, queryTestCollection,
		constants.NumberFieldName, "ASC", "TestQueryBlockNumber_EmptyCollection", 1)
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err), "an empty collection is benign not-found")
	assert.NotErrorIs(t, err, chains.ErrBlockNumberCorrupt)
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

// TestQueryBlockNumber_AfterPurge is the regression test for the
// 2026-09-02 incident: a PurgeByDocIDs residue left a dangling row behind,
// so the ASC lowest-block query kept returning a row whose number field was
// unparseable. After purging the lowest blocks, the query must return the
// next stored block without error.
func TestQueryBlockNumber_AfterPurge(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDBWithSchema(t, queryTestSDL)
	ctx := context.Background()

	for _, num := range []int64{100, 101, 102, 103, 104} {
		storeQueryBlockDoc(ctx, t, td, num)
	}

	lowest, err := chains.QueryBlockNumber(ctx, td.Node, queryTestCollection,
		constants.NumberFieldName, "ASC", "TestQueryBlockNumber_AfterPurge", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	docIDs, err := chains.QueryCollectionDocIDs(ctx, td.Node, queryTestCollection, constants.NumberFieldName, 100, 101)
	require.NoError(t, err)
	require.Len(t, docIDs, 2)

	col, err := td.Node.DB.GetCollectionByName(ctx, queryTestCollection)
	require.NoError(t, err)

	purgeIDs := make([]client.DocID, 0, len(docIDs))
	for _, id := range docIDs {
		docID, err := client.NewDocIDFromString(id)
		require.NoError(t, err)
		purgeIDs = append(purgeIDs, docID)
	}
	require.NoError(t, col.PurgeByDocIDs(ctx, purgeIDs, true))

	lowest, err = chains.QueryBlockNumber(ctx, td.Node, queryTestCollection,
		constants.NumberFieldName, "ASC", "TestQueryBlockNumber_AfterPurge", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(102), lowest)

	highest, err := chains.QueryBlockNumber(ctx, td.Node, queryTestCollection,
		constants.NumberFieldName, "DESC", "TestQueryBlockNumber_AfterPurge", chains.HighestBlockQueryLimit)
	require.NoError(t, err)
	assert.Equal(t, int64(104), highest)
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
