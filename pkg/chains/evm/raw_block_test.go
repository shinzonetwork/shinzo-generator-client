package evm

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rpcFixtureServer serves a fixed JSON-RPC response body for every request,
// rewriting the response id to match the request. It counts requests so tests
// can assert how many round-trips a code path made.
type rpcFixtureServer struct {
	*httptest.Server

	requests atomic.Int32
}

func newRPCFixtureServer(t *testing.T, responseBody []byte) *rpcFixtureServer {
	t.Helper()
	s := &rpcFixtureServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		var resp map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(responseBody, &resp))
		resp["id"] = req.ID
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(s.Close)
	return s
}

// rawBlockRPCBody is a minimal eth_getBlockByNumber response containing one
// Bor state-sync tx (type 0x7f, unsigned). go-ethereum rejects it, so the
// client's fallback decode is what these tests exercise.
const rawBlockRPCBody = `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "hash": "0xaaaa000000000000000000000000000000000000000000000000000000000001",
    "number": "0x10",
    "timestamp": "0x64",
    "parentHash": "0xbbbb000000000000000000000000000000000000000000000000000000000001",
    "difficulty": "0x0",
    "gasUsed": "0x5208",
    "gasLimit": "0x1c9c380",
    "baseFeePerGas": "0x3b9aca00",
    "nonce": "0x0000000000000000",
    "miner": "0x0000000000000000000000000000000000000000",
    "size": "0x400",
    "stateRoot": "0xcccc000000000000000000000000000000000000000000000000000000000001",
    "sha3Uncles": "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
    "transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
    "receiptsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
    "logsBloom": "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "extraData": "0x1234",
    "mixHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
    "uncles": [],
    "transactions": [
      {
        "hash": "0xdddd000000000000000000000000000000000000000000000000000000000001",
        "blockHash": "0xaaaa000000000000000000000000000000000000000000000000000000000001",
        "blockNumber": "0x10",
        "from": "0x0000000000000000000000000000000000000000",
        "to": null,
        "value": "0x0",
        "gas": "0x0",
        "gasPrice": "0x0",
        "maxFeePerGas": "0x0",
        "maxPriorityFeePerGas": "0x0",
        "input": "0x",
        "nonce": "0x0",
        "transactionIndex": "0x0",
        "type": "0x7f",
        "v": "0x0",
        "r": "0x0",
        "s": "0x0",
        "yParity": "0x0"
      },
      {
        "hash": "0xdddd000000000000000000000000000000000000000000000000000000000002",
        "blockHash": "0xaaaa000000000000000000000000000000000000000000000000000000000001",
        "blockNumber": "0x10",
        "from": "0x0000000000000000000000000000000000000001",
        "to": "0x0000000000000000000000000000000000000002",
        "value": "0x3e8",
        "gas": "0x5208",
        "gasPrice": "0x3b9aca00",
        "maxFeePerGas": "0x77359400",
        "maxPriorityFeePerGas": "0x3b9aca00",
        "input": "0x",
        "nonce": "0x5",
        "transactionIndex": "0x1",
        "type": "0x2",
        "chainId": "0x89",
        "accessList": [{"address": "0x0000000000000000000000000000000000000003", "storageKeys": ["0xaa"]}],
        "v": "0x1",
        "r": "0x11",
        "s": "0x22",
        "yParity": "0x1"
      },
      {
        "hash": "0xdddd000000000000000000000000000000000000000000000000000000000003",
        "blockHash": "0xaaaa000000000000000000000000000000000000000000000000000000000001",
        "blockNumber": "0x10",
        "from": "0x0000000000000000000000000000000000000004",
        "to": "0x0000000000000000000000000000000000000005",
        "value": "0x0",
        "gas": "0x5208",
        "gasPrice": "0x3b9aca00",
        "maxFeePerGas": "0x0",
        "maxPriorityFeePerGas": "0x0",
        "input": "0x",
        "nonce": "0x0",
        "transactionIndex": "0x2",
        "type": "0x0",
        "chainId": "0x89",
        "v": "0x125",
        "r": "0x33",
        "s": "0x44"
      }
    ]
  }
}`

func TestGetBlockByNumber_RawFallbackOnUnsupportedTxType(t *testing.T) {
	t.Parallel()
	srv := newRPCFixtureServer(t, []byte(rawBlockRPCBody))

	c, err := NewEthereumClient(srv.URL, "", "", "")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	// The fixture contains a 0x7f tx: the direct go-ethereum decode must
	// reject it, proving this test exercises the fallback.
	_, err = c.httpClient.BlockByNumber(context.Background(), big.NewInt(16))
	require.ErrorContains(t, err, "transaction type not supported")

	before := srv.requests.Load()
	block, err := c.GetBlockByNumber(context.Background(), big.NewInt(16))
	require.NoError(t, err)
	assert.EqualValues(t, 2, srv.requests.Load()-before, "ethclient attempt + one raw refetch")

	// Block quantities normalized to decimal, blobs without 0x.
	assert.Equal(t, "16", block.Number)
	assert.Equal(t, "100", block.Timestamp)
	assert.Equal(t, "21000", block.GasUsed)
	assert.NotContains(t, block.LogsBloom, "0x")
	assert.Len(t, block.LogsBloom, 512)
	assert.Equal(t, "1234", block.ExtraData)
	assert.Equal(t, "", block.TotalDifficulty)

	require.Len(t, block.Transactions, 3)

	// Bor state-sync deposit: unsigned, from zero address.
	deposit := block.Transactions[0]
	assert.Equal(t, "127", deposit.Type)
	assert.Equal(t, ZeroAddress, deposit.From)
	assert.Equal(t, "", deposit.To) // null to
	assert.Equal(t, "0", deposit.V)
	assert.Equal(t, "", deposit.MaxFeePerGas, "fee fields only for type 2")

	// Dynamic-fee tx keeps EIP-1559 fields, decimal-normalized.
	typed := block.Transactions[1]
	assert.Equal(t, "2", typed.Type)
	assert.Equal(t, "2000000000", typed.MaxFeePerGas)
	assert.Equal(t, "5", typed.Nonce)
	assert.Equal(t, 1, typed.TransactionIndex)
	assert.Equal(t, "137", typed.ChainID)
	require.Len(t, typed.AccessList, 1)
	assert.Equal(t, []string{"0xaa"}, typed.AccessList[0].StorageKeys)

	// Legacy tx: decimal v (0x125 = 293), no fee fields.
	legacy := block.Transactions[2]
	assert.Equal(t, "0", legacy.Type)
	assert.Equal(t, "293", legacy.V)
	assert.Equal(t, "", legacy.MaxFeePerGas)
}

func TestGetBlockByNumber_NoFallbackOnOtherErrors(t *testing.T) {
	t.Parallel()
	srv := newRPCFixtureServer(t, []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`))

	c, err := NewEthereumClient(srv.URL, "", "", "")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.GetBlockByNumber(context.Background(), big.NewInt(16))
	require.Error(t, err)
	assert.EqualValues(t, 1, srv.requests.Load(), "non-tx-type errors must not trigger the raw refetch")
}

func TestHexToDec(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "16", hexToDec("0x10"))
	assert.Equal(t, "0", hexToDec("0x0"))
	assert.Equal(t, "293", hexToDec("0x125"))
	assert.Equal(t, "already-decimal", hexToDec("already-decimal"))
	assert.Equal(t, "100", hexToDec("100"))
	assert.Equal(t, "", hexToDec(""))
}
