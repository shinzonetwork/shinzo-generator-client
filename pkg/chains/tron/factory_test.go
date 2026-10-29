package tron

import (
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFactory_RegisteredDispatch pins the registration side effect: with
// adapter "tron", the generic chains dispatch resolves to this package's
// factories.
func TestFactory_RegisteredDispatch(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://api.trongrid.io"},
	}

	fetcher, err := chains.NewFetcher(cfg)
	require.NoError(t, err)
	require.NotNil(t, fetcher)
	require.NoError(t, fetcher.Close())

	converter, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	require.NotNil(t, converter)
	assert.Equal(t, DefaultCollectionBlock, converter.GetCollections()[0])

	collections, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	assert.Equal(t, DefaultCollectionPrefix, collections.Prefix())
}

// TestFactory_ExplicitChainPrefix covers the dynamic prefix flow: chain
// name/network feed the collection names through the factory.
func TestFactory_ExplicitChainPrefix(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Nile", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://nile.trongrid.io"},
	}

	collections, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Tron__Nile", collections.Prefix())
}

// TestFactory_UnregisteredAdapter pins the miss path: dispatching an
// adapter nobody registered in this binary fails explicitly.
func TestFactory_UnregisteredAdapter(t *testing.T) {
	t.Parallel()
	_, err := chains.NewFetcher(&config.Config{Chain: config.ChainConfig{Adapter: "solana"}})
	require.Error(t, err)
	require.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)
}

// TestConverter_NilConfigDefaults covers the nil-config fallback contract:
// prefix derivation defaults to the Tron default collection prefix.
func TestConverter_NilConfigDefaults(t *testing.T) {
	t.Parallel()
	converter := NewConverter(nil)
	assert.Equal(t, DefaultCollectionPrefix, converter.Collections().Prefix())
}
