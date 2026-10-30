package evm

// Tests for the Kaia ClientAdapter against blocks and receipts captured from
// Kaia mainnet (testdata/kaia): the RPC block hash is kept although
// go-ethereum recomputes a different one, and Kaia-native txs (reported as
// 0x0 by the eth_ namespace) keep the RPC hash and sender, so every tx joins
// its receipt by hash.

import (
	"context"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kaiaFixtureBlocks: a block with two fee-delegated smart-contract executions
// among legacy txs (33 logs), a block with one native value-transfer-memo tx,
// and an empty block.
var kaiaFixtureBlocks = []int64{231186046, 231185949, 231185931} //nolint:gochecknoglobals // read-only fixture index

func TestAdapterForChain_Kaia(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Kaia", " kaia ", "KAIA"} {
		assert.IsType(t, kaiaClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	assert.True(t, adapterForChain("Kaia").NeedsRawBlock(), "Kaia block hashes are only correct on the raw path")
	_, ok := adapterForChain("Kaia").(receiptLogStamper)
	assert.False(t, ok, "Kaia logs carry correct block fields; only ShinzoHub restamps them")
}

func TestKaiaAdapter_BlockFromMainnetFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range kaiaFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			fx := loadFixtureFrom(t, "kaia", number)
			client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), adapterForChain("Kaia"))

			block, err := client.GetBlockByNumber(context.Background(), big.NewInt(number))
			require.NoError(t, err)

			wantHash, wantTxs := fixtureBlockFields(t, fx.block)
			assert.Equal(t, wantHash, block.Hash, "block hash must be the RPC hash, not a recomputed one")
			assert.Equal(t, strconv.FormatInt(number, 10), block.Number)
			require.Len(t, block.Transactions, len(wantTxs))
			for i, tx := range block.Transactions {
				assert.Equal(t, wantTxs[i].Hash, tx.Hash, "tx %d hash must be the RPC hash", i)
				assert.True(t, strings.EqualFold(wantTxs[i].From, tx.From), "tx %d from: want %s, got %s", i, wantTxs[i].From, tx.From)
				assert.Equal(t, wantHash, tx.BlockHash, "tx %d blockHash", i)
				assert.Empty(t, tx.YParity, "tx %d: Kaia's RPC reports no yParity", i)
			}

			receipts, err := client.GetBlockReceipts(context.Background(), wantHash)
			require.NoError(t, err)
			receiptMap := (&Converter{}).buildReceiptMap(receipts)
			for i, tx := range block.Transactions {
				r, ok := receiptMap[tx.Hash]
				require.True(t, ok, "tx %d must join its receipt by hash", i)
				assert.NotEmpty(t, r.GasUsed, "tx %d receipt gasUsed", i)
				assert.NotEmpty(t, r.Status, "tx %d receipt status", i)
				for j, l := range r.Logs {
					assert.Equal(t, block.Number, l.BlockNumber, "tx %d log %d blockNumber", i, j)
					assert.Equal(t, wantHash, l.BlockHash, "tx %d log %d blockHash", i, j)
				}
			}
		})
	}
}

// TestKaiaAdapter_DefaultAdapterStoresWrongHash pins why the raw path is
// mandatory: the typed ethclient decode accepts a Kaia block without error
// but recomputes a different hash.
func TestKaiaAdapter_DefaultAdapterStoresWrongHash(t *testing.T) {
	t.Parallel()
	fx := loadFixtureFrom(t, "kaia", kaiaFixtureBlocks[0])
	client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), defaultAdapter)

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(kaiaFixtureBlocks[0]))
	require.NoError(t, err, "the typed path must not reject the block; the failure is silent")

	wantHash, _ := fixtureBlockFields(t, fx.block)
	assert.NotEqual(t, wantHash, block.Hash)
}

// TestKaiaAdapter_NativeTxsDefeatRecomputation pins why the raw hash and the
// raw from are mandatory: native txs decode as legacy txs without error, but
// both the recomputed hash and the recovered sender are wrong.
func TestKaiaAdapter_NativeTxsDefeatRecomputation(t *testing.T) {
	t.Parallel()
	var native int
	for _, number := range kaiaFixtureBlocks {
		fx := loadFixtureFrom(t, "kaia", number)
		_, wantTxs := fixtureBlockFields(t, fx.block)
		for i, rawTx := range fixtureRawTxs(t, fx.block) {
			tx := new(ethtypes.Transaction)
			require.NoError(t, tx.UnmarshalJSON(rawTx), "block %d tx %d", number, i)
			if tx.Hash().Hex() == wantTxs[i].Hash {
				continue
			}
			native++
			from, err := GetFromAddress(tx)
			if err == nil {
				assert.False(t, strings.EqualFold(wantTxs[i].From, from.Hex()),
					"block %d tx %d: a recomputed-hash mismatch should come with a wrong sender", number, i)
			}
		}
	}
	assert.Equal(t, 3, native, "fixtures hold two fee-delegated executions and one value-transfer-memo tx")
}

func TestKaiaAdapter_ResolveFrom(t *testing.T) {
	t.Parallel()
	from, ok := kaiaClientAdapter{}.ResolveFrom(nil, json.RawMessage(`{"from":"0x7d2a7cc1d4a8cd3d0d2c0f7f0d3e3c5c0b6f5a11"}`))
	require.True(t, ok)
	assert.True(t, strings.EqualFold("0x7d2a7cc1d4a8cd3d0d2c0f7f0d3e3c5c0b6f5a11", from))
	_, ok = kaiaClientAdapter{}.ResolveFrom(nil, json.RawMessage(`{}`))
	assert.False(t, ok, "a tx without from falls back to signature recovery")
}
