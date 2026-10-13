package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_EthereumMainnetSchema(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Ethereum", "--network", "Mainnet"}, &buf))
	sdl := buf.String()
	assert.NotEmpty(t, sdl)
	for _, typeName := range evm.DefaultCollections() {
		assert.Contains(t, sdl, typeName)
	}
}

func TestRun_WithChain(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Arbitrum", "--network", "Mainnet"}, &buf))
	sdl := buf.String()
	assert.NotEmpty(t, sdl)
	assert.NotContains(t, sdl, evm.DefaultCollectionPrefix)
	assert.Contains(t, sdl, "Arbitrum__Mainnet__Block")
}

func TestRun_ChainReplacesAllCollectionTypes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Optimism", "--network", "Mainnet"}, &buf))
	sdl := buf.String()
	collections := evm.NewCollectionNames("Optimism__Mainnet")
	for _, name := range collections.AllCollections() {
		assert.Contains(t, sdl, name)
	}
}

func TestRun_InvalidFlag(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.Error(t, run([]string{"build_schema", "--nonexistent"}, &buf))
}

func TestRun_EthereumMainnetOutputMatchesFacade(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Ethereum", "--network", "Mainnet"}, &buf))
	expected, err := evm.NewCollectionNames(evm.DefaultCollectionPrefix).MergedSDL()
	require.NoError(t, err)
	assert.Equal(t, expected, buf.String())
}

func TestRun_OutputWithChainMatchesFacade(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Arbitrum", "--network", "Mainnet"}, &buf))
	expected, err := evm.NewCollectionNames("Arbitrum__Mainnet").MergedSDL()
	require.NoError(t, err)
	assert.Equal(t, expected, buf.String())
}

func TestRun_UnknownAdapterFails(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := run([]string{"build_schema", "--chain", "Ethereum", "--network", "Mainnet", "--adapter", "cosmos"}, &buf)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrChainFactoryNotRegistered)
}

func TestRun_SingleFile(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Ethereum", "--network", "Mainnet", "--file", "block.graphql"}, &buf))
	sdl := buf.String()
	assert.NotEmpty(t, sdl)
	assert.Contains(t, sdl, "Ethereum__Mainnet__Block")
	assert.NotContains(t, sdl, "type Ethereum__Mainnet__Transaction")
}

func TestRun_SingleFileWithChain(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--file", "block.graphql", "--chain", "Arbitrum", "--network", "Mainnet"}, &buf))
	sdl := buf.String()
	assert.NotContains(t, sdl, "Ethereum__Mainnet")
	assert.Contains(t, sdl, "Arbitrum__Mainnet__Block")
}

func TestRun_SingleFileNotFound(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.Error(t, run([]string{"build_schema", "--chain", "Ethereum", "--network", "Mainnet", "--file", "nonexistent.graphql"}, &buf))
}

func TestRun_ListFiles(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--chain", "Ethereum", "--network", "Mainnet", "--list-files"}, &buf))
	output := strings.TrimSpace(buf.String())
	assert.NotEmpty(t, output)
	lines := strings.Split(output, "\n")
	files, err := evm.NewCollectionNames(evm.DefaultCollectionPrefix).CollectionFiles()
	require.NoError(t, err)
	expected := make([]string, 0, len(files))
	for _, f := range files {
		expected = append(expected, f.File)
	}
	assert.Equal(t, expected, lines)
}

func TestRun_ListFilesIgnoresChain(t *testing.T) {
	t.Parallel()
	var bufEthereum, bufArbitrum bytes.Buffer
	require.NoError(t, run([]string{"build_schema", "--list-files", "--chain", "Ethereum", "--network", "Mainnet"}, &bufEthereum))
	require.NoError(t, run([]string{"build_schema", "--list-files", "--chain", "Arbitrum", "--network", "Mainnet"}, &bufArbitrum))
	assert.Equal(t, bufEthereum.String(), bufArbitrum.String())
}

func TestRun_PrefixFlagFails(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := run([]string{"build_schema", "--prefix", "Arbitrum__Mainnet"}, &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--prefix is no longer supported")
	assert.Contains(t, err.Error(), "--chain, --network, and --adapter")
	assert.Empty(t, buf.String())
}

func TestRun_ChainAndNetworkRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{name: "neither", args: []string{"build_schema"}},
		{name: "no network", args: []string{"build_schema", "--chain", "BSC"}},
		{name: "no chain", args: []string{"build_schema", "--network", "Testnet"}},
		{name: "list files", args: []string{"build_schema", "--list-files"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			require.ErrorIs(t, run(tc.args, &buf), errChainFlagsUnset)
			assert.Empty(t, buf.String())
		})
	}
}
