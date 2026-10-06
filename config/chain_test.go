package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testChain names a chain that validateConfig accepts.
var testChain = ChainConfig{Name: "Ethereum", Network: "Mainnet", Adapter: DefaultChainAdapter}

func TestLoadConfig_ChainRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc  string
		chain string
	}{
		{desc: "no chain section", chain: ""},
		{desc: "no network", chain: "chain:\n  name: \"Ethereum\"\n"},
		{desc: "no name", chain: "chain:\n  network: \"Mainnet\"\n"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			t.Parallel()
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(c.chain+"defradb:\n  embedded: true\n"), 0o600))

			_, err := LoadConfig(configPath)
			require.ErrorIs(t, err, ErrChainUnset)
		})
	}
}
