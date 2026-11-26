package evm

// Conformance of the raw pipeline across the EVM chains captured in
// testdata/<chain>/. Every fixture is a real capture: block_<n>.json is the
// .result of eth_getBlockByNumber(n, true) and receipts_<n>.json the .result
// of eth_getBlockReceipts(n).
//
// Each block is decoded a second time here, independently of client_wire.go,
// and the converted output must match the node's response field by field.
// The per-chain rows pin the quirk that once required a dedicated
// ClientAdapter, so a regression in the shared passthrough shows up as the
// chain it breaks. Adding a chain means adding fixtures and one row.

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chainConformance is one captured chain and what its fixtures must show.
type chainConformance struct {
	chain string
	// why is the RPC difference that used to need a ClientAdapter and that
	// the passthrough must keep handling.
	why string
	// typesSeen are tx types (decimal) that must appear across the chain's
	// fixtures, so the quirk stays exercised.
	typesSeen []string
	// zeroBlockNumberLogs is how many fixture logs the node served with
	// blockNumber 0x0, each of which must be stamped from its receipt.
	zeroBlockNumberLogs int
}

var chainConformanceCases = []chainConformance{ //nolint:gochecknoglobals // read-only test table
	{
		chain: "avalanche",
		why:   "coreth headers hash fields go-ethereum's header lacks; the RPC block hash must be kept",
	},
	{
		chain:     "celo",
		why:       "fee-currency (0x7b) and deposit (0x7e) envelopes; legacy txs signed over the Celo payload",
		typesSeen: []string{"123", "126"},
	},
	{
		chain: "kaia",
		why:   "Kaia-native txs served as 0x0 with a Kaia-RLP signature and hash; RPC hash and from must be kept",
	},
	{
		chain: "kava",
		why:   "cosmos-evm block hash is the CometBFT header hash",
	},
	{
		chain:     "polygon",
		why:       "Bor state-sync txs (0x7f) are unsigned and unknown to go-ethereum",
		typesSeen: []string{"127", "4"},
	},
	{
		chain:               "shinzohub",
		why:                 "cosmos-evm block hash; precompile logs carry blockNumber 0x0",
		zeroBlockNumberLogs: 2,
	},
}

// wireBlock and wireTx mirror the node's response with plain strings, so the
// expectations below never share decoding code with the pipeline under test.
type wireBlock struct {
	Hash         string   `json:"hash"`
	Number       string   `json:"number"`
	Transactions []wireTx `json:"transactions"`
}

type wireTx struct {
	Hash                 string  `json:"hash"`
	From                 string  `json:"from"`
	To                   *string `json:"to"`
	Type                 string  `json:"type"`
	Value                string  `json:"value"`
	Gas                  string  `json:"gas"`
	GasPrice             string  `json:"gasPrice"`
	MaxFeePerGas         string  `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string  `json:"maxPriorityFeePerGas"`
	Input                string  `json:"input"`
	Nonce                string  `json:"nonce"`
	ChainID              string  `json:"chainId"`
	V                    string  `json:"v"`
	R                    string  `json:"r"`
	S                    string  `json:"s"`
	YParity              string  `json:"yParity"`
}

type wireReceipt struct {
	TransactionHash string `json:"transactionHash"`
	Status          string `json:"status"`
	Root            string `json:"root"`
	GasUsed         string `json:"gasUsed"`
	Logs            []struct {
		BlockNumber string `json:"blockNumber"`
	} `json:"logs"`
}

// wireDecimal renders a hex (or decimal) quantity as a decimal string, ""
// when absent.
func wireDecimal(t *testing.T, q string) string {
	t.Helper()
	if q == "" {
		return ""
	}
	n, ok := new(big.Int).SetString(strings.TrimPrefix(strings.ToLower(q), "0x"), 16)
	if !strings.HasPrefix(strings.ToLower(q), "0x") {
		n, ok = new(big.Int).SetString(q, 10)
	}
	require.True(t, ok, "quantity %q", q)
	return n.String()
}

// wantGasPrice is the fee rule restated independently: the wire gasPrice when
// present, else maxFeePerGas for 0x2/0x3, else empty.
func wantGasPrice(t *testing.T, tx wireTx) string {
	t.Helper()
	if tx.GasPrice != "" {
		return wireDecimal(t, tx.GasPrice)
	}
	if typ := wireDecimal(t, tx.Type); (typ == "2" || typ == "3") && tx.MaxFeePerGas != "" {
		return wireDecimal(t, tx.MaxFeePerGas)
	}
	return ""
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test fixture path built from a fixed table
	require.NoError(t, err)
	return raw
}

// fixtureHeights lists the captured block heights for a chain, ascending.
func fixtureHeights(t *testing.T, chain string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", chain, "block_*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no fixtures for %s", chain)
	heights := make([]string, 0, len(files))
	for _, f := range files {
		heights = append(heights, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "block_"), ".json"))
	}
	sort.Strings(heights)
	return heights
}

func TestChainConformance(t *testing.T) {
	t.Parallel()
	for _, tc := range chainConformanceCases {
		t.Run(tc.chain, func(t *testing.T) {
			t.Parallel()
			typesSeen := map[string]bool{}
			zeroBlockNumberLogs := 0
			for _, height := range fixtureHeights(t, tc.chain) {
				t.Run(height, func(t *testing.T) {
					seen, zeroLogs := assertBlockConforms(t, tc, height)
					for typ := range seen {
						typesSeen[typ] = true
					}
					zeroBlockNumberLogs += zeroLogs
				})
			}
			for _, typ := range tc.typesSeen {
				assert.True(t, typesSeen[typ], "%s fixtures must exercise tx type %s (%s)", tc.chain, typ, tc.why)
			}
			assert.Equal(t, tc.zeroBlockNumberLogs, zeroBlockNumberLogs, "%s: logs served with blockNumber 0x0", tc.chain)
		})
	}
}

// assertBlockConforms checks one captured block end to end and returns the
// tx types it holds and how many of its logs the node served with
// blockNumber 0x0.
func assertBlockConforms(t *testing.T, tc chainConformance, height string) (map[string]bool, int) {
	t.Helper()
	rawBlock := readFixture(t, filepath.Join("testdata", tc.chain, "block_"+height+".json"))
	rawReceipts := readFixture(t, filepath.Join("testdata", tc.chain, "receipts_"+height+".json"))

	var wb wireBlock
	require.NoError(t, json.Unmarshal(rawBlock, &wb))
	var wireReceipts []wireReceipt
	require.NoError(t, json.Unmarshal(rawReceipts, &wireReceipts))
	var receiptObjects []json.RawMessage
	require.NoError(t, json.Unmarshal(rawReceipts, &receiptObjects))

	block, err := defaultAdapter.ConvertBlock(rawBlock)
	require.NoError(t, err, "%s: the default adapter must convert the block (%s)", tc.chain, tc.why)

	// Header identity is the node's.
	assert.Equal(t, strings.ToLower(wb.Hash), strings.ToLower(block.Hash), "block hash (%s)", tc.why)
	assert.Equal(t, wireDecimal(t, wb.Number), block.Number)
	require.Len(t, block.Transactions, len(wb.Transactions))

	types := map[string]bool{}
	txs := make([]*Transaction, len(block.Transactions))
	for i := range block.Transactions {
		got, want := &block.Transactions[i], wb.Transactions[i]
		txs[i] = got
		typ := wireDecimal(t, want.Type)
		if typ == "" {
			typ = "0"
		}
		types[typ] = true
		label := fmt.Sprintf("tx %d %s", i, want.Hash)

		assert.Equal(t, strings.ToLower(want.Hash), strings.ToLower(got.Hash), "%s hash", label)
		assert.Equal(t, common.HexToAddress(want.From).Hex(), got.From, "%s from", label)
		assert.Equal(t, typ, got.Type, "%s type", label)
		assert.Equal(t, want.YParity, got.YParity, "%s yParity", label)
		if want.To == nil {
			assert.Empty(t, got.To, "%s to (contract creation)", label)
		} else {
			assert.Equal(t, common.HexToAddress(*want.To).Hex(), got.To, "%s to", label)
		}
		assert.Equal(t, wireDecimal(t, want.Value), got.Value, "%s value", label)
		assert.Equal(t, wireDecimal(t, want.Gas), got.Gas, "%s gas", label)
		assert.Equal(t, wantGasPrice(t, want), got.GasPrice, "%s gasPrice", label)
		assert.Equal(t, wireDecimal(t, want.MaxFeePerGas), got.MaxFeePerGas, "%s maxFeePerGas", label)
		assert.Equal(t, wireDecimal(t, want.MaxPriorityFeePerGas), got.MaxPriorityFeePerGas, "%s maxPriorityFeePerGas", label)
		assert.Equal(t, strings.ToLower(want.Input), got.Input, "%s input", label)
		assert.Equal(t, wireDecimal(t, want.Nonce), got.Nonce, "%s nonce", label)
		assert.Equal(t, wireDecimal(t, want.ChainID), got.ChainID, "%s chainId", label)
		assert.Equal(t, wireDecimal(t, want.V), got.V, "%s v", label)
		assert.Equal(t, wireDecimal(t, want.R), got.R, "%s r", label)
		assert.Equal(t, wireDecimal(t, want.S), got.S, "%s s", label)
		assert.Equal(t, strings.ToLower(block.Hash), strings.ToLower(got.BlockHash), "%s blockHash", label)
		assert.Equal(t, block.Number, got.BlockNumber, "%s blockNumber", label)
		assert.Equal(t, i, got.TransactionIndex, "%s index", label)
	}

	// Receipts: one per tx, all from this block, all joining their tx.
	require.Len(t, receiptObjects, len(block.Transactions), "one receipt per tx")
	receipts := make([]*TransactionReceipt, len(receiptObjects))
	zeroBlockNumberLogs := 0
	for i, raw := range receiptObjects {
		r, err := convertRawReceipt(raw)
		require.NoError(t, err, "receipt %d", i)
		receipts[i] = r
		for j, log := range r.Logs {
			if wireDecimal(t, wireReceipts[i].Logs[j].BlockNumber) == "0" {
				zeroBlockNumberLogs++
			}
			assert.Equal(t, block.Number, log.BlockNumber, "receipt %d log %d blockNumber", i, j)
			assert.Equal(t, strings.ToLower(block.Hash), strings.ToLower(log.BlockHash), "receipt %d log %d blockHash", i, j)
			assert.Equal(t, strings.ToLower(r.TransactionHash), strings.ToLower(log.TransactionHash), "receipt %d log %d txHash", i, j)
		}
	}
	assert.True(t, receiptHashesMatch(receipts, block.Hash), "every receipt belongs to the block")
	assert.True(t, batchCoversTransactions(txs, receipts),
		"the pinned batch must cover every tx, or the block falls back to per-tx receipt fetches")

	// The merge carries each receipt's outcome onto its tx.
	mergeReceiptsOntoTransactions(txs, receipts)
	byHash := make(map[string]wireReceipt, len(wireReceipts))
	for _, wr := range wireReceipts {
		byHash[strings.ToLower(wr.TransactionHash)] = wr
	}
	for _, tx := range txs {
		wr, ok := byHash[strings.ToLower(tx.Hash)]
		require.True(t, ok, "tx %s has a receipt", tx.Hash)
		wantStatus := wr.Root != "" || wireDecimal(t, wr.Status) == "1"
		assert.Equal(t, wantStatus, tx.Status, "tx %s status", tx.Hash)
		assert.Equal(t, wireDecimal(t, wr.GasUsed), tx.GasUsed, "tx %s gasUsed", tx.Hash)
	}
	return types, zeroBlockNumberLogs
}
