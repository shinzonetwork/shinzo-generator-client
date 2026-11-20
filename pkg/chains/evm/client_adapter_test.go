package evm

// Tests for the ClientAdapter extension surface: the chain-name registry
// that selects a registered ConvertBlock override, and the default
// passthrough adapter every unregistered name resolves to.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConvertAdapter is a minimal ClientAdapter override for registry tests.
type fakeConvertAdapter struct{}

func (fakeConvertAdapter) ConvertBlock(_ json.RawMessage) (*Block, error) {
	return &Block{Hash: "0xoverride"}, nil
}

// --- adapterForChain (the registry) ---

func TestAdapterForChain_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "   ", "\t", "Ethereum", "BSC", "Unknown Chain", "TotallyUnknownChain"} {
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
