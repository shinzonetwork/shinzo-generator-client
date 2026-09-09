package pruner

import (
	"context"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// The pruner finds the oldest documents with `order: {<field>: ASC}, limit`, and the block-exists
// check filters by `<field>: {_eq}`. DefraDB serves both from a single-field index on that field,
// and index-backed ordering requires the field to lead the index.
//
// Declaring an index is not the same as having a usable one: an index whose build errored or has
// not finished is still listed on the collection but is excluded from query planning, so assert on
// its lifecycle status rather than its presence.
func TestSchemaIndexesBlockNumberField(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	cols, err := chains.NewCollections(nil)
	require.NoError(t, err)

	blockCol, err := cols.GetCollection(chains.TypeBlock)
	require.NoError(t, err)
	txCol, err := cols.GetCollection(chains.TypeTransaction)
	require.NoError(t, err)
	logCol, err := cols.GetCollection(chains.TypeLog)
	require.NoError(t, err)
	aleCol, err := cols.GetCollection(chains.TypeAccessListEntry)
	require.NoError(t, err)
	sigCol, err := cols.GetCollection(chains.TypeBlockSignature)
	require.NoError(t, err)

	cases := []struct {
		collection string
		field      string
	}{
		{blockCol, evm.NumberFieldValue},
		{txCol, evm.BlockNumberKeyValue},
		{logCol, evm.BlockNumberKeyValue},
		{aleCol, evm.BlockNumberKeyValue},
		{sigCol, evm.BlockNumberKeyValue},
	}

	for _, c := range cases {
		t.Run(c.collection, func(t *testing.T) {
			col, err := td.Node.DB.GetCollectionByName(ctx, c.collection)
			require.NoError(t, err)

			indexes, err := col.ListIndexes(ctx)
			require.NoError(t, err)

			var leading *client.ListIndexesResult
			for i := range indexes {
				fields := indexes[i].Description.Fields
				if len(fields) > 0 && fields[0].Name == c.field {
					leading = &indexes[i]
					break
				}
			}

			require.NotNil(t, leading, "no index leads with %s", c.field)
			require.NotEqual(t, client.ErroredActionStatus, leading.Execution.Status,
				"index %s errored: %s", leading.Description.Name, leading.Execution.Reason)
			require.NotEqual(t, client.InProgressActionStatus, leading.Execution.Status,
				"index %s has not finished building", leading.Description.Name)
		})
	}
}
