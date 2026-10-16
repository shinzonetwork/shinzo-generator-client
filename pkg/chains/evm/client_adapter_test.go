package evm

// Tests for the ClientAdapter extension surface: the default adapter's
// no-op contract, its standard-envelope decoding, and the chain-name
// registry lookup with its default fallback.

import (
	"encoding/json"
	"errors"
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

func TestDefaultClientAdapter_DecodeTransactions_StandardEnvelopes(t *testing.T) {
	t.Parallel()
	rawBlock := json.RawMessage(`{
		"hash": "0x0000000000000000000000000000000000000000000000000000000000000001",
		"transactions": [
			{
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
			},
			{
				"type": "0x2",
				"chainId": "0x1",
				"nonce": "0x2",
				"maxPriorityFeePerGas": "0x3b9aca00",
				"maxFeePerGas": "0x77359400",
				"gas": "0x5208",
				"to": "0x0000000000000000000000000000000000000002",
				"value": "0x1",
				"input": "0x",
				"accessList": [],
				"v": "0x1",
				"r": "0x1111111111111111111111111111111111111111111111111111111111111111",
				"s": "0x2222222222222222222222222222222222222222222222222222222222222222"
			}
		]
	}`)

	txs, err := defaultAdapter.DecodeTransactions(rawBlock)
	require.NoError(t, err)
	require.Len(t, txs, 2)
	assert.Equal(t, uint8(ethtypes.LegacyTxType), txs[0].Type())
	assert.Equal(t, uint8(ethtypes.DynamicFeeTxType), txs[1].Type())
	assert.Equal(t, big.NewInt(2000000000).String(), txs[1].GasFeeCap().String())
}

func TestDefaultClientAdapter_DecodeTransactions_UnsupportedEnvelope(t *testing.T) {
	t.Parallel()
	// A ZKsync-style 0x71 envelope is unknown to go-ethereum's decoder.
	rawBlock := json.RawMessage(`{
		"transactions": [
			{
				"type": "0x71",
				"nonce": "0x1",
				"gas": "0x5208",
				"to": "0x0000000000000000000000000000000000000002",
				"value": "0x1",
				"input": "0x"
			}
		]
	}`)

	_, err := defaultAdapter.DecodeTransactions(rawBlock)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ethtypes.ErrTxTypeNotSupported), "expected ErrTxTypeNotSupported, got: %v", err)
}

func TestDefaultClientAdapter_DecodeTransactions_MalformedBlock(t *testing.T) {
	t.Parallel()
	_, err := defaultAdapter.DecodeTransactions(json.RawMessage(`{"transactions": "not-an-array"}`))
	assert.Error(t, err)
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
