package bitcoin

// The tests here dispatch through the chains factory registry exactly the way
// the binaries do. They must keep pkg/chains/evm unlinked: importing the evm
// adapter from this test would register its factories and break the
// nil-config test's expectation that only the bitcoin family is registered
// in this test binary.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

func TestFactory_SignetPrefix(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Chain: config.ChainConfig{
		Adapter: config.BitcoinAdapter,
		Name:    "Bitcoin",
		Network: "Signet",
	}}

	f, err := chains.NewFetcher(cfg)
	require.NoError(t, err)
	_, ok := f.(*Fetcher)
	require.True(t, ok, "NewFetcher must return the bitcoin *Fetcher")

	c, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	bc, ok := c.(*Converter)
	require.True(t, ok, "NewConverter must return the bitcoin *Converter")
	assert.Len(t, bc.GetCollections(), 6)

	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	colNames, ok := cols.(*CollectionNames)
	require.True(t, ok, "NewCollections must return the bitcoin *CollectionNames")
	assert.Equal(t, "Bitcoin__Signet", colNames.Prefix())
	assert.Equal(t, "Bitcoin__Signet__Block", colNames.BlockCollection())
}

func TestFactory_DefaultPrefix(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Chain: config.ChainConfig{
		Adapter: config.BitcoinAdapter,
		Name:    "Bitcoin",
	}}

	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	colNames, ok := cols.(*CollectionNames)
	require.True(t, ok, "NewCollections must return the bitcoin *CollectionNames")
	assert.Equal(t, DefaultCollectionPrefix, colNames.Prefix())
}

// TestFactory_NilConfig pins the dispatch fallback: a nil config resolves the
// default evm adapter, whose factories are not linked into this test binary,
// so all three constructors fail with ErrChainFactoryNotRegistered instead of
// constructing anything.
func TestFactory_NilConfig(t *testing.T) {
	t.Parallel()

	_, err := chains.NewFetcher(nil)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	_, err = chains.NewConverter(nil)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	_, err = chains.NewCollections(nil)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)
}

// TestFactory_UnregisterRestore removes the bitcoin factories from the shared
// registry and verifies that every constructor reports
// ErrChainFactoryNotRegistered, then restores the full registration. It
// mutates the shared registry, so it stays sequential (no t.Parallel) and
// always restores via t.Cleanup, even on failure.
func TestFactory_UnregisterRestore(t *testing.T) {
	cfg := &config.Config{Chain: config.ChainConfig{
		Adapter: config.BitcoinAdapter,
		Name:    "Bitcoin",
		Network: "Signet",
	}}

	chains.UnregisterChainForTest(config.BitcoinAdapter)
	t.Cleanup(registerChainFactories)

	_, err := chains.NewFetcher(cfg)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	_, err = chains.NewConverter(cfg)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	_, err = chains.NewCollections(cfg)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)

	registerChainFactories()

	f, err := chains.NewFetcher(cfg)
	require.NoError(t, err)
	_, ok := f.(*Fetcher)
	require.True(t, ok, "restored Fetcher must dispatch again")

	_, err = chains.NewConverter(cfg)
	require.NoError(t, err)

	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Bitcoin__Signet", cols.Prefix())
}
