package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testChain names a chain that validateConfig accepts.
var testChain = ChainConfig{Name: "Ethereum", Network: "Mainnet", ChainID: 1, Adapter: DefaultChainAdapter}

func TestLoadConfig_ChainRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		chain   string
		wantErr error
	}{
		{name: "no chain section", chain: "", wantErr: ErrChainUnset},
		{name: "no network", chain: "chain:\n  name: \"Ethereum\"\n  chain_id: 1\n", wantErr: ErrChainUnset},
		{name: "no name", chain: "chain:\n  network: \"Mainnet\"\n  chain_id: 1\n", wantErr: ErrChainUnset},
		{name: "no chain id", chain: "chain:\n  name: \"Ethereum\"\n  network: \"Mainnet\"\n", wantErr: ErrChainIDUnset},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(tt.chain+"defradb:\n  embedded: true\n"), 0o600))

			_, err := LoadConfig(configPath)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestValidateChainNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		chain   string
		network string
		wantErr error
	}{
		{name: "capitalized words", chain: "Ethereum", network: "Mainnet"},
		{name: "lower-case first letter", chain: "Ethereum", network: "mainnet", wantErr: ErrInvalidChainName},
		{name: "digits after the first letter", chain: "Base2", network: "Sepolia1"},
		{name: "space", chain: "Arbitrum One", network: "Mainnet", wantErr: ErrInvalidChainName},
		{name: "hyphen", chain: "Ethereum", network: "Ethereum-Sepolia", wantErr: ErrInvalidChainName},
		{name: "underscore", chain: "Arbitrum_One", network: "Mainnet", wantErr: ErrInvalidChainName},
		{name: "starts with a digit", chain: "Ethereum", network: "1Mainnet", wantErr: ErrInvalidChainName},
		{name: "non-ASCII letter", chain: "Éthereum", network: "Mainnet", wantErr: ErrInvalidChainName},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, ValidateChainNames(tt.chain, tt.network), tt.wantErr)
		})
	}
}

func TestLoadConfig_InvalidChainName(t *testing.T) {
	t.Setenv("CHAIN_NAME", "Arbitrum One")
	_, err := LoadConfig("config.yaml")
	require.ErrorIs(t, err, ErrInvalidChainName)
}

func TestApplyChainEnvOverrides_ChainID(t *testing.T) {
	t.Setenv("CHAIN_ID", "11155111")
	cfg := &Config{Chain: testChain}
	require.NoError(t, applyChainEnvOverrides(cfg))
	require.Equal(t, uint64(11155111), cfg.Chain.ChainID)

	t.Setenv("CHAIN_ID", "sepolia")
	require.Error(t, applyChainEnvOverrides(cfg))
}

// The container image runs with the shipped config, so it must name a chain the generator accepts.
func TestLoadConfig_ShippedConfig(t *testing.T) {
	t.Setenv("SCHEMA_AUTH_MODE", "none")
	cfg, err := LoadConfig("config.yaml")
	require.NoError(t, err)
	require.Equal(t, uint64(1), cfg.Chain.ChainID)
}

// Each shipped per-chain config must load, name its chain, carry the chain ID
// the RPC is checked against, and keep its store (and the snapshots inside it)
// separate from every other chain's.
func TestLoadConfig_ShippedChainConfigs(t *testing.T) {
	// Env overrides win over the file; clear the ones a developer's .env may set.
	for _, k := range []string{"CHAIN_NAME", "CHAIN_NETWORK", "CHAIN_ID", "CHAIN_ADAPTER", "GETH_RPC_URL", "GETH_WS_URL", "DEFRADB_STORE_PATH"} {
		t.Setenv(k, "")
	}
	t.Setenv("SCHEMA_AUTH_MODE", "none")

	cases := []struct {
		file, name, network, store string
		chainID                    uint64
	}{
		{"config_bsc.yaml", "BSC", "Mainnet", "./.defra-bsc", 56},
		{"config_polygon.yaml", "Polygon", "Mainnet", "./.defra-polygon", 137},
		{"config_polygon_amoy.yaml", "Polygon", "Amoy", "./.defra-polygon-amoy", 80002},
		{"config_avalanche.yaml", "Avalanche", "Mainnet", "./.defra-avalanche", 43114},
		{"config_celo.yaml", "Celo", "Mainnet", "./.defra-celo", 42220},
		{"config_kaia.yaml", "Kaia", "Mainnet", "./.defra-kaia", 8217},
	}
	stores := map[string]string{}
	for _, tc := range cases {
		cfg, err := LoadConfig(tc.file)
		require.NoError(t, err, tc.file)
		assert.Equal(t, tc.name, cfg.Chain.Name, tc.file)
		assert.Equal(t, tc.network, cfg.Chain.Network, tc.file)
		assert.Equal(t, tc.chainID, cfg.Chain.ChainID, tc.file)
		assert.Equal(t, DefaultChainAdapter, cfg.Chain.Adapter, tc.file)
		assert.Equal(t, tc.store, cfg.DefraDB.Store.Path, tc.file)
		assert.Equal(t, tc.store+"/snapshots", cfg.Snapshot.Dir, "%s: snapshots live inside the chain's own store", tc.file)
		assert.NotEmpty(t, cfg.Geth.NodeURL, tc.file)
		assert.GreaterOrEqual(t, int64(cfg.Snapshot.MaxSnapshots)*cfg.Snapshot.BlocksPerFile, cfg.Pruner.MaxBlocks,
			"%s: snapshots must cover the pruner's retention window", tc.file)
		prev, dup := stores[tc.store]
		assert.False(t, dup, "%s shares its store with %s", tc.file, prev)
		stores[tc.store] = tc.file
	}
}
