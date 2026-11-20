package bitcoin

// These tests dispatch through the Factories() bundle exactly the way the
// cmd/block_poster/bitcoin binary does, pinning the bundle's construction and
// the defaulting behaviour the factories share.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

func TestFactories_SignetPrefix(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Chain: config.ChainConfig{
		Adapter: config.BitcoinAdapter,
		Name:    "Bitcoin",
		Network: "Signet",
	}}
	factories := Factories()
	require.NotNil(t, factories.Fetcher)
	require.NotNil(t, factories.Converter)
	require.NotNil(t, factories.Collections)

	f, err := factories.Fetcher(cfg)
	require.NoError(t, err)
	_, ok := f.(*Fetcher)
	require.True(t, ok, "Fetcher must construct the bitcoin *Fetcher")

	c, err := factories.Converter(cfg)
	require.NoError(t, err)
	bc, ok := c.(*Converter)
	require.True(t, ok, "Converter must construct the bitcoin *Converter")
	assert.Len(t, bc.GetCollections(), 6)

	cols, err := factories.Collections(cfg)
	require.NoError(t, err)
	colNames, ok := cols.(*CollectionNames)
	require.True(t, ok, "Collections must construct the bitcoin *CollectionNames")
	assert.Equal(t, "Bitcoin__Signet", colNames.Prefix())
	assert.Equal(t, "Bitcoin__Signet__Block", colNames.BlockCollection())
}

// TestFactories_DefaultPrefix pins the bundle's dispatch fallback: a nil or
// unset config resolves the default Bitcoin__Mainnet prefix, the same
// fallback chainPrefixFromConfig applies for NewConverter.
func TestFactories_DefaultPrefix(t *testing.T) {
	t.Parallel()

	factories := Factories()

	c, err := factories.Converter(nil)
	require.NoError(t, err)
	bc, ok := c.(*Converter)
	require.True(t, ok, "Converter must construct the bitcoin *Converter")
	assert.Equal(t, DefaultCollectionPrefix, bc.Collections().Prefix())

	cols, err := factories.Collections(nil)
	require.NoError(t, err)
	colNames, ok := cols.(*CollectionNames)
	require.True(t, ok, "Collections must construct the bitcoin *CollectionNames")
	assert.Equal(t, DefaultCollectionPrefix, colNames.Prefix())

	cfg := &config.Config{Chain: config.ChainConfig{
		Adapter: config.BitcoinAdapter,
		Name:    "Bitcoin",
	}}
	cols, err = factories.Collections(cfg)
	require.NoError(t, err)
	assert.Equal(t, DefaultCollectionPrefix, cols.Prefix())
}
