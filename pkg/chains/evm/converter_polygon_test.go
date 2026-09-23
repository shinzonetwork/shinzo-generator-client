package evm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

func polygonConfig() *config.Config {
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{Name: "Polygon", Network: "Mainnet"}
	return cfg
}

// bundleWithTxs builds a BlockBundle whose Transactions slice points into the
// block's txs, mirroring what the fetcher produces.
func bundleWithTxs(num int64, txs ...Transaction) *BlockBundle {
	block := fakeBlockWithTxs(num, txs...)
	txPtrs := make([]*Transaction, len(block.Transactions))
	for i := range block.Transactions {
		txPtrs[i] = &block.Transactions[i]
	}
	return &BlockBundle{Block: block, Transactions: txPtrs}
}

// txDocByHash finds a transaction doc in the conversion result groups.
func txDocByHash(t *testing.T, resultGroupsTxDocs []map[string]any, hash string) map[string]any {
	t.Helper()
	for _, d := range resultGroupsTxDocs {
		if d["hash"] == hash {
			return d
		}
	}
	t.Fatalf("tx doc %s not found", hash)
	return nil
}

func TestConvert_Polygon_BlockDocStripsTotalDifficulty(t *testing.T) {
	t.Parallel()
	c := NewConverter(polygonConfig())

	result, err := c.Convert(context.Background(), &BlockBundle{Block: fakeBlock(100)})
	require.NoError(t, err)
	require.NotEmpty(t, result.Groups)

	blockDoc := result.Groups[0].Docs[0]
	assert.Equal(t, "Polygon__Mainnet__Block", result.Groups[0].Collection)
	_, hasTD := blockDoc["totalDifficulty"]
	assert.False(t, hasTD, "Polygon block doc must not contain totalDifficulty")
	// Real fields still present.
	assert.Contains(t, blockDoc, "hash")
	assert.Contains(t, blockDoc, "number")
	assert.Contains(t, blockDoc, "gasUsed")
}

func TestConvert_Polygon_TxDocShape(t *testing.T) {
	t.Parallel()
	c := NewConverter(polygonConfig())

	typed := fakeTx("0xaaa")
	typed.Type = "2"
	typed.V = "27"
	legacy := fakeTx("0xbbb")
	legacy.Type = "0"
	legacy.V = "27"
	deposit := fakeTx("0xccc")
	deposit.Type = "127" // Bor state-sync deposit (0x7f)
	deposit.V = "0"
	deposit.R = "0"
	deposit.S = "0"
	deposit.From = ZeroAddress

	bundle := bundleWithTxs(100, typed, legacy, deposit)
	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)

	var txDocs []map[string]any
	for _, g := range result.Groups {
		if g.Collection == "Polygon__Mainnet__Transaction" {
			txDocs = g.Docs
		}
	}
	require.Len(t, txDocs, 3)

	for _, d := range txDocs {
		for _, stripped := range []string{"status", "cumulativeGasUsed", "effectiveGasPrice", "gasUsed", "totalDifficulty"} {
			assert.NotContains(t, d, stripped)
		}
		assert.Contains(t, d, "yParity")
	}

	assert.Equal(t, "0", txDocByHash(t, txDocs, "0xaaa")["yParity"])
	assert.Equal(t, "", txDocByHash(t, txDocs, "0xbbb")["yParity"])
	assert.Equal(t, "0", txDocByHash(t, txDocs, "0xccc")["yParity"])
	assert.Equal(t, ZeroAddress, txDocByHash(t, txDocs, "0xccc")["from"])
}

func TestConvert_Polygon_SchemaIsVariantShape(t *testing.T) {
	t.Parallel()
	c := NewConverter(polygonConfig())

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "Polygon__Mainnet__Block")
	assert.Contains(t, sdl, "yParity: String")
	assert.NotContains(t, sdl, "totalDifficulty")
	assert.NotContains(t, sdl, "cumulativeGasUsed")
	assert.NotContains(t, sdl, "effectiveGasPrice")

	cols := c.GetCollections()
	require.Len(t, cols, 6)
	assert.Equal(t, "Polygon__Mainnet__Block", cols[0])
	assert.Equal(t, "Polygon__Mainnet__Transaction", cols[3])
}
