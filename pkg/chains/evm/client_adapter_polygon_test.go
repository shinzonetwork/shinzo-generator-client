package evm

// Tests for the Polygon ClientAdapter against blocks and receipts captured
// from Polygon Amoy (testdata/polygon): standard envelopes keep the RPC hash,
// sender and yParity, and a Bor state-sync tx (0x7f), which go-ethereum's
// typed decode rejects, is remapped yet still joins its receipt by hash.

import (
	"context"
	"encoding/json"
	"maps"
	"math/big"
	"strconv"
	"strings"
	"testing"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// polygonFixtureBlocks: the busiest block of the sampled Amoy window (four
// 0x2 txs, 15 logs), a block with one 0x2 contract call (6 logs), and an
// empty block.
var polygonFixtureBlocks = []int64{51992266, 51903166, 51992252} //nolint:gochecknoglobals // read-only fixture index

// polygonStateSyncHash is the hash of the synthetic state-sync tx appended
// by polygonStateSyncFixture.
const polygonStateSyncHash = "0x5ca1ab1e00000000000000000000000000000000000000000000000000000001"

func TestAdapterForChain_Polygon(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Polygon", " polygon ", "POLYGON"} {
		assert.IsType(t, polygonClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	assert.True(t, adapterForChain("Polygon").NeedsRawBlock(), "state-sync txs only decode on the raw path")
	_, ok := adapterForChain("Polygon").(receiptLogStamper)
	assert.False(t, ok, "Polygon logs carry correct block fields; only ShinzoHub restamps them")
}

func TestPolygonAdapter_BlockFromAmoyFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range polygonFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			assertPolygonBlockJoins(t, loadFixtureFrom(t, "polygon", number))
		})
	}
}

// TestPolygonAdapter_FixturesRecomputeOnTypedPath pins that the block hash
// is not why the raw path is needed: Bor's header hash recomputes exactly.
func TestPolygonAdapter_FixturesRecomputeOnTypedPath(t *testing.T) {
	t.Parallel()
	for _, number := range polygonFixtureBlocks {
		fx := loadFixtureFrom(t, "polygon", number)
		client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), defaultAdapter)

		block, err := client.GetBlockByNumber(context.Background(), big.NewInt(number))
		require.NoError(t, err, "block %d", number)
		wantHash, _ := fixtureBlockFields(t, fx.block)
		assert.Equal(t, wantHash, block.Hash, "block %d", number)
	}
}

func TestPolygonAdapter_StateSyncTx(t *testing.T) {
	t.Parallel()
	fx := polygonStateSyncFixture(t)
	block := assertPolygonBlockJoins(t, fx)

	sync := block.Transactions[len(block.Transactions)-1]
	assert.Equal(t, polygonStateSyncHash, sync.Hash, "the RPC hash is kept, not the remapped envelope's")
	assert.Equal(t, strconv.Itoa(polygonStateSyncTxType), sync.Type, "the RPC type is kept")
	assert.True(t, strings.EqualFold("0x0000000000000000000000000000000000000000", sync.From), "state syncs are from the zero address")
	assert.Equal(t, "0", sync.GasPrice)
	assert.Empty(t, sync.YParity, "state syncs are unsigned")
}

// TestPolygonAdapter_DefaultAdapterRejectsStateSync pins why the raw path is
// mandatory: the typed ethclient decode fails the whole block.
func TestPolygonAdapter_DefaultAdapterRejectsStateSync(t *testing.T) {
	t.Parallel()
	fx := polygonStateSyncFixture(t)
	client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), defaultAdapter)

	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(fx.number))
	require.Error(t, err)
}

func TestPolygonAdapter_UnknownTypeDoesNotFail(t *testing.T) {
	t.Parallel()
	rawTxs := []json.RawMessage{
		json.RawMessage(`{"type":"0x7f","gas":"0x0","to":"0x0000000000000000000000000000000000001001","input":"0x"}`),
		json.RawMessage(`{"type":"0x99","maxFeePerGas":"0x10","maxPriorityFeePerGas":"0x1","gas":"0x5208"}`),
	}
	txs, err := polygonClientAdapter{}.DecodeTransactions(rawTxs)
	require.NoError(t, err)
	require.Len(t, txs, 2)
	assert.Equal(t, uint8(ethtypes.LegacyTxType), txs[0].Type())
	assert.Zero(t, txs[0].GasPrice().Sign(), "state syncs pay no gas")
	assert.Equal(t, uint8(ethtypes.DynamicFeeTxType), txs[1].Type())
}

// assertPolygonBlockJoins converts fx on the Polygon adapter and checks that
// the RPC block hash and every tx's hash, sender and yParity are kept and
// that every tx joins its receipt by hash.
func assertPolygonBlockJoins(t *testing.T, fx shinzohubFixture) *Block {
	t.Helper()
	client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), adapterForChain("Polygon"))

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(fx.number))
	require.NoError(t, err)

	wantHash, wantTxs := fixtureBlockFields(t, fx.block)
	rawTxs := fixtureRawTxs(t, fx.block)
	assert.Equal(t, wantHash, block.Hash)
	assert.Equal(t, strconv.FormatInt(fx.number, 10), block.Number)
	require.Len(t, block.Transactions, len(wantTxs))
	for i, tx := range block.Transactions {
		assert.Equal(t, wantTxs[i].Hash, tx.Hash, "tx %d hash must be the RPC hash", i)
		assert.True(t, strings.EqualFold(wantTxs[i].From, tx.From), "tx %d from: want %s, got %s", i, wantTxs[i].From, tx.From)
		assert.Equal(t, wantHash, tx.BlockHash, "tx %d blockHash", i)
		assert.Equal(t, rawYParity(rawTxs[i]), tx.YParity, "tx %d yParity", i)
	}

	receipts, err := client.GetBlockReceipts(context.Background(), wantHash)
	require.NoError(t, err)
	receiptMap := (&Converter{}).buildReceiptMap(receipts)
	for i, tx := range block.Transactions {
		r, ok := receiptMap[tx.Hash]
		require.True(t, ok, "tx %d must join its receipt by hash", i)
		assert.NotEmpty(t, r.Status, "tx %d receipt status", i)
		for j, l := range r.Logs {
			assert.Equal(t, block.Number, l.BlockNumber, "tx %d log %d blockNumber", i, j)
			assert.Equal(t, wantHash, l.BlockHash, "tx %d log %d blockHash", i, j)
		}
	}
	return block
}

// polygonStateSyncFixture appends a Bor state-sync tx and its receipt (one
// log) to the busiest Amoy fixture. Amoy produced no state sync in the
// probed window, so the tx follows Bor's PIP-74 shape: type 0x7f, from the
// zero address to the state receiver, no gas and no signature.
func polygonStateSyncFixture(t *testing.T) shinzohubFixture {
	t.Helper()
	fx := loadFixtureFrom(t, "polygon", polygonFixtureBlocks[0])

	var block map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fx.block, &block))
	var txs []json.RawMessage
	require.NoError(t, json.Unmarshal(block["transactions"], &txs))
	var blockHash, blockNumber string
	require.NoError(t, json.Unmarshal(block["hash"], &blockHash))
	require.NoError(t, json.Unmarshal(block["number"], &blockNumber))
	index := "0x" + strconv.FormatInt(int64(len(txs)), 16)

	syncTx, err := json.Marshal(map[string]any{
		"type": "0x7f", "hash": polygonStateSyncHash, "blockHash": blockHash, "blockNumber": blockNumber,
		"transactionIndex": index, "from": "0x0000000000000000000000000000000000000000",
		"to": "0x0000000000000000000000000000000000001001", "nonce": "0x0", "gas": "0x0",
		"gasPrice": "0x0", "value": "0x0", "input": "0x", "v": "0x0", "r": "0x0", "s": "0x0",
	})
	require.NoError(t, err)
	block["transactions"], err = json.Marshal(append(txs, syncTx))
	require.NoError(t, err)
	fx.block, err = json.Marshal(block)
	require.NoError(t, err)

	// The receipt reuses the last receipt's shape with the state-sync identity.
	var receipts []map[string]any
	require.NoError(t, json.Unmarshal(fx.receipts, &receipts))
	require.NotEmpty(t, receipts)
	last := receipts[len(receipts)-1]
	syncReceipt := maps.Clone(last)
	log := maps.Clone(last["logs"].([]any)[0].(map[string]any))
	log["transactionHash"], log["transactionIndex"], log["logIndex"] = polygonStateSyncHash, index, "0x63"
	syncReceipt["type"], syncReceipt["transactionHash"], syncReceipt["transactionIndex"] = "0x7f", polygonStateSyncHash, index
	syncReceipt["from"], syncReceipt["to"] = "0x0000000000000000000000000000000000000000", "0x0000000000000000000000000000000000001001"
	syncReceipt["gasUsed"], syncReceipt["effectiveGasPrice"], syncReceipt["logs"] = "0x0", "0x0", []any{log}
	fx.receipts, err = json.Marshal(append(receipts, syncReceipt))
	require.NoError(t, err)
	return fx
}
