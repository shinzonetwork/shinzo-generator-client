package evm

// Tests for the Polygon ClientAdapter against blocks and receipts captured
// from Polygon Amoy and mainnet (testdata/polygon): standard envelopes keep
// the RPC hash, sender and yParity, and a Bor state-sync tx (0x7f), which
// go-ethereum's typed decode rejects, is remapped yet still joins its
// receipt by hash.

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

// polygonFixtureBlocks are Amoy blocks of standard envelopes only: the
// busiest block of the sampled window (four 0x2 txs, 15 logs), a block with
// one 0x2 contract call (6 logs), and an empty block.
var polygonFixtureBlocks = []int64{51992266, 51903166, 51992252} //nolint:gochecknoglobals // read-only fixture index

// polygonStateSyncBlock is a mainnet sprint-boundary block: 80 standard txs
// (0x0, 0x2) followed by one state-sync tx with 4 logs, 871 logs in all.
const (
	polygonStateSyncBlock = 96744272
	polygonStateSyncHash  = "0xf449efdd5d2074c0e79c62d412ff24a93a8859306b31633af07cc07fb2ebb794"
)

func TestAdapterForChain_Polygon(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Polygon", " polygon ", "POLYGON"} {
		assert.IsType(t, polygonClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	assert.True(t, adapterForChain("Polygon").NeedsRawBlock(), "state-sync txs only decode on the raw path")
	_, ok := adapterForChain("Polygon").(receiptLogStamper)
	assert.False(t, ok, "Polygon logs carry correct block fields; only ShinzoHub restamps them")
}

func TestPolygonAdapter_BlockFromFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range append([]int64{polygonStateSyncBlock}, polygonFixtureBlocks...) {
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
	block := assertPolygonBlockJoins(t, loadFixtureFrom(t, "polygon", polygonStateSyncBlock))

	sync := block.Transactions[len(block.Transactions)-1]
	assert.Equal(t, polygonStateSyncHash, sync.Hash, "the RPC hash is kept, not the remapped envelope's")
	assert.Equal(t, strconv.Itoa(polygonStateSyncTxType), sync.Type, "the RPC type is kept")
	assert.True(t, strings.EqualFold("0x0000000000000000000000000000000000000000", sync.From), "state syncs are from the zero address")
	assert.Equal(t, "0", sync.GasPrice)
}

// TestPolygonAdapter_DefaultAdapterRejectsStateSync pins why the raw path is
// mandatory: the typed ethclient decode fails the whole block.
func TestPolygonAdapter_DefaultAdapterRejectsStateSync(t *testing.T) {
	t.Parallel()
	fx := loadFixtureFrom(t, "polygon", polygonStateSyncBlock)
	client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), defaultAdapter)

	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(polygonStateSyncBlock))
	require.ErrorContains(t, err, "transaction type not supported")
}

func TestPolygonAdapter_UnknownTypeDoesNotFail(t *testing.T) {
	t.Parallel()
	rawTxs := []json.RawMessage{
		json.RawMessage(`{"type":"0x7f","gas":"0x0","gasPrice":"0x0","maxFeePerGas":"0x0","to":"0x0000000000000000000000000000000000000000","input":"0x"}`),
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
