package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCosmosConfig writes a minimal cosmos-evm config with the given extra
// YAML (geth and cosmos sections) and returns its path.
func writeCosmosConfig(t *testing.T, sections string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "chain:\n  name: \"Kava\"\n  network: \"Mainnet\"\n  adapter: \"cosmos-evm\"\n  chain_id: 2222\ndefradb:\n  embedded: true\n" + sections
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	return path
}

// clearCosmosEnv isolates a test from GETH_*/COSMOS_* in the developer's
// environment and .env: an empty value counts as unset in the overrides.
func clearCosmosEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GETH_RPC_URL", "GETH_WS_URL", "COSMOS_HOST", "COSMOS_RPC_URL", "COSMOS_LCD_URL",
		"COSMOS_REQUEST_TIMEOUT_SECONDS", "CHAIN_NAME", "CHAIN_NETWORK", "CHAIN_ID", "CHAIN_ADAPTER", "SCHEMA_AUTH_MODE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadConfig_CosmosHostDerivesEndpoints(t *testing.T) {
	clearCosmosEnv(t)
	cfg, err := LoadConfig(writeCosmosConfig(t, "cosmos:\n  host: \"node.example\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "http://node.example:8545", cfg.Geth.NodeURL)
	assert.Equal(t, "http://node.example:26657", cfg.Cosmos.RPCURL)
	assert.Equal(t, "http://node.example:1317", cfg.Cosmos.LCDURL)
}

func TestLoadConfig_CosmosExplicitURLsWinOverHost(t *testing.T) {
	clearCosmosEnv(t)
	cfg, err := LoadConfig(writeCosmosConfig(t,
		"geth:\n  node_url: \"https://evm.example\"\ncosmos:\n  host: \"node.example\"\n  rpc_url: \"https://rpc.example\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "https://evm.example", cfg.Geth.NodeURL)
	assert.Equal(t, "https://rpc.example", cfg.Cosmos.RPCURL)
	assert.Equal(t, "http://node.example:1317", cfg.Cosmos.LCDURL, "only the empty endpoint is derived")
}

func TestLoadConfig_CosmosEnvOverrides(t *testing.T) {
	clearCosmosEnv(t)
	t.Setenv("COSMOS_RPC_URL", "https://rpc.env")
	t.Setenv("COSMOS_LCD_URL", "https://lcd.env")
	t.Setenv("GETH_RPC_URL", "https://evm.env")
	t.Setenv("COSMOS_REQUEST_TIMEOUT_SECONDS", "7")
	cfg, err := LoadConfig(writeCosmosConfig(t, ""))
	require.NoError(t, err)
	assert.Equal(t, "https://rpc.env", cfg.Cosmos.RPCURL)
	assert.Equal(t, "https://lcd.env", cfg.Cosmos.LCDURL)
	assert.Equal(t, "https://evm.env", cfg.Geth.NodeURL)
	assert.Equal(t, 7, cfg.Cosmos.RequestTimeoutSeconds)
}

func TestLoadConfig_CosmosValidation(t *testing.T) {
	tests := []struct {
		name     string
		sections string
		wantErr  string
	}{
		{"no native endpoints", "geth:\n  node_url: \"https://evm.example\"\n", "cosmos.rpc_url and cosmos.lcd_url"},
		{"lcd missing", "geth:\n  node_url: \"https://evm.example\"\ncosmos:\n  rpc_url: \"https://rpc.example\"\n", "cosmos.rpc_url and cosmos.lcd_url"},
		{"no evm endpoint", "cosmos:\n  rpc_url: \"https://rpc.example\"\n  lcd_url: \"https://lcd.example\"\n", "geth node_url/ws_url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCosmosEnv(t)
			_, err := LoadConfig(writeCosmosConfig(t, tt.sections))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestLoadConfig_CosmosRequiresChainID(t *testing.T) {
	clearCosmosEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("chain:\n  name: \"Kava\"\n  network: \"Mainnet\"\n  adapter: \"cosmos-evm\"\ncosmos:\n  host: \"node.example\"\n"), 0o600))
	_, err := LoadConfig(path)
	require.ErrorIs(t, err, ErrChainIDUnset)
}

func TestLoadConfig_ShippedCosmosConfigs(t *testing.T) {
	for _, tc := range []struct {
		file, prefix string
		chainID      uint64
	}{
		{"config_shinzohub.yaml", "Shinzohub__Testnet", 91273001},
		{"config_kava.yaml", "Kava__Mainnet", 2222},
	} {
		t.Run(tc.file, func(t *testing.T) {
			clearCosmosEnv(t)
			cfg, err := LoadConfig(tc.file)
			require.NoError(t, err)
			assert.Equal(t, CosmosEVMChainAdapter, cfg.Chain.Adapter)
			assert.Equal(t, tc.prefix, cfg.Chain.Name+"__"+cfg.Chain.Network)
			assert.Equal(t, tc.chainID, cfg.Chain.ChainID)
			assert.NotEmpty(t, cfg.Geth.NodeURL)
			assert.NotEmpty(t, cfg.Cosmos.RPCURL)
			assert.NotEmpty(t, cfg.Cosmos.LCDURL)
		})
	}
}
