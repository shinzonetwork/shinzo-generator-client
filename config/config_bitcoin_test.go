package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bitcoinEnvKeys lists the nine bitcoin adapter env overrides.
var bitcoinEnvKeys = []string{
	"BITCOIN_RPC_URL",
	"BITCOIN_RPC_USER",
	"BITCOIN_RPC_PASSWORD",
	"BITCOIN_COOKIE_PATH",
	"BITCOIN_API_KEY",
	"BITCOIN_API_KEY_TYPE",
	"BITCOIN_ARCHIVE_RPC_URL",
	"BITCOIN_CONFIRMATION_DEPTH",
	"BITCOIN_DIAL_TIMEOUT_SECONDS",
}

// bitcoinTestConfig returns a full Config that satisfies every bitcoin
// adapter rule: basic auth, a confirmation depth inside the approved range,
// and the schema-auth, DefraDB, and EVM chain-ID rules every adapter shares.
func bitcoinTestConfig() *Config {
	cfg := &Config{}
	cfg.Chain.Adapter = BitcoinAdapter
	cfg.Chain.Name = "Bitcoin"
	cfg.Chain.Network = "Mainnet"
	cfg.Chain.ChainID = 0
	cfg.DefraDB.Embedded = true
	cfg.Indexer.SchemaAuthMode = constants.SchemaAuthModeToken
	cfg.Bitcoin.RPCURL = "http://bitcoin-core:8332"
	cfg.Bitcoin.RPCUser = "shinzorpc"
	cfg.Bitcoin.RPCPassword = "shinzo-password"
	cfg.Bitcoin.ConfirmationDepth = 6
	cfg.Bitcoin.DialTimeoutSeconds = 10
	return cfg
}

// neutralizeBitcoinEnv pins the chain-identity, bitcoin, and geth env
// overrides to empty values: every override reader skips an empty value, and
// godotenv cannot inject from a .env file for a key already present in the
// process environment, so the fixture is the only input the config loader
// sees. The geth keys are pinned because the loader reads the repository's
// .env file on every call, and the bitcoin deployment assertions require the
// geth section to stay empty.
func neutralizeBitcoinEnv(t *testing.T) {
	t.Helper()

	for _, key := range append([]string{
		"CHAIN_ADAPTER", "CHAIN_NAME", "CHAIN_NETWORK",
		"GETH_RPC_URL", "GETH_WS_URL", "GETH_API_KEY", "GETH_API_KEY_TYPE", "GETH_DIAL_TIMEOUT_SECONDS",
	}, bitcoinEnvKeys...) {
		t.Setenv(key, "")
	}
}

func TestValidateConfig_Bitcoin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mutate      func(cfg *Config)
		shouldError bool
		errContains string
	}{
		{"user and password auth", func(_ *Config) {}, false, ""},
		{"cookie only auth", func(cfg *Config) {
			cfg.Bitcoin.RPCUser = ""
			cfg.Bitcoin.RPCPassword = ""
			cfg.Bitcoin.CookiePath = "/bitcoin/.cookie"
		}, false, ""},
		{"depth at upper bound", func(cfg *Config) {
			cfg.Bitcoin.ConfirmationDepth = maxBitcoinConfirmationDepth
		}, false, ""},
		{"empty rpc_url", func(cfg *Config) { cfg.Bitcoin.RPCURL = "" }, true, "rpc_url"},
		{"whitespace rpc_url", func(cfg *Config) { cfg.Bitcoin.RPCURL = "   " }, true, "rpc_url"},
		{"no auth", func(cfg *Config) {
			cfg.Bitcoin.RPCUser = ""
			cfg.Bitcoin.RPCPassword = ""
			cfg.Bitcoin.CookiePath = ""
		}, true, "authentication"},
		{"user only is incomplete", func(cfg *Config) { cfg.Bitcoin.RPCPassword = "" }, true, "authentication"},
		{"password only is incomplete", func(cfg *Config) { cfg.Bitcoin.RPCUser = "" }, true, "authentication"},
		{"depth below approved range", func(cfg *Config) { cfg.Bitcoin.ConfirmationDepth = 5 }, true, "confirmation_depth"},
		{"depth above approved range", func(cfg *Config) { cfg.Bitcoin.ConfirmationDepth = 101 }, true, "confirmation_depth"},
		{"depth zero rejects without defaults", func(cfg *Config) {
			cfg.Bitcoin.ConfirmationDepth = 0
		}, true, "confirmation_depth"},
		{"evm adapter leaves bitcoin section unchecked", func(cfg *Config) {
			cfg.Chain.Adapter = DefaultChainAdapter
			cfg.Chain.ChainID = 1
			cfg.Bitcoin = BitcoinConfig{}
		}, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := bitcoinTestConfig()
			tt.mutate(cfg)
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

func TestApplyDefaults_BitcoinChainName(t *testing.T) {
	t.Parallel()

	t.Run("nameless bitcoin config defaults name to Bitcoin", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		cfg.Chain.Adapter = BitcoinAdapter
		applyDefaults(cfg)
		assert.Equal(t, "Bitcoin", cfg.Chain.Name)
		// The network has no default: chain.network must be named explicitly,
		// and validateChainConfig rejects an empty one with ErrChainUnset.
		assert.Empty(t, cfg.Chain.Network)
	})

	t.Run("fully empty config defaults the adapter only", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		applyDefaults(cfg)
		assert.Equal(t, DefaultChainAdapter, cfg.Chain.Adapter)
		// Only the tron and bitcoin adapters name their chain; an EVM
		// deployment must set chain.name itself, and validateChainConfig
		// rejects an unnamed one.
		assert.Empty(t, cfg.Chain.Name)
	})

	t.Run("preset name preserved", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		cfg.Chain.Adapter = BitcoinAdapter
		cfg.Chain.Name = "Signet"
		applyDefaults(cfg)
		assert.Equal(t, "Signet", cfg.Chain.Name)
	})
}

func TestApplyDefaults_BitcoinNumericFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		depth     int
		dial      int
		wantDepth int
		wantDial  int
	}{
		{"zero values get defaults", 0, 0, DefaultBitcoinConfirmationDepth, DefaultBitcoinDialTimeoutSeconds},
		{"negative values get defaults", -1, -7, DefaultBitcoinConfirmationDepth, DefaultBitcoinDialTimeoutSeconds},
		{"preset values preserved", 12, 20, 12, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			cfg.Bitcoin.ConfirmationDepth = tt.depth
			cfg.Bitcoin.DialTimeoutSeconds = tt.dial
			applyDefaults(cfg)
			assert.Equal(t, tt.wantDepth, cfg.Bitcoin.ConfirmationDepth, "ConfirmationDepth")
			assert.Equal(t, tt.wantDial, cfg.Bitcoin.DialTimeoutSeconds, "DialTimeoutSeconds")
		})
	}
}

func TestApplyEnvOverrides_BitcoinConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("BITCOIN_RPC_URL", "http://bitcoin-core:8332")
	t.Setenv("BITCOIN_RPC_USER", "shinzorpc")
	t.Setenv("BITCOIN_RPC_PASSWORD", "shinzo-password")
	t.Setenv("BITCOIN_COOKIE_PATH", "/bitcoin/.cookie")
	t.Setenv("BITCOIN_API_KEY", "api-key")
	t.Setenv("BITCOIN_API_KEY_TYPE", "X-Api-Key")
	t.Setenv("BITCOIN_ARCHIVE_RPC_URL", "http://archive-node:8332")
	t.Setenv("BITCOIN_CONFIRMATION_DEPTH", "12")
	t.Setenv("BITCOIN_DIAL_TIMEOUT_SECONDS", "15")
	require.NoError(t, applyEnvOverrides(cfg))

	assert.Equal(t, "http://bitcoin-core:8332", cfg.Bitcoin.RPCURL, "Bitcoin.RPCURL")
	assert.Equal(t, "shinzorpc", cfg.Bitcoin.RPCUser, "Bitcoin.RPCUser")
	assert.Equal(t, "shinzo-password", cfg.Bitcoin.RPCPassword, "Bitcoin.RPCPassword")
	assert.Equal(t, "/bitcoin/.cookie", cfg.Bitcoin.CookiePath, "Bitcoin.CookiePath")
	assert.Equal(t, "api-key", cfg.Bitcoin.APIKey, "Bitcoin.APIKey")
	assert.Equal(t, "X-Api-Key", cfg.Bitcoin.APIKeyType, "Bitcoin.APIKeyType")
	assert.Equal(t, "http://archive-node:8332", cfg.Bitcoin.ArchiveRPCURL, "Bitcoin.ArchiveRPCURL")
	assert.Equal(t, 12, cfg.Bitcoin.ConfirmationDepth, "Bitcoin.ConfirmationDepth")
	assert.Equal(t, 15, cfg.Bitcoin.DialTimeoutSeconds, "Bitcoin.DialTimeoutSeconds")
	assert.True(t, cfg.Bitcoin.AuthConfigured(), "env-provided credentials count as configured")
}

func TestApplyEnvOverrides_BitcoinConfig_InvalidInt(t *testing.T) {
	tests := []struct {
		name     string
		envKey   string
		envValue string
	}{
		{"non-numeric confirmation depth", "BITCOIN_CONFIRMATION_DEPTH", "not_a_number"},
		{"non-numeric dial timeout", "BITCOIN_DIAL_TIMEOUT_SECONDS", "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Bitcoin.ConfirmationDepth = 6
			cfg.Bitcoin.DialTimeoutSeconds = 10
			t.Setenv(tt.envKey, tt.envValue)

			err := applyBitcoinEnvOverrides(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.envKey)
			assert.Equal(t, 6, cfg.Bitcoin.ConfirmationDepth, "failed override leaves the depth preset intact")
			assert.Equal(t, 10, cfg.Bitcoin.DialTimeoutSeconds, "failed override leaves the dial preset intact")
		})
	}
}

// Zero and negative numeric overrides stay accepted: applyDefaults normalizes
// them to the defaults exactly as the yaml contract ("0 or negative = 6")
// promises, so only a non-numeric value is an error.
func TestApplyEnvOverrides_BitcoinConfig_ZeroAndNegativeAccepted(t *testing.T) {
	cfg := &Config{}
	t.Setenv("BITCOIN_CONFIRMATION_DEPTH", "0")
	t.Setenv("BITCOIN_DIAL_TIMEOUT_SECONDS", "-5")

	require.NoError(t, applyBitcoinEnvOverrides(cfg))

	assert.Zero(t, cfg.Bitcoin.ConfirmationDepth, "zero depth accepted, defaults applied later")
	assert.Equal(t, -5, cfg.Bitcoin.DialTimeoutSeconds, "negative dial timeout accepted, defaults applied later")
}

func TestApplyEnvOverrides_BitcoinConfig_EmptyIgnored(t *testing.T) {
	cfg := &Config{}
	cfg.Bitcoin.RPCURL = "http://preset:8332"
	t.Setenv("BITCOIN_RPC_URL", "")
	require.NoError(t, applyEnvOverrides(cfg))

	assert.Equal(t, "http://preset:8332", cfg.Bitcoin.RPCURL, "empty env value should not override")
}

func TestBitcoinConfig_AuthConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		bitcoin BitcoinConfig
		want    bool
	}{
		{"empty", BitcoinConfig{}, false},
		{"user and password", BitcoinConfig{RPCUser: "u", RPCPassword: "p"}, true},
		{"user only", BitcoinConfig{RPCUser: "u"}, false},
		{"password only", BitcoinConfig{RPCPassword: "p"}, false},
		{"cookie only", BitcoinConfig{CookiePath: "/bitcoin/.cookie"}, true},
		{"all auth kinds present", BitcoinConfig{RPCUser: "u", RPCPassword: "p", CookiePath: "/bitcoin/.cookie"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.bitcoin.AuthConfigured())
		})
	}
}

func TestLoadConfig_BitcoinYAML(t *testing.T) {
	neutralizeBitcoinEnv(t)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContent := `
chain:
  name: Bitcoin
  network: Mainnet
  adapter: "bitcoin"
defradb:
  embedded: true
bitcoin:
  rpc_url: "http://bitcoin-core:8332"
  rpc_user: shinzorpc
  rpc_password: temp-fixtures-only
  confirmation_depth: 12
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o600))

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	assert.Equal(t, BitcoinAdapter, cfg.Chain.Adapter, "Chain.Adapter")
	assert.Equal(t, "Bitcoin", cfg.Chain.Name, "nameless bitcoin config defaults the chain name")
	assert.Equal(t, "http://bitcoin-core:8332", cfg.Bitcoin.RPCURL, "Bitcoin.RPCURL")
	assert.Equal(t, 12, cfg.Bitcoin.ConfirmationDepth, "preset depth preserved")
	assert.Equal(t, DefaultBitcoinDialTimeoutSeconds, cfg.Bitcoin.DialTimeoutSeconds, "unset dial timeout defaults")
	assert.True(t, cfg.Bitcoin.AuthConfigured(), "Bitcoin.AuthConfigured")
}

func TestLoadConfig_BitcoinYAML_Invalid(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		errContains string
	}{
		{
			"missing auth",
			"chain:\n  adapter: \"bitcoin\"\nbitcoin:\n  rpc_url: \"http://bitcoin-core:8332\"\n",
			"authentication",
		},
		{
			"depth below approved range",
			"chain:\n  adapter: \"bitcoin\"\nbitcoin:\n  rpc_url: \"http://bitcoin-core:8332\"\n  rpc_user: u\n  rpc_password: p\n  confirmation_depth: 3\n",
			"confirmation_depth",
		},
		{
			"empty rpc_url",
			"chain:\n  adapter: \"bitcoin\"\nbitcoin:\n  rpc_user: u\n  rpc_password: p\n",
			"rpc_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			neutralizeBitcoinEnv(t)

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(tt.yaml), 0o600))

			_, err := LoadConfig(configPath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestLoadConfig_ConfigBitcoinYAML guards the shipped bitcoin deployment
// file. Its bitcoin fields ship empty on purpose: the loader never expands
// a "${VAR}" placeholder, so an unset variable would otherwise hand the
// client a literal string. With a neutralized environment the file must
// therefore fail fast with the loader's own validation message, and with
// the env variables a deployment supplies it loads green while the
// optional fields read as genuinely unset.
func TestLoadConfig_ConfigBitcoinYAML(t *testing.T) {
	// The subtests stay sequential: both mutate the process environment,
	// which must not race with a concurrent LoadConfig reading it.
	t.Run("unset env fails fast with the validation message", func(t *testing.T) {
		neutralizeBitcoinEnv(t)
		t.Setenv("SCHEMA_AUTH_MODE", "")

		_, err := LoadConfig("config_bitcoin.yaml")
		require.Error(t, err, "the shipped file must fail fast when the required env overrides are missing")
		assert.Contains(t, err.Error(), "bitcoin.rpc_url is required")
	})

	t.Run("env overrides load it green", func(t *testing.T) {
		neutralizeBitcoinEnv(t)
		t.Setenv("BITCOIN_RPC_URL", "http://bitcoin-core:8332")
		t.Setenv("BITCOIN_RPC_USER", "shinzorpc")
		t.Setenv("BITCOIN_RPC_PASSWORD", "temp-fixtures-only")
		t.Setenv("SCHEMA_AUTH_MODE", constants.SchemaAuthModeNone)

		cfg, err := LoadConfig("config_bitcoin.yaml")
		require.NoError(t, err, "the shipped bitcoin deployment file must parse and validate")

		assert.Equal(t, BitcoinAdapter, cfg.Chain.Adapter, "Chain.Adapter")
		assert.Equal(t, "Bitcoin", cfg.Chain.Name, "Chain.Name")
		assert.Equal(t, "Mainnet", cfg.Chain.Network, "Chain.Network")
		assert.Equal(t, "http://bitcoin-core:8332", cfg.Bitcoin.RPCURL, "Bitcoin.RPCURL from env")
		assert.Equal(t, "shinzorpc", cfg.Bitcoin.RPCUser, "Bitcoin.RPCUser from env")
		assert.True(t, cfg.Bitcoin.AuthConfigured(), "Bitcoin.AuthConfigured")
		assert.Equal(t, 6, cfg.Bitcoin.ConfirmationDepth, "shipped confirmation depth")
		assert.Equal(t, 10, cfg.Bitcoin.DialTimeoutSeconds, "shipped dial timeout")
		assert.Empty(t, cfg.Bitcoin.CookiePath, "the shipped file leaves the cookie path unset")
		assert.Empty(t, cfg.Bitcoin.ArchiveRPCURL, "the shipped file leaves the optional archive endpoint unset")
		assert.Empty(t, cfg.Bitcoin.APIKey, "the shipped file leaves the optional api key unset")
		assert.Empty(t, cfg.Bitcoin.APIKeyType, "the shipped file leaves the optional api key type unset")
		assert.Empty(t, cfg.Geth.NodeURL, "the bitcoin deployment file carries no geth section")
	})
}
