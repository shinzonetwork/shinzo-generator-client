package evm

// Tests for the Celo ClientAdapter against blocks and receipts captured from
// Celo mainnet (testdata/celo): fee-currency (0x7b) and deposit (0x7e) txs
// are remapped onto standard envelopes yet keep the RPC hash, type and
// sender, so every tx still joins its receipt by hash.

import (
	"context"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// celoFixtureBlocks: a small block (deposit, 0x2, 0x7b), a block with legacy
// txs and many logs, and the busiest block of the sampled window.
var celoFixtureBlocks = []int64{81301748, 81301743, 81301744} //nolint:gochecknoglobals // read-only fixture index

func TestAdapterForChain_Celo(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Celo", " celo ", "CELO"} {
		assert.IsType(t, celoClientAdapter{}, adapterForChain(name), "chain name %q", name)
	}
	assert.True(t, adapterForChain("Celo").NeedsRawBlock(), "0x7b/0x7e txs only decode on the raw path")
	_, ok := adapterForChain("Celo").(receiptLogStamper)
	assert.False(t, ok, "Celo logs carry correct block fields; only ShinzoHub restamps them")
}

func TestCeloAdapter_BlockFromMainnetFixtures(t *testing.T) {
	t.Parallel()
	for _, number := range celoFixtureBlocks {
		t.Run(strconv.FormatInt(number, 10), func(t *testing.T) {
			t.Parallel()
			fx := loadFixtureFrom(t, "celo", number)
			client := newRawPathClient(t, newShinzohubFixtureServer(t, fx), adapterForChain("Celo"))

			block, err := client.GetBlockByNumber(context.Background(), big.NewInt(number))
			require.NoError(t, err)

			wantHash, wantTxs := fixtureBlockFields(t, fx.block)
			wantTypes := fixtureTxTypes(t, fx.block)
			wantYParity := fixtureYParities(t, fx.block)
			assert.Equal(t, wantHash, block.Hash)
			require.Len(t, block.Transactions, len(wantTxs))
			for i, tx := range block.Transactions {
				assert.Equal(t, wantTxs[i].Hash, tx.Hash, "tx %d hash must be the RPC hash", i)
				assert.Equal(t, wantTypes[i], tx.Type, "tx %d type must be the RPC type", i)
				assert.True(t, strings.EqualFold(wantTxs[i].From, tx.From), "tx %d from: want %s, got %s", i, wantTxs[i].From, tx.From)
				assert.Equal(t, wantHash, tx.BlockHash, "tx %d blockHash", i)
				assert.Equal(t, wantYParity[i], tx.YParity, "tx %d yParity", i)
			}

			deposit := block.Transactions[0]
			assert.Equal(t, "126", deposit.Type, "the L1 info deposit sits at index 0")
			assert.Equal(t, "0", deposit.GasPrice)
			assert.True(t, strings.EqualFold("0xdeaddeaddeaddeaddeaddeaddeaddeaddead0001", deposit.From))

			receipts, err := client.GetBlockReceipts(context.Background(), wantHash)
			require.NoError(t, err)
			receiptMap := (&Converter{}).buildReceiptMap(receipts)
			for i, tx := range block.Transactions {
				r, ok := receiptMap[tx.Hash]
				require.True(t, ok, "tx %d (type %s) must join its receipt by hash", i, tx.Type)
				assert.NotEmpty(t, r.GasUsed, "tx %d receipt gasUsed", i)
				assert.NotEmpty(t, r.Status, "tx %d receipt status", i)
			}
		})
	}
}

func TestCeloAdapter_FixturesCoverTxTypes(t *testing.T) {
	t.Parallel()
	types := map[string]bool{}
	for _, number := range celoFixtureBlocks {
		for _, typ := range fixtureTxTypes(t, loadFixtureFrom(t, "celo", number).block) {
			types[typ] = true
		}
	}
	for _, typ := range []string{"0", "2", "123", "126"} {
		assert.True(t, types[typ], "fixtures must cover type %s, got %v", typ, types)
	}
}

// TestCeloAdapter_StandardDecodeRejectsCeloTypes pins why Celo needs its own
// decode: go-ethereum rejects the fee-currency envelope.
func TestCeloAdapter_StandardDecodeRejectsCeloTypes(t *testing.T) {
	t.Parallel()
	_, err := rawStandardClientAdapter{}.DecodeTransactions(fixtureRawTxs(t, loadFixtureFrom(t, "celo", celoFixtureBlocks[0]).block))
	require.Error(t, err)
}

func TestCeloAdapter_FeeCurrencyTxKeepsFees(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"type":"0x7b","chainId":"0xa4ec","nonce":"0x12a","gas":"0x3ed5d",
		"maxFeePerGas":"0x3d2fb1be9","maxPriorityFeePerGas":"0x15e3b","to":"0xbedaccbd65230ea441e7955c0f5344acfc2a45e5",
		"value":"0x0","input":"0x","accessList":[],"feeCurrency":"0x0e2a3e05bc9a16f5292a6170456a710cb89c6f72",
		"v":"0x1","r":"0x1","s":"0x2"}`)
	txs, err := celoClientAdapter{}.DecodeTransactions([]json.RawMessage{raw})
	require.NoError(t, err)
	tx := txs[0]
	assert.Equal(t, uint8(ethtypes.DynamicFeeTxType), tx.Type())
	assert.Equal(t, big.NewInt(0x3d2fb1be9), tx.GasFeeCap())
	assert.Equal(t, big.NewInt(0x15e3b), tx.GasTipCap())
	assert.Equal(t, big.NewInt(42220), tx.ChainId())
	assert.Equal(t, uint64(0x12a), tx.Nonce())
}

func TestCeloAdapter_UnknownTypeDoesNotFail(t *testing.T) {
	t.Parallel()
	rawTxs := []json.RawMessage{
		json.RawMessage(`{"type":"0x7a","maxFeePerGas":"0x10","maxPriorityFeePerGas":"0x1","gas":"0x5208"}`),
		json.RawMessage(`{"type":"0x99","gasPrice":"0x7","gas":"0x5208"}`),
	}
	txs, err := celoClientAdapter{}.DecodeTransactions(rawTxs)
	require.NoError(t, err)
	require.Len(t, txs, 2)
	assert.Equal(t, uint8(ethtypes.DynamicFeeTxType), txs[0].Type())
	assert.Equal(t, uint8(ethtypes.LegacyTxType), txs[1].Type())
	assert.Equal(t, big.NewInt(7), txs[1].GasPrice())
}

func TestCeloAdapter_ResolveFrom(t *testing.T) {
	t.Parallel()
	from, ok := celoClientAdapter{}.ResolveFrom(nil, json.RawMessage(`{"from":"0x95727f3ac5802a65d49849e205e88333834fde7f"}`))
	assert.True(t, ok)
	assert.Equal(t, common.HexToAddress("0x95727f3ac5802a65d49849e205e88333834fde7f").Hex(), from)
}

func TestRawFrom(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		raw    json.RawMessage
		want   string
		wantOK bool
	}{
		"present":   {json.RawMessage(`{"from":"0x95727f3ac5802a65d49849e205e88333834fde7f"}`), common.HexToAddress("0x95727f3ac5802a65d49849e205e88333834fde7f").Hex(), true},
		"absent":    {json.RawMessage(`{"type":"0x0"}`), "", false},
		"empty":     {nil, "", false},
		"malformed": {json.RawMessage(`{"from":12}`), "", false},
	}
	for name, tc := range cases {
		from, ok := rawFrom(tc.raw)
		assert.Equal(t, tc.wantOK, ok, name)
		assert.Equal(t, tc.want, from, name)
	}
}

func TestRawTxIdentity(t *testing.T) {
	t.Parallel()
	tx := ethtypes.NewTx(&ethtypes.LegacyTx{GasPrice: big.NewInt(1)})
	decodedHash := tx.Hash().Hex()
	rawHash := "0x14f047532d83e6df25b3ea9d6737c0ae3d91ed02b9acdacedc4b591a0c8ae15a"
	cases := map[string]struct {
		raw              json.RawMessage
		wantHash, wantTy string
	}{
		"raw fields win":   {json.RawMessage(`{"hash":"` + rawHash + `","type":"0x7b"}`), rawHash, "123"},
		"typed path":       {nil, decodedHash, "0"},
		"fields absent":    {json.RawMessage(`{}`), decodedHash, "0"},
		"malformed fields": {json.RawMessage(`{"type":123}`), decodedHash, "0"},
	}
	for name, tc := range cases {
		hash, typ := rawTxIdentity(tx, tc.raw)
		assert.Equal(t, tc.wantHash, hash, name)
		assert.Equal(t, tc.wantTy, typ, name)
	}
}

// fixtureTxTypes returns each fixture tx's type as the client stores it
// (decimal).
func fixtureTxTypes(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	out := make([]string, 0)
	for _, rawTx := range fixtureRawTxs(t, raw) {
		var fields struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(rawTx, &fields))
		n, err := strconv.ParseUint(strings.TrimPrefix(fields.Type, "0x"), 16, 64)
		require.NoError(t, err)
		out = append(out, strconv.FormatUint(n, 10))
	}
	return out
}

func fixtureRawTxs(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var b struct {
		Transactions []json.RawMessage `json:"transactions"`
	}
	require.NoError(t, json.Unmarshal(raw, &b))
	return b.Transactions
}
