package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// neutralizeSolanaEnv pins the Solana and chain environment variables to empty
// so ambient environment values cannot leak into a test result. t.Setenv with
// an empty value is enough: applyChainEnvOverrides ignores empty variables.
func neutralizeSolanaEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SOLANA_RPC_URL",
		"SOLANA_WS_URL",
		"SOLANA_API_KEY",
		"SOLANA_API_KEY_TYPE",
		"SOLANA_ARCHIVE_RPC_URL",
		"SOLANA_COMMITMENT",
		"SOLANA_DIAL_TIMEOUT_SECONDS",
		"CHAIN_NAME",
		"CHAIN_NETWORK",
		"CHAIN_ADAPTER",
	} {
		t.Setenv(name, "")
	}
}

func solanaTestConfig() *Config {
	cfg := &Config{}
	cfg.DefraDB.Embedded = true
	cfg.Chain.Adapter = SolanaAdapter
	cfg.Indexer.SchemaAuthMode = "token"
	return cfg
}

func TestValidateConfig_Solana(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		arrange     func(*Config)
		shouldError bool
		errContains string
	}{
		{"valid confirmed", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
		}, false, ""},
		{"valid finalized", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = "finalized"
		}, false, ""},
		{"missing rpc_url", func(c *Config) {
			c.Solana.NodeURL = ""
			c.Solana.Commitment = DefaultSolanaCommitment
		}, true, "solana.rpc_url"},
		{"whitespace rpc_url", func(c *Config) {
			c.Solana.NodeURL = "   "
			c.Solana.Commitment = DefaultSolanaCommitment
		}, true, "solana.rpc_url"},
		{"commitment processed rejected", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = "processed"
		}, true, "invalid solana.commitment"},
		{"commitment empty rejected", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = ""
		}, true, "invalid solana.commitment"},
		{"commitment wrong case rejected", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = "Confirmed"
		}, true, "invalid solana.commitment"},
		{"ws_url empty ok", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = ""
		}, false, ""},
		{"ws_url ws ok", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = "ws://localhost:8900"
		}, false, ""},
		{"ws_url wss ok", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = "wss://api.example.com"
		}, false, ""},
		{"ws_url http rejected", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = "http://localhost:8900"
		}, true, "invalid solana.ws_url"},
		{"ws_url https rejected", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = "https://api.example.com"
		}, true, "invalid solana.ws_url"},
		{"ws_url no scheme rejected", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = "localhost:8900"
		}, true, "invalid solana.ws_url"},
		{"ws_url unexpanded placeholder exempt", func(c *Config) {
			c.Solana.NodeURL = "http://localhost:8899"
			c.Solana.Commitment = DefaultSolanaCommitment
			c.Solana.WsURL = "${SOLANA_WS_URL}"
		}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := solanaTestConfig()
			tt.arrange(cfg)
			err := validateConfig(cfg)
			if tt.shouldError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestApplyDefaults_Solana(t *testing.T) {
	t.Parallel()
	t.Run("adapter default stays evm with Ethereum name", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		applyDefaults(cfg)
		assert.Equal(t, DefaultChainAdapter, cfg.Chain.Adapter)
		assert.Equal(t, "Ethereum", cfg.Chain.Name)
	})
	t.Run("nameless solana config defaults to Solana", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		cfg.Chain.Adapter = SolanaAdapter
		applyDefaults(cfg)
		assert.Equal(t, "Solana", cfg.Chain.Name)
		assert.Equal(t, "Mainnet", cfg.Chain.Network)
		assert.Equal(t, DefaultSolanaCommitment, cfg.Solana.Commitment)
		assert.Equal(t, DefaultSolanaMaxSupportedTxVersion, cfg.Solana.MaxSupportedTransactionVersion)
		assert.Equal(t, DefaultSolanaDialTimeoutSeconds, cfg.Solana.DialTimeoutSeconds)
	})
	t.Run("explicit solana values preserved", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		cfg.Chain.Adapter = SolanaAdapter
		cfg.Chain.Name = "Solana"
		cfg.Solana.Commitment = "finalized"
		cfg.Solana.MaxSupportedTransactionVersion = 2
		cfg.Solana.DialTimeoutSeconds = 42
		applyDefaults(cfg)
		assert.Equal(t, "finalized", cfg.Solana.Commitment)
		assert.Equal(t, 2, cfg.Solana.MaxSupportedTransactionVersion)
		assert.Equal(t, 42, cfg.Solana.DialTimeoutSeconds)
	})
	t.Run("negative solana values fall back to defaults", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		cfg.Chain.Adapter = SolanaAdapter
		cfg.Solana.Commitment = ""
		cfg.Solana.MaxSupportedTransactionVersion = -1
		cfg.Solana.DialTimeoutSeconds = 0
		applyDefaults(cfg)
		assert.Equal(t, DefaultSolanaCommitment, cfg.Solana.Commitment)
		assert.Equal(t, DefaultSolanaMaxSupportedTxVersion, cfg.Solana.MaxSupportedTransactionVersion)
		assert.Equal(t, DefaultSolanaDialTimeoutSeconds, cfg.Solana.DialTimeoutSeconds)
	})
}

func TestSolanaConfigRewardsEnabled(t *testing.T) {
	t.Parallel()
	t.Run("nil rewards means enabled", func(t *testing.T) {
		t.Parallel()
		cfg := &SolanaConfig{}
		assert.True(t, cfg.RewardsEnabled())
	})
	t.Run("rewards false disabled", func(t *testing.T) {
		t.Parallel()
		disabled := false
		cfg := &SolanaConfig{Rewards: &disabled}
		assert.False(t, cfg.RewardsEnabled())
	})
	t.Run("rewards true enabled", func(t *testing.T) {
		t.Parallel()
		enabled := true
		cfg := &SolanaConfig{Rewards: &enabled}
		assert.True(t, cfg.RewardsEnabled())
	})
}

func TestSolanaEnvOverrides(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(*testing.T, *Config)
	}{
		{
			name: "all solana variables applied",
			env: map[string]string{
				"SOLANA_RPC_URL":              "http://rpc:8899",
				"SOLANA_WS_URL":               "wss://ws:8900",
				"SOLANA_API_KEY":              "key-123",
				"SOLANA_API_KEY_TYPE":         "X-Api-Key",
				"SOLANA_ARCHIVE_RPC_URL":      "https://archive.example.com",
				"SOLANA_COMMITMENT":           "finalized",
				"SOLANA_DIAL_TIMEOUT_SECONDS": "7",
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "http://rpc:8899", cfg.Solana.NodeURL)
				assert.Equal(t, "wss://ws:8900", cfg.Solana.WsURL)
				assert.Equal(t, "key-123", cfg.Solana.APIKey)
				assert.Equal(t, "X-Api-Key", cfg.Solana.APIKeyType)
				assert.Equal(t, "https://archive.example.com", cfg.Solana.ArchiveRPCURL)
				assert.Equal(t, "finalized", cfg.Solana.Commitment)
				assert.Equal(t, 7, cfg.Solana.DialTimeoutSeconds)
			},
		},
		{
			name: "invalid dial timeout ignored, preset preserved",
			env: map[string]string{
				"SOLANA_DIAL_TIMEOUT_SECONDS": "not-a-number",
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, 21, cfg.Solana.DialTimeoutSeconds)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SOLANA_RPC_URL", "")
			t.Setenv("SOLANA_WS_URL", "")
			t.Setenv("SOLANA_API_KEY", "")
			t.Setenv("SOLANA_API_KEY_TYPE", "")
			t.Setenv("SOLANA_ARCHIVE_RPC_URL", "")
			t.Setenv("SOLANA_COMMITMENT", "")
			t.Setenv("SOLANA_DIAL_TIMEOUT_SECONDS", "")
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			cfg := &Config{}
			cfg.Solana.DialTimeoutSeconds = 21
			require.NoError(t, applyEnvOverrides(cfg))
			tt.check(t, cfg)
		})
	}
}

func TestLoadConfig_Solana(t *testing.T) {
	neutralizeSolanaEnv(t)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config-solana.yaml")

	configContent := `
chain:
  adapter: "solana"
defradb:
  embedded: true
solana:
  rpc_url: "http://localhost:8899"
indexer:
  start_height: 0
`

	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, SolanaAdapter, cfg.Chain.Adapter)
	assert.Equal(t, "Solana", cfg.Chain.Name, "a nameless solana config must not default to Ethereum")
	assert.Equal(t, "http://localhost:8899", cfg.Solana.NodeURL)
	assert.Equal(t, DefaultSolanaCommitment, cfg.Solana.Commitment)
	assert.Equal(t, DefaultSolanaMaxSupportedTxVersion, cfg.Solana.MaxSupportedTransactionVersion)
	assert.Equal(t, DefaultSolanaDialTimeoutSeconds, cfg.Solana.DialTimeoutSeconds)
	assert.True(t, cfg.Solana.RewardsEnabled(), "nil rewards means enabled")
}

func TestLoadConfig_SolanaDeploymentFile_STILL_VALID(t *testing.T) {
	neutralizeSolanaEnv(t)
	// The file carries a literal ${SCHEMA_AUTH_MODE} placeholder, which the YAML
	// loader does not expand; the env pin supplies the value deployment would.
	t.Setenv("SCHEMA_AUTH_MODE", "token")

	cfg, err := LoadConfig("config-solana.yaml")
	require.NoError(t, err)

	assert.Equal(t, SolanaAdapter, cfg.Chain.Adapter)
	assert.Equal(t, "Solana", cfg.Chain.Name)
	assert.Equal(t, "Mainnet", cfg.Chain.Network)
	// The ${VAR} placeholders load as literal strings; a non-empty one satisfies
	// the rpc_url requirement and deployment envsubst resolves it later.
	assert.Equal(t, "${SOLANA_RPC_URL}", cfg.Solana.NodeURL)
	assert.Equal(t, "confirmed", cfg.Solana.Commitment)
	assert.Equal(t, 1, cfg.Solana.MaxSupportedTransactionVersion)
	assert.Equal(t, 10, cfg.Solana.DialTimeoutSeconds)
	assert.True(t, cfg.Solana.RewardsEnabled())
	assert.Equal(t, 60, cfg.Indexer.BlocksPerMinute)
}
