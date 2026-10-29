package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// neutralizeTronEnv pins the TRON_* variables to empty strings so ambient shell
// or .env values cannot leak into a test. godotenv does not override variables
// that are already set, and applyEnvOverrides skips empty values.
func neutralizeTronEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TRON_RPC_URL", "")
	t.Setenv("TRON_SOLIDITY_RPC_URL", "")
	t.Setenv("TRON_API_KEY", "")
	t.Setenv("TRON_DIAL_TIMEOUT_SECONDS", "")
}

func TestValidateConfig_Tron(t *testing.T) {
	t.Parallel()
	// One table for the tron validation contract: a complete tron config
	// passes, a missing or whitespace-only node URL reports the offending
	// field, and the evm adapter keeps passing with an entirely empty tron
	// section (the section stays inert outside the tron adapter).
	tests := []struct {
		name    string
		adapter string
		nodeURL string
		wantErr string
	}{
		{"tron with node url", TronChainAdapter, "https://api.trongrid.io", ""},
		{"tron without node url", TronChainAdapter, "", "tron.node_url"},
		{"tron with whitespace-only node url", TronChainAdapter, "   ", "tron.node_url"},
		{"evm with empty tron section", DefaultChainAdapter, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			cfg.DefraDB.Embedded = true
			cfg.Chain.Adapter = tt.adapter
			cfg.Tron.NodeURL = tt.nodeURL
			cfg.Indexer.SchemaAuthMode = constants.SchemaAuthModeToken

			err := validateConfig(cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestApplyDefaults_TronDialTimeoutSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"unset", 0, DefaultTronDialTimeoutSeconds},
		{"negative", -5, DefaultTronDialTimeoutSeconds},
		{"preset preserved", 42, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Tron: TronConfig{DialTimeoutSeconds: tt.input}}
			applyDefaults(cfg)
			assert.Equal(t, tt.want, cfg.Tron.DialTimeoutSeconds, "Tron.DialTimeoutSeconds")
		})
	}
}

func TestApplyEnvOverrides_TronConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("TRON_RPC_URL", "https://api.trongrid.io")
	t.Setenv("TRON_SOLIDITY_RPC_URL", "https://solidity.tron.example.test")
	t.Setenv("TRON_API_KEY", "key-123")
	t.Setenv("TRON_DIAL_TIMEOUT_SECONDS", "30")
	require.NoError(t, applyEnvOverrides(cfg))

	assert.Equal(t, "https://api.trongrid.io", cfg.Tron.NodeURL, "Tron.NodeURL")
	assert.Equal(t, "https://solidity.tron.example.test", cfg.Tron.SolidityURL, "Tron.SolidityURL")
	assert.Equal(t, "key-123", cfg.Tron.APIKey, "Tron.APIKey")
	assert.Equal(t, 30, cfg.Tron.DialTimeoutSeconds, "Tron.DialTimeoutSeconds")
}

func TestApplyEnvOverrides_TronInvalidTimeoutIgnored(t *testing.T) {
	cfg := &Config{Tron: TronConfig{DialTimeoutSeconds: 7}}
	t.Setenv("TRON_DIAL_TIMEOUT_SECONDS", "not_a_number")
	require.NoError(t, applyEnvOverrides(cfg))

	assert.Equal(t, 7, cfg.Tron.DialTimeoutSeconds, "invalid env value should be ignored, preset preserved")
}

func TestLoadConfig_TronAdapter(t *testing.T) {
	neutralizeTronEnv(t)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config-tron.yaml")

	configContent := `
chain:
  name: "Tron"
  network: "Mainnet"
  adapter: "tron"
tron:
  node_url: "https://api.trongrid.io"
defradb:
  embedded: true
`

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "tron", cfg.Chain.Adapter, "Chain.Adapter")
	assert.Equal(t, "https://api.trongrid.io", cfg.Tron.NodeURL, "Tron.NodeURL")
	assert.Equal(t, DefaultTronDialTimeoutSeconds, cfg.Tron.DialTimeoutSeconds, "Tron.DialTimeoutSeconds default")

	// A tron config that omits chain.name must default to Tron, not
	// Ethereum: otherwise collections derive the Ethereum__Mainnet prefix
	// from the tron adapter's own SDL.
	namelessPath := filepath.Join(tempDir, "config-tron-noname.yaml")
	namelessContent := `
chain:
  network: "Mainnet"
  adapter: "tron"
tron:
  node_url: "https://api.trongrid.io"
defradb:
  embedded: true
`
	require.NoError(t, os.WriteFile(namelessPath, []byte(namelessContent), 0o600))

	nameless, err := LoadConfig(namelessPath)
	require.NoError(t, err)
	assert.Equal(t, "Tron", nameless.Chain.Name, "Chain.Name tron default")
	assert.Equal(t, "Mainnet", nameless.Chain.Network, "Chain.Network default")
	assert.Equal(t, "tron", nameless.Chain.Adapter, "Chain.Adapter")
}

func TestLoadConfig_RepositoryTronConfigFile(t *testing.T) {
	neutralizeTronEnv(t)

	// Guards the shipped deployment file: it must keep parsing and passing
	// validateConfig as the config schema evolves. The tron URLs use ${VAR}
	// placeholders; the loader does NOT expand them, so they load as literal
	// strings and deployment (docker-compose, envsubst) resolves them.
	cfg, err := LoadConfig("config-tron.yml")
	require.NoError(t, err)
	assert.Equal(t, "tron", cfg.Chain.Adapter, "Chain.Adapter")
	assert.Equal(t, "${TRON_RPC_URL}", cfg.Tron.NodeURL, "Tron.NodeURL placeholder")
	assert.Equal(t, "${TRON_SOLIDITY_RPC_URL}", cfg.Tron.SolidityURL, "Tron.SolidityURL placeholder")
	assert.Equal(t, DefaultTronDialTimeoutSeconds, cfg.Tron.DialTimeoutSeconds, "Tron.DialTimeoutSeconds")
	assert.Equal(t, 20, cfg.Indexer.BlocksPerMinute, "Indexer.BlocksPerMinute")
}
