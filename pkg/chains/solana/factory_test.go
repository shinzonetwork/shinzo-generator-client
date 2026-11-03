package solana

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

func TestFactory_Dispatch(t *testing.T) {
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

	fetcher, err := chains.NewFetcher(cfg)
	require.NoError(t, err)
	assert.IsType(t, &Fetcher{}, fetcher)

	converter, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	assert.IsType(t, &Converter{}, converter)
	assert.Equal(t, "Solana__Devnet", converter.Collections().Prefix(),
		"the converter factory must derive the prefix from the chain config")

	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Solana__Devnet", cols.Prefix())
	assert.Equal(t, "Solana__Devnet__Block", cols.BlockCollection())
}

// TestFactory_NilConfigDefaultsToEVM pins the registry-level nil-config
// contract: chains.NewFetcher(nil) resolves the default adapter ("evm"),
// which is not registered by this package, so dispatch fails with the
// not-registered sentinel rather than silently building a solana fetcher.
func TestFactory_NilConfigDefaultsToEVM(t *testing.T) {
	t.Parallel()

	_, err := chains.NewFetcher(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)
	assert.ErrorContains(t, err, "evm", "nil and empty adapters resolve to the default adapter name")
}

func TestFactory_Unregister(t *testing.T) {
	// Not parallel: mutating the shared registry must not race other tests.
	chains.UnregisterChainForTest(config.SolanaAdapter)
	t.Cleanup(func() {
		chains.RegisterChain(config.SolanaAdapter, chains.ChainFactories{
			Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
				return NewFetcherFromConfig(cfg)
			},
			Converter: func(cfg *config.Config) (chains.Converter, error) {
				return NewConverter(cfg), nil
			},
			Collections: func(cfg *config.Config) (chains.Collections, error) {
				return NewCollectionNames(chainPrefixFromConfig(cfg)), nil
			},
		})
	})

	cfg := &config.Config{Chain: config.ChainConfig{Adapter: config.SolanaAdapter}}

	_, err := chains.NewFetcher(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	_, err = chains.NewConverter(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	_, err = chains.NewCollections(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)
}
