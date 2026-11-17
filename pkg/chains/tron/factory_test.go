package tron

import (
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFactories_Dispatch pins the Factories() bundle: each closure resolves
// to this package's constructors, so the tron main can inject the bundle and
// every dispatch path uses the same three factories by construction.
func TestFactories_Dispatch(t *testing.T) {
	t.Parallel()
	factories := Factories()
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://api.trongrid.io"},
	}

	fetcher, err := factories.Fetcher(cfg)
	require.NoError(t, err)
	require.NotNil(t, fetcher)
	require.NoError(t, fetcher.Close())

	converter, err := factories.Converter(cfg)
	require.NoError(t, err)
	require.NotNil(t, converter)
	assert.Equal(t, DefaultCollectionBlock, converter.GetCollections()[0])

	collections, err := factories.Collections(cfg)
	require.NoError(t, err)
	assert.Equal(t, DefaultCollectionPrefix, collections.Prefix())
}

// TestFactories_ExplicitChainPrefix covers the dynamic prefix flow: chain
// name/network feed the collection names through the Factories() bundle.
func TestFactories_ExplicitChainPrefix(t *testing.T) {
	t.Parallel()
	factories := Factories()
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Nile", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://nile.trongrid.io"},
	}

	collections, err := factories.Collections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Tron__Nile", collections.Prefix())
}

// TestConverter_NilConfigDefaults covers the nil-config fallback contract:
// prefix derivation defaults to the Tron default collection prefix.
func TestConverter_NilConfigDefaults(t *testing.T) {
	t.Parallel()
	converter := NewConverter(nil)
	assert.Equal(t, DefaultCollectionPrefix, converter.Collections().Prefix())
}
