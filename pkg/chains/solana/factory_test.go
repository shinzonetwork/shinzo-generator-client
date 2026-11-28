package solana

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

func TestFactories_Dispatch(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: "Devnet",
			Adapter: config.SolanaAdapter,
		},
		Solana: config.SolanaConfig{
			NodeURL:                        "https://api.devnet.solana.com",
			Commitment:                     "confirmed",
			MaxSupportedTransactionVersion: 1,
		},
	}

	factories := Factories()

	fetcher, err := factories.Fetcher(cfg)
	require.NoError(t, err)
	assert.IsType(t, &Fetcher{}, fetcher)

	converter, err := factories.Converter(cfg)
	require.NoError(t, err)
	assert.IsType(t, &Converter{}, converter)
	assert.Equal(t, "Solana__Devnet", converter.Collections().Prefix(),
		"the converter factory must derive the prefix from the chain config")

	cols, err := factories.Collections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Solana__Devnet", cols.Prefix())
	assert.Equal(t, "Solana__Devnet__Block", cols.BlockCollection())
}

// TestFactories_NilConfigFallsBackToDefaults pins the bundle's nil-config
// contract: the Collections factory resolves the adapter's default prefix
// (Solana__Mainnet) instead of erroring, matching the converter's fallback
// behaviour for unconfigured name/network.
func TestFactories_NilConfigFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	factories := Factories()

	cols, err := factories.Collections(nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultCollectionPrefix, cols.Prefix())
	assert.Equal(t, DefaultCollectionBlock, cols.BlockCollection())
}
