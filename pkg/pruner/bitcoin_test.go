package pruner

// bitcoin_test.go exercises the pruner against the bitcoin adapter's own
// collections, stored through the production path (real converter, real
// block handler, signing identity in the context) so every height carries a
// real BlockSignature document. The startup-cleanup leg proves the three
// oldest of five signed heights are reclaimed across every collection while
// the retention window keeps its signatures and a residue-free lowest
// query; the runPrune leg proves the same shape under the filter-based
// prune path with the metrics counters moving.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	btc "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pruneHash is this file's deterministic 64-hex identity.
func pruneHash(key string) string {
	sum := sha256.Sum256([]byte("pruner-" + key))
	return hex.EncodeToString(sum[:])
}

// newBitcoinPrunerChain returns the converter whose collections, height
// queries, and docID sweeps the pruner consumes, with the bitcoin prefix.
func newBitcoinPrunerChain(t *testing.T) *btc.Converter {
	t.Helper()
	return btc.NewConverter(&config.Config{
		Chain: config.ChainConfig{
			Adapter: config.BitcoinAdapter,
			Name:    "Bitcoin",
			Network: "Mainnet",
		},
	})
}

// newBitcoinPrunerStore boots an embedded node with the bitcoin schema
// applied: the shared SetupTestDefraDB helper defaults to the evm schema,
// so the SDL comes from the converter under test.
func newBitcoinPrunerStore(t *testing.T, c *btc.Converter) *testutils.TestDefraDB {
	t.Helper()
	sdl, err := c.GetSchema()
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchema(t, sdl)
}

// pruneBlock builds one coinbase-only wire block at the given height, the
// shape that yields exactly one transaction, input, and output document per
// height.
func pruneBlock(height int64) *btc.Block {
	return &btc.Block{
		Hash:              pruneHash(fmt.Sprintf("block-%d", height)),
		Confirmations:     int64(1),
		StrippedSize:      int64(1000),
		Size:              int64(2000),
		Weight:            int64(4000),
		Height:            height,
		Version:           uint32(2),
		MerkleRoot:        pruneHash(fmt.Sprintf("merkle-%d", height)),
		Time:              1650000000 + height*600,
		Nonce:             uint32(height),
		Bits:              "1a0facdc",
		Chainwork:         pruneHash(fmt.Sprintf("chainwork-%d", height)),
		PreviousBlockHash: pruneHash(fmt.Sprintf("block-%d", height-1)),
		Tx: []btc.Tx{{
			TxID:     pruneHash(fmt.Sprintf("coinbase-%d", height)),
			Hash:     pruneHash(fmt.Sprintf("coinbase-wtxid-%d", height)),
			Version:  uint32(1),
			LockTime: uint32(0),
			Vin: []btc.Vin{{
				Coinbase: fmt.Sprintf("03%06x", height),
				Sequence: 4294967295,
			}},
			Vout: []btc.Vout{{
				Index: uint32(0),
				Value: 50.0,
				ScriptPubKey: btc.ScriptPubKey{
					Hex:  "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
					Type: "pubkeyhash",
				},
			}},
		}},
	}
}

// insertBitcoinSignedHeights stores the inclusive height range through the
// production path with a signing identity, so BlockSignature documents are
// minted the way StartIndexing mints them.
func insertBitcoinSignedHeights(t *testing.T, td *testutils.TestDefraDB, c chains.Converter, start, end int64) context.Context {
	t.Helper()
	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	fullIdent, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	ctx := defracontext.WithIdentity(context.Background(), fullIdent)

	for height := start; height <= end; height++ {
		result, err := c.Convert(ctx, pruneBlock(height))
		require.NoError(t, err, "convert block %d", height)
		_, err = handler.Store(ctx, result)
		require.NoError(t, err, "store block %d", height)
	}
	return ctx
}

func TestBitcoinPruner_StartupCleanup(t *testing.T) {
	ctx := context.Background()
	c := newBitcoinPrunerChain(t)
	td := newBitcoinPrunerStore(t, c)

	insertBitcoinSignedHeights(t, td, c, 100, 104)

	p := NewPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 2, PruneHistory: false}, td.Node, c)

	require.NoError(t, p.startupCleanup(ctx))

	metrics := p.GetMetrics()
	assert.Equal(t, int64(3), metrics.TotalBlocksPruned, "the three oldest heights must be pruned")
	assert.Greater(t, metrics.TotalDocsPruned, int64(0))
	assert.False(t, metrics.LastPruneTime.IsZero())

	// The retention window survives in every collection, signatures
	// included, one document per height per collection for the coinbase-only
	// shape.
	docIDs, err := c.GetDocIDsByBlockRange(ctx, td.Node, 103, 104)
	require.NoError(t, err)
	assert.Len(t, docIDs[c.Collections().BlockCollection()], 2)
	assert.Len(t, docIDs[c.Collections().BlockSignatureCollection()], 2)
	assert.Len(t, docIDs[btc.DefaultCollectionTransaction], 2)
	assert.Len(t, docIDs[btc.DefaultCollectionTxInput], 2)
	assert.Len(t, docIDs[btc.DefaultCollectionTxOutput], 2)

	// The pruned heights are gone across the board, and the lowest query
	// lands on the surviving window with no residue.
	remnants, err := c.GetDocIDsByBlockRange(ctx, td.Node, 100, 102)
	require.NoError(t, err)
	assert.Empty(t, remnants)

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(103), lowest)

	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(104), highest)
}

func TestBitcoinPruner_FilterBasedRunPrune(t *testing.T) {
	ctx := context.Background()
	c := newBitcoinPrunerChain(t)
	td := newBitcoinPrunerStore(t, c)

	insertBitcoinSignedHeights(t, td, c, 100, 104)

	p := NewPruner(&config.PrunerConfig{Enabled: true, MaxBlocks: 2}, td.Node, c)

	// A nil queue routes runPrune to the filter-based prune, which reads the
	// DB count the same way the startup cleanup does.
	require.NoError(t, p.runPrune(ctx))

	metrics := p.GetMetrics()
	assert.Equal(t, int64(3), metrics.TotalBlocksPruned)
	assert.Greater(t, metrics.TotalDocsPruned, int64(0))

	docIDs, err := c.GetDocIDsByBlockRange(ctx, td.Node, 100, 102)
	require.NoError(t, err)
	assert.Empty(t, docIDs, "the excess heights must be reclaimed across every collection")

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(103), lowest)
}
