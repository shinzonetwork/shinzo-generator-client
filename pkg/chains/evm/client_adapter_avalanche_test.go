package evm

// Tests for the Avalanche ClientAdapter against blocks and receipts captured
// from C-Chain mainnet (testdata/avalanche): the RPC-provided block hash is
// kept although coreth's extra header fields make go-ethereum recompute a
// different one, standard envelopes decode with the right sender and hash,
// yParity is read from the raw tx, and blockExtraData (atomic txs) is
// ignored.

import (
	"context"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// avalancheFixtureBlocks: a busy block mixing legacy and 0x2 txs with logs,
// a block with EVM txs plus an atomic tx in blockExtraData, and a block
// carrying only an atomic tx (no EVM txs).
var avalancheFixtureBlocks = []int64{98712791, 98711738, 98708120} //nolint:gochecknoglobals // read-only fixture index

func TestAdapterForChain_Avalanche(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Avalanche", " avalanche ", "AVALANCHE"} {
		assert.IsType(t, avalancheClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	assert.True(t, adapterForChain("Avalanche").NeedsRawBlock(), "coreth block hashes are only correct on the raw path")
	_, ok := adapterForChain("Avalanche").(receiptLogStamper)
	assert.False(t, ok, "Avalanche logs carry correct block fields; only ShinzoHub restamps them")
}

func TestAvalancheAdapter_BlockFromMainnetFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range avalancheFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			fx := loadFixtureFrom(t, "avalanche", number)
			client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), adapterForChain("Avalanche"))

			block, err := client.GetBlockByNumber(context.Background(), big.NewInt(number))
			require.NoError(t, err)

			wantHash, wantTxs := fixtureBlockFields(t, fx.block)
			wantYParity := fixtureYParities(t, fx.block)
			assert.Equal(t, wantHash, block.Hash, "block hash must be the RPC hash, not a recomputed one")
			assert.Equal(t, strconv.FormatInt(number, 10), block.Number)
			require.Len(t, block.Transactions, len(wantTxs))
			for i, tx := range block.Transactions {
				assert.Equal(t, wantTxs[i].Hash, tx.Hash, "tx %d hash", i)
				assert.True(t, strings.EqualFold(wantTxs[i].From, tx.From), "tx %d from: want %s, got %s", i, wantTxs[i].From, tx.From)
				assert.Equal(t, wantHash, tx.BlockHash, "tx %d blockHash", i)
				assert.Equal(t, wantYParity[i], tx.YParity, "tx %d yParity", i)
			}

			receipts, err := client.GetBlockReceipts(context.Background(), wantHash)
			require.NoError(t, err)
			require.Len(t, receipts, len(wantTxs))
			for i, r := range receipts {
				assert.Equal(t, wantTxs[i].Hash, r.TransactionHash, "receipt %d joins its tx by hash", i)
				for j, l := range r.Logs {
					assert.Equal(t, block.Number, l.BlockNumber, "receipt %d log %d blockNumber", i, j)
					assert.Equal(t, wantHash, l.BlockHash, "receipt %d log %d blockHash", i, j)
				}
			}
		})
	}
}

// TestAvalancheAdapter_DefaultAdapterStoresWrongHash pins why the raw path is
// mandatory: the typed ethclient decode accepts a coreth block without error
// but recomputes a hash that drops coreth's extra header fields. If this ever
// starts matching, coreth headers became go-ethereum compatible.
func TestAvalancheAdapter_DefaultAdapterStoresWrongHash(t *testing.T) {
	t.Parallel()
	fx := loadFixtureFrom(t, "avalanche", avalancheFixtureBlocks[0])
	client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), defaultAdapter)

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(avalancheFixtureBlocks[0]))
	require.NoError(t, err, "the typed path must not reject the block; the failure is silent")

	wantHash, _ := fixtureBlockFields(t, fx.block)
	assert.NotEqual(t, wantHash, block.Hash)
}

func TestAvalancheAdapter_FixturesCoverTxTypesAndAtomicTxs(t *testing.T) {
	t.Parallel()
	types := map[string]bool{}
	var atomicOnly, atomicWithTxs bool
	for _, number := range avalancheFixtureBlocks {
		fx := loadFixtureFrom(t, "avalanche", number)
		var b struct {
			BlockExtraData string `json:"blockExtraData"`
			Transactions   []struct {
				Type string `json:"type"`
			} `json:"transactions"`
		}
		require.NoError(t, json.Unmarshal(fx.block, &b))
		for _, tx := range b.Transactions {
			types[tx.Type] = true
		}
		hasAtomic := b.BlockExtraData != "" && b.BlockExtraData != "0x"
		atomicOnly = atomicOnly || (hasAtomic && len(b.Transactions) == 0)
		atomicWithTxs = atomicWithTxs || (hasAtomic && len(b.Transactions) > 0)
	}
	assert.True(t, types["0x0"] && types["0x2"], "fixtures must cover legacy and 0x2 txs, got %v", types)
	assert.True(t, atomicOnly, "fixtures must include a block with only an atomic tx")
	assert.True(t, atomicWithTxs, "fixtures must include a block with EVM txs and an atomic tx")
}

func TestRawYParity(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		raw  json.RawMessage
		want string
	}{
		"typed tx":  {json.RawMessage(`{"type":"0x2","yParity":"0x1"}`), "0x1"},
		"legacy tx": {json.RawMessage(`{"type":"0x0","v":"0x150f7"}`), ""},
		"empty":     {nil, ""},
		"malformed": {json.RawMessage(`{"yParity":1}`), ""},
	}
	for name, tc := range cases {
		assert.Equal(t, tc.want, rawYParity(tc.raw), name)
	}
}

// fixtureYParities returns each fixture tx's yParity as the RPC reported it.
func fixtureYParities(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var b struct {
		Transactions []json.RawMessage `json:"transactions"`
	}
	require.NoError(t, json.Unmarshal(raw, &b))
	out := make([]string, len(b.Transactions))
	for i, tx := range b.Transactions {
		out[i] = rawYParity(tx)
	}
	return out
}
