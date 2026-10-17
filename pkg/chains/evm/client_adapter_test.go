package evm

// Tests for the ClientAdapter extension surface: the default adapter's
// no-op contract, its empty decode stub, and the chain-name
// registry lookup with its default fallback.

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- defaultClientAdapter ---

func TestDefaultClientAdapter_NeedsRawBlock(t *testing.T) {
	t.Parallel()
	assert.False(t, defaultAdapter.NeedsRawBlock())
}

func TestDefaultClientAdapter_ResolveFrom(t *testing.T) {
	t.Parallel()
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)

	from, ok := defaultAdapter.ResolveFrom(tx, nil)
	assert.False(t, ok, "ok=false must keep the existing GetFromAddress behaviour")
	assert.Empty(t, from)
}

func TestDefaultClientAdapter_YParity(t *testing.T) {
	t.Parallel()
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)

	assert.Empty(t, defaultAdapter.YParity(tx, nil))
}

func TestDefaultClientAdapter_DecodeTransactions_IsNoOp(t *testing.T) {
	t.Parallel()

	for name, rawTxs := range map[string][]json.RawMessage{
		"standard envelopes": {json.RawMessage(`{
			"type": "0x0",
			"nonce": "0x1",
			"gasPrice": "0x3b9aca00",
			"gas": "0x5208",
			"to": "0x0000000000000000000000000000000000000002",
			"value": "0x1",
			"input": "0x",
			"v": "0x1b",
			"r": "0x1111111111111111111111111111111111111111111111111111111111111111",
			"s": "0x2222222222222222222222222222222222222222222222222222222222222222"
		}`)},
		"unsupported envelope": {json.RawMessage(`{
			"type": "0x71",
			"nonce": "0x1",
			"gas": "0x5208",
			"to": "0x0000000000000000000000000000000000000002",
			"value": "0x1",
			"input": "0x"
		}`)},
		"malformed raw tx": {json.RawMessage(`not-an-object`)},
	} {
		txs, err := defaultAdapter.DecodeTransactions(rawTxs)
		require.NoError(t, err, name)
		assert.Empty(t, txs, name)
	}
}

// --- adapterForChain ---

func TestAdapterForChain_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "Ethereum", "BSC", "arbitrum", "  Monad  ", "TotallyUnknownChain"} {
		assert.Equal(t, defaultAdapter, adapterForChain(name), "chain %q must resolve to the default adapter", name)
	}
}

func TestAdapterForChain_RegisteredEntry(t *testing.T) {
	// Not parallel: mutates the shared chainAdapters registry.
	const registered = "testchain-adapter"
	chainAdapters[registered] = stubRawAdapter{}
	t.Cleanup(func() { delete(chainAdapters, registered) })

	assert.Equal(t, ClientAdapter(stubRawAdapter{}), adapterForChain("TestChain-Adapter"))
	assert.Equal(t, ClientAdapter(stubRawAdapter{}), adapterForChain("  testchain-adapter  "))
	assert.Equal(t, defaultAdapter, adapterForChain("OtherChain"))
}
