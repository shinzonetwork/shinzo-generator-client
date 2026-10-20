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
