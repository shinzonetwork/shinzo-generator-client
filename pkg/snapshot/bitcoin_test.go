package snapshot

// bitcoin_test.go exercises the snapshot round-trip against the bitcoin
// adapter's own collections: the production store path (real converter,
// block handler, and signing identity) over heights 100..104 an aligned
// five-height snapshot with per-block signature roots in the header, the
// ImportKV restore into a fresh bitcoin-schema node, and the purged-ranges
// variant where the snapshotter must skip the reclaimed heights and land on
// the next aligned window.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	btc "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapHash is this file's deterministic 64-hex identity, mirroring the
// chain fakes the integration suites stand up.
func snapHash(key string) string {
	sum := sha256.Sum256([]byte("snapshot-" + key))
	return hex.EncodeToString(sum[:])
}

// newBitcoinConverter builds the converter under test with the bitcoin
// prefix and default batch knobs.
func newBitcoinConverter(t *testing.T) *btc.Converter {
	t.Helper()
	return btc.NewConverter(&config.Config{
		Chain: config.ChainConfig{
			Adapter: config.BitcoinAdapter,
			Name:    "Bitcoin",
			Network: "Mainnet",
		},
	})
}

// newBitcoinSnapshotDB boots an embedded node with the bitcoin schema
// applied: the shared SetupTestDefraDB default is the evm schema, so the
// SDL has to come from the converter under test.
func newBitcoinSnapshotDB(t *testing.T, c *btc.Converter) *testutils.TestDefraDB {
	t.Helper()
	sdl, err := c.GetSchema()
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchema(t, sdl)
}

// snapBlock builds one coinbase-only wire block at the given height, the
// shape that yields exactly one transaction, input, and output document per
// height.
func snapBlock(height int64) *btc.Block {
	return &btc.Block{
		Hash:              snapHash(fmt.Sprintf("block-%d", height)),
		Confirmations:     int64(1),
		StrippedSize:      int64(1000),
		Size:              int64(2000),
		Weight:            int64(4000),
		Height:            height,
		Version:           uint32(2),
		MerkleRoot:        snapHash(fmt.Sprintf("merkle-%d", height)),
		Time:              1650000000 + height*600,
		Nonce:             uint32(height),
		Bits:              "1a0facdc",
		Chainwork:         snapHash(fmt.Sprintf("chainwork-%d", height)),
		PreviousBlockHash: snapHash(fmt.Sprintf("block-%d", height-1)),
		Tx:                []btc.Tx{snapCoinbaseTx(height)},
	}
}

// snapCoinbaseTx builds the coinbase transaction of one synthetic height: a
// single coinbase input, one 50-BTC output, and no fee (undo data never
// prices the coinbase).
func snapCoinbaseTx(height int64) btc.Tx {
	return btc.Tx{
		TxID:     snapHash(fmt.Sprintf("coinbase-%d", height)),
		Hash:     snapHash(fmt.Sprintf("coinbase-wtxid-%d", height)),
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
	}
}

// insertBitcoinHeightsWithIdentity stores the inclusive height range through
// the production path: the real converter, the real block handler, and a
// signing identity in the context, so BlockSignature documents are minted
// the way StartIndexing mints them.
func insertBitcoinHeightsWithIdentity(t *testing.T, td *testutils.TestDefraDB, c *btc.Converter, start, end int64) (context.Context, *defra.BlockHandler) {
	t.Helper()
	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	fullIdent, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	ctx := defracontext.WithIdentity(context.Background(), fullIdent)

	for height := start; height <= end; height++ {
		result, err := c.Convert(ctx, snapBlock(height))
		require.NoError(t, err, "convert block %d", height)
		_, err = handler.Store(ctx, result)
		require.NoError(t, err, "store block %d", height)
	}
	return ctx, handler
}

// purgeHeightRange reclaims every document of the inclusive height
// range the way the pruner does: one docID sweep per collection from the
// converter, then a content-addressed purge.
func purgeHeightRange(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *btc.Converter, lo, hi int64) {
	t.Helper()
	docIDsByCollection, err := c.GetDocIDsByBlockRange(ctx, td.Node, lo, hi)
	require.NoError(t, err)
	require.NotEmpty(t, docIDsByCollection)
	for collection, docIDs := range docIDsByCollection {
		col, err := td.Node.DB.GetCollectionByName(ctx, collection)
		require.NoError(t, err)
		purgeIDs := make([]client.DocID, 0, len(docIDs))
		for _, id := range docIDs {
			parsed, err := client.NewDocIDFromString(id)
			require.NoError(t, err)
			purgeIDs = append(purgeIDs, parsed)
		}
		require.NoError(t, col.PurgeByDocIDs(ctx, purgeIDs, true), "purge %s", collection)
	}
}

func TestBitcoinSnapshot_RoundTrip(t *testing.T) {
	c := newBitcoinConverter(t)
	td := newBitcoinSnapshotDB(t, c)

	identCtx, _ := insertBitcoinHeightsWithIdentity(t, td, c, 100, 104)

	snapshotDir := t.TempDir()
	s := New(&config.SnapshotConfig{Dir: snapshotDir, BlocksPerFile: 5}, td.Node, c)
	s.ctx = identCtx

	require.NoError(t, s.checkAndSnapshot(identCtx))
	assert.Equal(t, int64(104), s.lastSnapshotBlock)

	snapshotFile := filepath.Join(snapshotDir, "snapshot_100_104.kvsnap.gz")
	_, err := os.Stat(snapshotFile)
	require.NoError(t, err, "snapshot file should exist")

	header := readSnapshotHeader(t, snapshotFile)
	assert.Equal(t, constants.HeaderMagicValue, header.Magic)
	assert.Equal(t, int64(100), header.StartBlock)
	assert.Equal(t, int64(104), header.EndBlock)
	assert.Len(t, header.BlockSigMerkleRoots, 5, "one signature root per height in the header")

	sigs, err := QuerySnapshotSignatures(context.Background(), td.Node, c.Collections().SnapshotSignatureCollection())
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	sig := sigs["snapshot_100_104.kvsnap.gz"]
	require.NotNil(t, sig)
	assert.Equal(t, constants.Secp256k1ValueString, sig.SignatureType)
	assert.NotEmpty(t, sig.MerkleRoot)
	assert.NotEmpty(t, sig.SignatureValue)

	// ImportKV into a fresh bitcoin-schema node and rebuild the indexes the
	// bulk KV import skips.
	td2 := newBitcoinSnapshotDB(t, c)
	importResult, err := ImportKV(context.Background(), td2.Node, snapshotFile)
	require.NoError(t, err)
	assert.Equal(t, int64(100), importResult.StartBlock)
	assert.Equal(t, int64(104), importResult.EndBlock)

	require.NoError(t, RebuildAllIndexes(context.Background(), td2.Node, c.GetCollections()))

	restoredLowest, err := c.GetLowestStoredBlockNumber(context.Background(), td2.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), restoredLowest)
	restoredHighest, err := c.GetHighestStoredBlockNumber(context.Background(), td2.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(104), restoredHighest)

	docIDsByCol, err := c.GetDocIDsByBlockRange(context.Background(), td2.Node, 100, 104)
	require.NoError(t, err)
	assert.Len(t, docIDsByCol[c.Collections().BlockCollection()], 5)
	assert.Len(t, docIDsByCol[btc.DefaultCollectionTransaction], 5)
	assert.Len(t, docIDsByCol[btc.DefaultCollectionTxInput], 5)
	assert.Len(t, docIDsByCol[btc.DefaultCollectionTxOutput], 5)
	assert.Len(t, docIDsByCol[c.Collections().BlockSignatureCollection()], 5,
		"signatures ride the snapshot and stay queryable after the restore")
}

func TestBitcoinSnapshot_PurgedRanges(t *testing.T) {
	c := newBitcoinConverter(t)
	td := newBitcoinSnapshotDB(t, c)

	identCtx, _ := insertBitcoinHeightsWithIdentity(t, td, c, 100, 114)

	// Purge the ten lowest heights directly, the reclaim path a pruner run
	// takes, so the snapshotter sees a hole from its aligned window 100..104
	// through 109.
	purgeHeightRange(identCtx, t, td, c, 100, 109)

	lowest, err := c.GetLowestStoredBlockNumber(context.Background(), td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(110), lowest)

	snapshotDir := t.TempDir()
	s := New(&config.SnapshotConfig{Dir: snapshotDir, BlocksPerFile: 5}, td.Node, c)
	s.ctx = identCtx

	require.NoError(t, s.checkAndSnapshot(identCtx))
	assert.Equal(t, int64(114), s.lastSnapshotBlock)

	infos := s.ListSnapshots()
	require.Len(t, infos, 1)
	assert.Equal(t, int64(110), infos[0].StartBlock)
	assert.Equal(t, int64(114), infos[0].EndBlock)

	_, err = os.Stat(filepath.Join(snapshotDir, "snapshot_110_114.kvsnap.gz"))
	require.NoError(t, err, "the retained aligned range must be snapshotted")
	assert.NoFileExists(t, filepath.Join(snapshotDir, "snapshot_100_104.kvsnap.gz"))

	sigs, err := QuerySnapshotSignatures(context.Background(), td.Node, c.Collections().SnapshotSignatureCollection())
	require.NoError(t, err)
	assert.Len(t, sigs, 1)
}
