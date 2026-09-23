package evm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ethereum regression lock: the Ethereum variant must keep emitting the
// legacy fields and must never emit yParity. The polygon variant must not
// have leaked onto this path.

func TestConvert_Ethereum_KeepsLegacyFields(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig()) // Ethereum

	tx := fakeTx("0xaaa")
	tx.Type = "2"
	tx.V = "27"
	bundle := bundleWithTxs(100, tx)

	result, err := c.Convert(context.Background(), bundle)
	require.NoError(t, err)

	blockDoc := result.Groups[0].Docs[0]
	assert.Contains(t, blockDoc, "totalDifficulty", "Ethereum block keeps totalDifficulty")

	var txDocs []map[string]any
	for _, g := range result.Groups {
		if g.Collection == "Ethereum__Mainnet__Transaction" {
			txDocs = g.Docs
		}
	}
	require.Len(t, txDocs, 1)
	txDoc := txDocs[0]
	assert.Contains(t, txDoc, "status")
	assert.Contains(t, txDoc, "cumulativeGasUsed")
	assert.Contains(t, txDoc, "effectiveGasPrice")
	assert.NotContains(t, txDoc, "yParity", "Ethereum tx doc must never contain yParity")
}

func TestConvert_Ethereum_DefaultForUnknownAndEmptyChains(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "Arbitrum", "Optimism"} {
		cfg := testConfig()
		cfg.Chain.Name = name
		c := NewConverter(cfg)

		result, err := c.Convert(context.Background(), bundleWithTxs(100, fakeTx("0xaaa")))
		require.NoError(t, err, "chain %q", name)

		var txDocs []map[string]any
		for _, g := range result.Groups {
			if g.Collection == c.collections.Transaction {
				txDocs = g.Docs
			}
		}
		require.Len(t, txDocs, 1, "chain %q", name)
		assert.NotContains(t, txDocs[0], "yParity", "chain %q must use the Ethereum shape", name)
		assert.Contains(t, txDocs[0], "status", "chain %q must use the Ethereum shape", name)
	}
}

func TestConverter_Ethereum_SchemaKeepsLegacyFields(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "totalDifficulty: String")
	assert.Contains(t, sdl, "cumulativeGasUsed: String")
	assert.Contains(t, sdl, "effectiveGasPrice: String")
	assert.NotContains(t, sdl, "yParity")
}
