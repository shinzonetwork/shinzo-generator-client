package chains_test

import (
	"fmt"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCollections_NilConfig_DefaultsToEVM(t *testing.T) {
	t.Parallel()

	c, err := chains.NewCollections(nil)
	require.NoError(t, err)
	require.NotNil(t, c)

	assert.Equal(t, evm.DefaultCollectionPrefix, c.Prefix())
	assert.Equal(t, evm.DefaultCollections(), c.AllCollections())
}

func TestPolygonFactoriesUseSameVariant(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Polygon",
			Network: "Mainnet",
			Adapter: config.DefaultChainAdapter,
		},
	}
	converter, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	collections, err := chains.NewCollections(cfg)
	require.NoError(t, err)

	assert.Equal(t, collections.Prefix(), converter.Collections().Prefix())
	assert.Equal(t, collections.AllCollections(), converter.GetCollections())
	sdl, err := converter.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "type Polygon__Mainnet__Transaction")
	assert.Contains(t, sdl, "yParity: String")
}

func TestUnregisteredAdapter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Adapter: "nonexistent",
		},
	}

	tests := []struct {
		name string
		fn   func(*config.Config) (any, error)
	}{
		{"NewFetcher", func(cfg *config.Config) (any, error) { return chains.NewFetcher(cfg) }},
		{"NewConverter", func(cfg *config.Config) (any, error) { return chains.NewConverter(cfg) }},
		{"NewCollections", func(cfg *config.Config) (any, error) { return chains.NewCollections(cfg) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.fn(cfg)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)
			assert.Contains(t, err.Error(), "nonexistent")
		})
	}
}

func TestRegisterChain_IncompletePanics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		factories chains.ChainFactories
	}{
		{
			"all nil",
			chains.ChainFactories{},
		},
		{
			"only fetcher",
			chains.ChainFactories{
				Fetcher: func(_ *config.Config) (chains.Fetcher, error) { return nil, nil }, //nolint:nilnil //Valid value for testing purposes
			},
		},
		{
			"missing collections",
			chains.ChainFactories{
				Fetcher:   func(_ *config.Config) (chains.Fetcher, error) { return nil, nil },   //nolint:nilnil //Valid value for testing purposes
				Converter: func(_ *config.Config) (chains.Converter, error) { return nil, nil }, //nolint:nilnil //Valid value for testing purposes
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.PanicsWithValue(t, fmt.Sprintf("RegisterChain(%q): all factory fields must be non-nil", tt.name), func() {
				chains.RegisterChain(tt.name, tt.factories)
			})
		})
	}
}
