package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
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

func TestValidateConfig_ChainMode(t *testing.T) {
	t.Parallel()
	usd1 := Token{Address: "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d", SupplySlot: "0x2"}
	cases := []struct {
		name      string
		mode      string
		tokens    []Token
		interval  int64
		pollMs    int64
		snapshots bool
		wantErr   error
	}{
		{name: "full by default", mode: ""},
		{name: "full", mode: ChainModeFull},
		{name: "tokens mode is not accepted", mode: ChainModeTokens, tokens: []Token{usd1}, interval: 1, wantErr: ErrInvalidChainMode},
		{name: "unknown mode", mode: "logs", wantErr: ErrInvalidChainMode},
		{name: "tokens in full mode", mode: ChainModeFull, tokens: []Token{usd1}, wantErr: ErrTokenKeyInFullMode},
		{name: "state capture interval in full mode", mode: ChainModeFull, interval: 1, wantErr: ErrTokenKeyInFullMode},
		{name: "finalized poll interval in full mode", mode: ChainModeFull, pollMs: 500, wantErr: ErrTokenKeyInFullMode},
		{name: "snapshots in full mode", mode: ChainModeFull, snapshots: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Chain: testChain}
			cfg.Chain.Mode = tt.mode
			cfg.Chain.Tokens = tt.tokens
			cfg.Chain.StateCaptureInterval = tt.interval
			cfg.Chain.FinalizedPollIntervalMs = tt.pollMs
			cfg.Snapshot.Enabled = tt.snapshots
			cfg.DefraDB.Embedded = true
			cfg.Indexer.SchemaAuthMode = constants.SchemaAuthModeToken
			require.ErrorIs(t, validateConfig(cfg), tt.wantErr)
		})
	}
}

// ContractAddress takes either letter case, with or without 0x, and refuses anything that is not
// 20 bytes of hex.
func TestContractAddress(t *testing.T) {
	t.Parallel()
	const usd1 = "8d0d000ee44948fc98c9b98a4fa4921476f08b0d"
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr error
	}{
		{name: "lower case with 0x", raw: "0x" + usd1, want: usd1},
		{name: "upper case without 0x", raw: strings.ToUpper(usd1), want: usd1},
		{name: "short", raw: "0x8d0d", wantErr: ErrInvalidContractAddress},
		{name: "long", raw: "0x" + usd1 + "00", wantErr: ErrInvalidContractAddress},
		{name: "not hex", raw: "0x" + usd1[:39] + "g", wantErr: ErrInvalidContractAddress},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ContractAddress(tt.raw)
			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestApplyChainEnvOverrides_Mode(t *testing.T) {
	t.Setenv("CHAIN_MODE", ChainModeTokens)
	t.Setenv("CHAIN_TOKENS", " 0xaa:0x1, 0xbb : 0x2 ")
	t.Setenv("CHAIN_STATE_CAPTURE_INTERVAL", "10")
	t.Setenv("CHAIN_FINALIZED_POLL_INTERVAL_MS", "250")
	cfg := &Config{Chain: testChain}
	require.NoError(t, applyChainEnvOverrides(cfg))
	require.Equal(t, ChainModeTokens, cfg.Chain.Mode)
	require.Equal(t, []Token{{Address: "0xaa", SupplySlot: "0x1"}, {Address: "0xbb", SupplySlot: "0x2"}}, cfg.Chain.Tokens)
	require.Equal(t, int64(10), cfg.Chain.StateCaptureInterval)
	require.Equal(t, int64(250), cfg.Chain.FinalizedPollIntervalMs)

	t.Setenv("CHAIN_TOKENS", "0xaa:0x1,0xbb")
	require.ErrorIs(t, applyChainEnvOverrides(cfg), ErrInvalidChainTokensEnv)

	t.Setenv("CHAIN_TOKENS", "0xaa:0x1")
	t.Setenv("CHAIN_STATE_CAPTURE_INTERVAL", "ten")
	require.Error(t, applyChainEnvOverrides(cfg))

	t.Setenv("CHAIN_STATE_CAPTURE_INTERVAL", "10")
	t.Setenv("CHAIN_FINALIZED_POLL_INTERVAL_MS", "fast")
	require.Error(t, applyChainEnvOverrides(cfg))
}

// The container image runs with the shipped config, so it must name a chain the generator accepts.
func TestLoadConfig_ShippedConfig(t *testing.T) {
	t.Setenv("SCHEMA_AUTH_MODE", "none")
	cfg, err := LoadConfig("config.yaml")
	require.NoError(t, err)
	require.Equal(t, uint64(1), cfg.Chain.ChainID)
}
