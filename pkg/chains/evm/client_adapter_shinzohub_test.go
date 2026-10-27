package evm

// Tests for the ShinzoHub ClientAdapter against blocks and receipts captured
// from ShinzoHub testnet (testdata/shinzohub): the RPC-provided block hash is
// kept, standard envelopes decode with the right sender, and precompile logs
// (which the RPC returns with blockNumber 0x0) are restamped from their
// receipt.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shinzohubFixture is one captured testnet block with its receipts.
type shinzohubFixture struct {
	number   int64
	block    json.RawMessage
	receipts json.RawMessage
}

// shinzohubFixtureBlocks are the captured heights: a 0x2 call to the
// indexerregistry precompile, a 0x2 call to the hostregistry precompile, a
// legacy contract call whose logs come from the EVM itself, and an empty
// block.
var shinzohubFixtureBlocks = []int64{5708560, 5648812, 5406552, 5708559} //nolint:gochecknoglobals // read-only fixture index

func loadShinzohubFixture(t *testing.T, number int64) shinzohubFixture {
	t.Helper()
	return loadFixtureFrom(t, "shinzohub", number)
}

// loadFixtureFrom reads block_<n>.json and receipts_<n>.json from
// testdata/<chain>.
func loadFixtureFrom(t *testing.T, chain string, number int64) shinzohubFixture {
	t.Helper()
	dir := filepath.Join("testdata", chain)
	block, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("block_%d.json", number)))
	require.NoError(t, err)
	receipts, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("receipts_%d.json", number)))
	require.NoError(t, err)
	return shinzohubFixture{number: number, block: block, receipts: receipts}
}

// newShinzohubFixtureServer serves one fixture block, its receipts, and each
// receipt by tx hash for the per-transaction fallback.
func newShinzohubFixtureServer(t *testing.T, fx shinzohubFixture) string {
	t.Helper()
	var receipts []json.RawMessage
	require.NoError(t, json.Unmarshal(fx.receipts, &receipts))
	byHash := make(map[string]json.RawMessage, len(receipts))
	for _, r := range receipts {
		var probe struct {
			TransactionHash string `json:"transactionHash"`
		}
		require.NoError(t, json.Unmarshal(r, &probe))
		byHash[probe.TransactionHash] = r
	}

	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fx.block, nil
		case ethGetBlockReceipts:
			return fx.receipts, nil
		case ethGetTransactionReceipt:
			var args []string
			if err := json.Unmarshal(params, &args); err != nil || len(args) != 1 {
				return nil, fmt.Errorf("bad params: %s", params)
			}
			if r, ok := byHash[args[0]]; ok {
				return r, nil
			}
			return nil, fmt.Errorf("unknown tx %s", args[0])
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	})
	t.Cleanup(server.Close)
	return server.URL
}

// fixtureBlockFields extracts the RPC values the conversion must preserve.
func fixtureBlockFields(t *testing.T, raw json.RawMessage) (hash string, txs []struct{ Hash, From string }) {
	t.Helper()
	var b struct {
		Hash         string `json:"hash"`
		Transactions []struct {
			Hash string `json:"hash"`
			From string `json:"from"`
		} `json:"transactions"`
	}
	require.NoError(t, json.Unmarshal(raw, &b))
	for _, tx := range b.Transactions {
		txs = append(txs, struct{ Hash, From string }{tx.Hash, tx.From})
	}
	return b.Hash, txs
}

func TestAdapterForChain_Shinzohub(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Shinzohub", " shinzohub ", "SHINZOHUB"} {
		assert.IsType(t, shinzohubClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	for _, name := range []string{"Ethereum", "BSC", ""} {
		assert.Equal(t, defaultAdapter, adapterForChain(name), "chain name %q must keep the default adapter", name)
	}
}

func TestShinzohubAdapter_DefaultAdapterDoesNotStampLogs(t *testing.T) {
	t.Parallel()
	_, ok := defaultAdapter.(receiptLogStamper)
	assert.False(t, ok, "the default adapter must leave receipts exactly as the RPC returned them")
}

func TestShinzohubAdapter_BlockFromTestnetFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range shinzohubFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			fx := loadShinzohubFixture(t, number)
			client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), adapterForChain("Shinzohub"))

			block, err := client.GetBlockByNumber(context.Background(), big.NewInt(number))
			require.NoError(t, err)

			wantHash, wantTxs := fixtureBlockFields(t, fx.block)
			assert.Equal(t, wantHash, block.Hash, "block hash must be the RPC (CometBFT) hash, not a recomputed one")
			assert.Equal(t, strconv.FormatInt(number, 10), block.Number)
			require.Len(t, block.Transactions, len(wantTxs))
			for i, tx := range block.Transactions {
				assert.Equal(t, wantTxs[i].Hash, tx.Hash, "tx %d hash", i)
				assert.True(t, strings.EqualFold(wantTxs[i].From, tx.From), "tx %d from: want %s, got %s", i, wantTxs[i].From, tx.From)
				assert.Equal(t, wantHash, tx.BlockHash, "tx %d blockHash", i)
				assert.Equal(t, block.Number, tx.BlockNumber, "tx %d blockNumber", i)
			}
		})
	}
}

func TestShinzohubAdapter_ReceiptLogsCarryBlockFields(t *testing.T) {
	t.Parallel()
	for _, number := range shinzohubFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			fx := loadShinzohubFixture(t, number)
			url := newShinzohubFixtureServer(t, fx)
			client := newRawPathClient(t, url, adapterForChain("Shinzohub"))
			wantHash, wantTxs := fixtureBlockFields(t, fx.block)
			wantNumber := strconv.FormatInt(number, 10)

			batch, err := client.GetBlockReceipts(context.Background(), wantHash)
			require.NoError(t, err)
			require.Len(t, batch, len(wantTxs))

			// The per-transaction fallback must stamp exactly like the batch call.
			single := make([]*TransactionReceipt, 0, len(wantTxs))
			for _, tx := range wantTxs {
				r, rerr := client.GetTransactionReceipt(context.Background(), tx.Hash)
				require.NoError(t, rerr)
				single = append(single, r)
			}

			for _, receipts := range [][]*TransactionReceipt{batch, single} {
				for _, r := range receipts {
					assert.Equal(t, wantNumber, r.BlockNumber)
					for i, l := range r.Logs {
						assert.Equal(t, wantNumber, l.BlockNumber, "log %d blockNumber", i)
						assert.Equal(t, wantHash, l.BlockHash, "log %d blockHash", i)
					}
				}
			}
		})
	}
}

// TestShinzohubAdapter_FixtureHasPrecompileZeroLogBlock pins the RPC defect
// the adapter works around: without stamping, the precompile log keeps the
// RPC's blockNumber 0. If ShinzoHub fixes its precompiles, refresh the
// fixture and this test documents that the stamping became a no-op.
func TestShinzohubAdapter_FixtureHasPrecompileZeroLogBlock(t *testing.T) {
	t.Parallel()
	fx := loadShinzohubFixture(t, 5708560)
	client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), defaultAdapter)

	blockHash, _ := fixtureBlockFields(t, fx.block)
	receipts, err := client.GetBlockReceipts(context.Background(), blockHash)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Len(t, receipts[0].Logs, 1)
	assert.Equal(t, "0x0000000000000000000000000000000000000212", receipts[0].Logs[0].Address)
	assert.Equal(t, "0", receipts[0].Logs[0].BlockNumber, "the default adapter keeps the RPC's zero block number")
}
