package pruner

import (
	"context"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/require"
)

// The pruner finds the oldest documents with `order: {<field>: ASC}, limit` and the block-signing
// read-back filters by `<field>: {_eq}`. DefraDB serves both from a single-field index on that
// field (index-backed ordering requires it to be the index's first field). This checks the applied
// schema declares that index on every collection queried by block number.
func TestSchemaIndexesBlockNumberField(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	ctx := context.Background()

	cases := []struct {
		collection string
		field      string
	}{
		{constants.CollectionBlock, constants.NumberFieldValue},
		{constants.CollectionTransaction, constants.BlockNumberKeyValue},
		{constants.CollectionLog, constants.BlockNumberKeyValue},
		{constants.CollectionAccessListEntry, constants.BlockNumberKeyValue},
		{constants.CollectionBlockSignature, constants.BlockNumberKeyValue},
	}
	for _, c := range cases {
		col, err := td.Node.DB.GetCollectionByName(ctx, c.collection)
		require.NoError(t, err, "get collection %s", c.collection)
		require.NotEmpty(t, col.Version().GetIndexesOnField(c.field),
			"collection %s must have an index on %s", c.collection, c.field)
	}
}
