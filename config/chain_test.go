package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testChain names a chain that validateConfig accepts.
var testChain = ChainConfig{Name: "Ethereum", Network: "Mainnet", ChainID: 1, Adapter: DefaultChainAdapter}

func TestLoadConfig_ChainRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc    string
		chain   string
		wantErr error
	}{
		{desc: "no chain section", chain: "", wantErr: ErrChainUnset},
		{desc: "no network", chain: "chain:\n  name: \"Ethereum\"\n  chain_id: 1\n", wantErr: ErrChainUnset},
		{desc: "no name", chain: "chain:\n  network: \"Mainnet\"\n  chain_id: 1\n", wantErr: ErrChainUnset},
		{desc: "no chain id", chain: "chain:\n  name: \"Ethereum\"\n  network: \"Mainnet\"\n", wantErr: ErrChainIDUnset},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			t.Parallel()
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(c.chain+"defradb:\n  embedded: true\n"), 0o600))

			_, err := LoadConfig(configPath)
			require.ErrorIs(t, err, c.wantErr)
		})
	}
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
