package evm

// Tests for the ClientAdapter extension surface: the vetted-chain allowlist
// enforced at construction, and the chain-name registry that selects a
// registered ConvertBlock override.

import (
	"encoding/json"
	stderrors "errors"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConvertAdapter is a minimal ClientAdapter override for registry tests.
type fakeConvertAdapter struct{}

func (fakeConvertAdapter) ConvertBlock(_ json.RawMessage) (*Block, error) {
	return &Block{Hash: "0xoverride"}, nil
}

// --- validateChainName (the construction-time allowlist) ---

func TestValidateChainName_EmptyKeepsDefault(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "   ", "\t"} {
		require.NoError(t, validateChainName(name), "an empty chain name keeps the Ethereum default: %q", name)
	}
}

func TestValidateChainName_VettedNamesPass(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"Ethereum", "ethereum", "  Ethereum  ",
		"BSC", "bsc",
		"Polygon", "polygon",
		"Arbitrum One", "arbitrum one",
		"OP Mainnet", "op mainnet",
		"Base", "base",
		"ZKsync Era", "zksync era",
		"Linea", "linea",
		"Monad", "monad",
		"Avalanche C-Chain", "avalanche c-chain",
	} {
		assert.NoError(t, validateChainName(name), "vetted chain %q must pass, case-insensitively and whitespace-tolerantly", name)
	}
}

func TestValidateChainName_UnknownNameFailsWithFixtureRequirement(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"UnknownChain", "arbitrum", "OP", "ShinzoHub"} {
		err := validateChainName(name)
		require.Error(t, err, "an unknown non-empty chain name must fail construction: %q", name)
		assert.Contains(t, err.Error(), "vetted chain allowlist",
			"the error must name the allowlist so the operator sees the misconfiguration: %q", name)
		assert.Contains(t, err.Error(), "golden fixture",
			"the error must name the fixture requirement: %q", name)
	}
}

func TestValidateChainName_ConfigurationErrorClassification(t *testing.T) {
	t.Parallel()
	err := validateChainName("UnknownChain")
	require.Error(t, err)
	idxErr, ok := stderrors.AsType[errors.IndexerError](err)
	require.True(t, ok, "the allowlist rejection must be a package IndexerError, got: %T", err)
	assert.Equal(t, errors.CodeConfigurationError, idxErr.Code(), "a misconfigured chain name is a configuration error")
}

// --- construction rejects an unknown chain before dialing ---

func TestNewEthereumClientForChain_UnknownChainFailsFast(t *testing.T) {
	t.Parallel()
	// Empty URLs and an unknown name: the allowlist check runs before any
	// dial, so no server is needed to prove the fail-fast.
	_, err := NewEthereumClientForChain(t.Context(), "", "", "", "", 0, "UnknownChain")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vetted chain allowlist")
}

// --- adapterForChain (the registry) ---

func TestAdapterForChain_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "Ethereum", "BSC", "Arbitrum One", "TotallyUnknownChain"} {
		assert.Equal(t, defaultAdapter, adapterForChain(name), "chain %q must resolve to the default adapter", name)
	}
}

func TestAdapterForChain_RegisteredEntry(t *testing.T) {
	// Not parallel: mutates the shared chainAdapters registry.
	const registered = "testchain-adapter"
	chainAdapters[registered] = fakeConvertAdapter{}
	t.Cleanup(func() { delete(chainAdapters, registered) })

	assert.Equal(t, ClientAdapter(fakeConvertAdapter{}), adapterForChain("TestChain-Adapter"))
	assert.Equal(t, ClientAdapter(fakeConvertAdapter{}), adapterForChain("  testchain-adapter  "))
	assert.Equal(t, defaultAdapter, adapterForChain("OtherChain"))
}

func TestDefaultAdapter_DelegatesToConvertRawBlock(t *testing.T) {
	t.Parallel()

	block := fullBlockResponse(testBlockNumberHex, nil)
	block["hash"] = rawBlockHash
	raw, err := json.Marshal(block)
	require.NoError(t, err)

	viaDefault, err := defaultAdapter.ConvertBlock(raw)
	require.NoError(t, err)
	viaHelper, err := ConvertRawBlock(raw)
	require.NoError(t, err)

	assert.Equal(t, viaHelper, viaDefault,
		"the default adapter must delegate to the package-level helper so registered adapters can reuse it")
	assert.Equal(t, strings.ToLower(rawBlockHash), viaDefault.Hash)
}
