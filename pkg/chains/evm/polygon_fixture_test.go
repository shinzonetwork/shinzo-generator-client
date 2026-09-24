package evm

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real Polygon mainnet block 93954400, fetched via eth_getBlockByNumber. It
// contains a genuine Bor state-sync deposit (type 0x7f):
// 0x8b4590d41dc86b0690a58af0285457c525d9c551625c95fee5ef2fd95e23fa9f.
const polygonFixtureFile = "testdata/polygon_block_93954400.json"

const polygonDepositTxHash = "0x8b4590d41dc86b0690a58af0285457c525d9c551625c95fee5ef2fd95e23fa9f"

func loadPolygonFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(polygonFixtureFile)
	require.NoError(t, err)
	return data
}

// nodeReportedYParity indexes the yParity values the Polygon node itself
// returned for each tx in the fixture, keyed by tx hash.
func nodeReportedYParity(t *testing.T) map[string]string {
	t.Helper()
	var resp struct {
		Result struct {
			Transactions []struct {
				Hash    string `json:"hash"`
				Type    string `json:"type"`
				YParity string `json:"yParity"`
			} `json:"transactions"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(loadPolygonFixture(t), &resp))
	m := make(map[string]string)
	for _, tx := range resp.Result.Transactions {
		if tx.YParity != "" {
			m[tx.Hash] = tx.YParity
		}
	}
	return m
}

// TestPolygonFixture_DecodeAndConvert runs the client over the real
// deposit-bearing block: the go-ethereum decode fails on the 0x7f tx, the raw
// fallback decodes all 111 txs, and the Polygon converter emits the deposit
// tx with from=zero-address and yParity "0".
func TestPolygonFixture_DecodeAndConvert(t *testing.T) {
	t.Parallel()
	srv := newRPCFixtureServer(t, loadPolygonFixture(t))

	c, err := NewEthereumClient(srv.URL, "", "", "")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	blockNum := big.NewInt(93954400)

	// Sanity: the direct geth path really does reject this block.
	_, err = c.httpClient.BlockByNumber(context.Background(), blockNum)
	require.ErrorContains(t, err, "transaction type not supported")

	block, err := c.GetBlockByNumber(context.Background(), blockNum)
	require.NoError(t, err)
	assert.Equal(t, "93954400", block.Number)
	require.Len(t, block.Transactions, 111)

	// Cross-check derived yParity against the node-reported value for every
	// tx whose type our typed set covers.
	reported := nodeReportedYParity(t)
	checked := 0
	txPtrs := make([]*Transaction, len(block.Transactions))
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		txPtrs[i] = tx
		nodeHex, ok := reported[tx.Hash]
		switch tx.Type {
		case "1", "2", "126", "127":
			require.True(t, ok, "node should report yParity for typed tx %s (type %s)", tx.Hash, tx.Type)
			assert.Equal(t, hexToDec(nodeHex), yParity(tx.Type, tx.V), "tx %s", tx.Hash)
			checked++
		}
	}
	assert.Positive(t, checked, "expected typed txs to cross-check")

	// Full converter pass with the Polygon variant.
	converter := NewConverter(polygonConfig())
	result, err := converter.Convert(context.Background(), &BlockBundle{
		Block:        block,
		Transactions: txPtrs,
	})
	require.NoError(t, err)

	var txDocs []map[string]any
	for _, g := range result.Groups {
		if g.Collection == "Polygon__Mainnet__Transaction" {
			txDocs = g.Docs
		}
	}
	require.Len(t, txDocs, 111)

	deposit := txDocByHash(t, txDocs, polygonDepositTxHash)
	assert.Equal(t, ZeroAddress, deposit["from"], "unsigned deposit tx falls back to the zero address")
	assert.Equal(t, "127", deposit["type"])
	assert.Equal(t, "0", deposit["yParity"])
	assert.Equal(t, int64(93954400), deposit["blockNumber"])
	for _, stripped := range []string{"status", "cumulativeGasUsed", "effectiveGasPrice", "gasUsed"} {
		assert.NotContains(t, deposit, stripped)
	}
}
