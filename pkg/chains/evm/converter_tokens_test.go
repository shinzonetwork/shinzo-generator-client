package evm

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

func TestTokenMode_ReadsTokenTables(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain.Name = "Bsc"
	cfg.Chain.Mode = config.ChainModeTokens
	cfg.Chain.Tokens = []config.Token{{Address: "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d"}}
	c, err := NewConverter(cfg)
	require.NoError(t, err)
	sdl, err := c.GetSchema()
	require.NoError(t, err)
	td := testutils.SetupTestDefraDBWithSchema(t, sdl)
	ctx := context.Background()

	for _, add := range []string{
		fmt.Sprintf(`%s(input: {number: 7, hash: "0x07"})`, c.collections.Header),
		fmt.Sprintf(`%s(input: {number: 9, hash: "0x09"})`, c.collections.Header),
		fmt.Sprintf(`%s(input: {blockNumber: 7, asset: "8d0d000ee44948fc98c9b98a4fa4921476f08b0d"})`, c.collections.Evidence),
		fmt.Sprintf(`%s(input: {blockNumber: 7, asset: "8d0d000ee44948fc98c9b98a4fa4921476f08b0d"})`, c.collections.StateCapture),
		fmt.Sprintf(`%s(input: {blockNumber: 7})`, c.collections.BlockAttestation),
	} {
		res := td.Node.DB.ExecRequest(ctx, "mutation { add_"+add+" { _docID } }")
		require.Empty(t, res.GQL.Errors)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(7), lowest)
	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(9), highest)

	docIDs, err := c.GetDocIDsByBlockRange(ctx, td.Node, 7, 7)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{c.collections.Header, c.collections.Evidence, c.collections.StateCapture, c.collections.BlockAttestation}, slices.Collect(maps.Keys(docIDs)))
	for name, ids := range docIDs {
		assert.Len(t, ids, 1, name)
	}
}
