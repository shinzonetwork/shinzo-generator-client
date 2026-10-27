package evm

// Tests for the Kava ClientAdapter against blocks and receipts captured
// from Kava mainnet (testdata/kava): the RPC-provided (CometBFT) block hash is
// kept, legacy EIP-155 envelopes decode with the right sender, and receipts
// are left exactly as the RPC returned them.

import (
	"context"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kavaFixtureBlocks: a legacy contract call with logs, a block with two
// legacy txs, and two blocks without EVM txs.
var kavaFixtureBlocks = []int64{23209054, 23209012, 23208969, 23209032} //nolint:gochecknoglobals // read-only fixture index

func TestAdapterForChain_Kava(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Kava", " kava ", "KAVA"} {
		assert.IsType(t, rawStandardClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	_, ok := adapterForChain("Kava").(receiptLogStamper)
	assert.False(t, ok, "Kava logs carry correct block fields; only ShinzoHub restamps them")
}

func TestKavaAdapter_BlockFromMainnetFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range kavaFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			fx := loadFixtureFrom(t, "kava", number)
			client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), adapterForChain("Kava"))

			block, err := client.GetBlockByNumber(context.Background(), big.NewInt(number))
			require.NoError(t, err)

			wantHash, wantTxs := fixtureBlockFields(t, fx.block)
			assert.Equal(t, wantHash, block.Hash, "block hash must be the RPC (CometBFT) hash, not a recomputed one")
			require.Len(t, block.Transactions, len(wantTxs))
			for i, tx := range block.Transactions {
				assert.Equal(t, wantTxs[i].Hash, tx.Hash, "tx %d hash", i)
				assert.True(t, strings.EqualFold(wantTxs[i].From, tx.From), "tx %d from: want %s, got %s", i, wantTxs[i].From, tx.From)
				assert.Equal(t, wantHash, tx.BlockHash, "tx %d blockHash", i)
			}

			for _, tx := range wantTxs {
				r, rerr := client.GetTransactionReceipt(context.Background(), tx.Hash)
				require.NoError(t, rerr)
				for i, l := range r.Logs {
					assert.Equal(t, block.Number, l.BlockNumber, "log %d blockNumber", i)
					assert.Equal(t, wantHash, l.BlockHash, "log %d blockHash", i)
				}
			}
		})
	}
}
