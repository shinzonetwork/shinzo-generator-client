package evm

// The single-path client suite: every chain fetches the raw
// eth_getBlockByNumber response and converts it through the adapter's
// tolerant passthrough, so there is no typed/raw duality to test. The
// carried identity assertions prove the stored identity equals the RPC
// value: the node's own hash, type, from, and yParity.

import (
	"context"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

// rawBlockHash is a deliberately non-recomputable block hash: no header
// derived from the standard fields could ever produce it, so any assertion
// matching it proves the hash came from the RPC response.
const rawBlockHash = "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

// rawTxHash is the wire transaction hash the fixture carries: any assertion
// matching this value proves the hash came from the response's own hash
// field.
const rawTxHash = "0x0000000000000000000000000000000000000000000000000000000000000abc"

// zksyncStyleTx builds a raw 0x71 transaction object as ZKsync Era returns
// it inside eth_getBlockByNumber, with an explicit from and yParity. Under
// tolerant passthrough this envelope lands with zero per-chain code.
func zksyncStyleTx() map[string]any {
	return map[string]any{
		"type":     "0x71",
		"hash":     rawTxHash,
		"nonce":    "0x1",
		"gasPrice": "0x3b9aca00",
		"gas":      "0x5208",
		"to":       "0x0000000000000000000000000000000000000002",
		"value":    "0x1",
		"input":    "0xdeadbeef",
		"chainId":  "0x144",
		"from":     "0x1111111111111111111111111111111111111111",
		"yParity":  "0x0",
		"v":        "0x1",
		"r":        "0x2",
		"s":        "0x3",
	}
}

// rawZksyncBlockResponse builds a raw block fixture carrying the 0x71 tx,
// with size and total difficulty set for RPC-provided-value assertions.
func rawZksyncBlockResponse() map[string]any {
	return rawZksyncBlockResponseWithTxs([]any{zksyncStyleTx()})
}

// rawZksyncBlockResponseWithTxs builds the same block fixture with a
// caller-provided transactions array.
func rawZksyncBlockResponseWithTxs(txs []any) map[string]any {
	block := fullBlockResponse(testBlockNumberHex, txs)
	block[constants.HashFieldName] = rawBlockHash
	block[TransactionsRootFieldName] = "0x1111111111111111111111111111111111111111111111111111111111111111"
	block["size"] = "0x1a4"
	block["totalDifficulty"] = "0x1f4"
	return block
}

func newSinglePathClient(t *testing.T, serverURL string) *EthereumClient {
	t.Helper()
	client, err := NewEthereumClient(t.Context(), serverURL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// --- GetBlockByNumber: the one pipeline ---

func TestGetBlockByNumber_NonStandardEnvelope(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			var args []json.RawMessage
			var paramsOK bool
			if uerr := json.Unmarshal(params, &args); uerr == nil {
				paramsOK = len(args) == 2
			}
			if paramsOK {
				var full bool
				if ferr := json.Unmarshal(args[1], &full); ferr != nil || !full {
					assert.Fail(t, "the fetch must request full transaction objects, params: %s", params)
				}
			} else {
				assert.Fail(t, "unexpected eth_getBlockByNumber params: %s", params)
			}
			return rawZksyncBlockResponse(), nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newSinglePathClient(t, server.URL)

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, block)

	// Block-level values come from the RPC response, not from recomputation.
	assert.Equal(t, rawBlockHash, block.Hash)
	assert.Equal(t, "420", block.Size)
	assert.Equal(t, "500", block.TotalDifficulty)
	assert.Equal(t, strconv.Itoa(testBlockNumber), block.Number)

	require.Len(t, block.Transactions, 1)
	tx := block.Transactions[0]
	// Stored identity equals the RPC value: the node computed the true
	// hash, from, type, and yParity, and passthrough stores exactly those.
	assert.Equal(t, common.HexToAddress("0x1111111111111111111111111111111111111111").Hex(), tx.From,
		"from comes from the response, normalized to the checksummed format")
	assert.Equal(t, "0x0", tx.YParity)
	assert.Equal(t, "113", tx.Type, "0x71 becomes the decimal 113, the wire value")
	assert.Equal(t, "1000000000", tx.GasPrice)
	assert.Equal(t, rawBlockHash, tx.BlockHash)
	assert.Equal(t, strconv.Itoa(testBlockNumber), tx.BlockNumber)
	assert.Equal(t, rawTxHash, tx.Hash, "the response's own hash field is the authoritative identifier")
}

func TestGetBlockByNumber_NotFound(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			return nil, nil //nolint:nilnil // JSON-RPC result: null, the node's response for a missing block
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newSinglePathClient(t, server.URL)

	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.Error(t, err)
	assert.ErrorIs(t, err, ethereum.NotFound)
}

// TestGetBlockByNumber_MissingTxHashIsHardError pins the identity contract:
// a tx object without a usable hash field fails the fetch instead of
// storing a guessed identity, because the receipt merge and the log join
// key on the node's hash.
func TestGetBlockByNumber_MissingTxHashIsHardError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "MissingHash", mutate: func(tx map[string]any) { delete(tx, "hash") }},
		{name: "MalformedHash", mutate: func(tx map[string]any) { tx["hash"] = "0x123" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			txObj := zksyncStyleTx()
			tc.mutate(txObj)
			server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
				if method == ethGetBlockByNumber {
					return rawZksyncBlockResponseWithTxs([]any{txObj}), nil
				}
				return "0x1", nil
			})
			defer server.Close()

			client := newSinglePathClient(t, server.URL)
			_, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
			require.Error(t, err, "a tx object without identity fields is a broken response, not an unknown envelope")
			assert.Contains(t, err.Error(), "missing or malformed hash")
		})
	}
}

// TestGetBlockByNumber_MissingTypeIsLegacy pins the tolerance boundary: the
// type field is not identity, so a tx object without one converts as the
// legacy type the typed path would have produced.
func TestGetBlockByNumber_MissingTypeIsLegacy(t *testing.T) {
	t.Parallel()
	txObj := zksyncStyleTx()
	delete(txObj, "type")
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			return rawZksyncBlockResponseWithTxs([]any{txObj}), nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newSinglePathClient(t, server.URL)
	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.NoError(t, err)
	require.Len(t, block.Transactions, 1)
	assert.Equal(t, "0", block.Transactions[0].Type)
}

func TestGetBlockByNumber_MalformedBlockPropagates(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			return "not-a-block-object", nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client := newSinglePathClient(t, server.URL)

	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode raw block")
}

// --- receipt machinery on the one pipeline ---

// rawPathFailedReceiptResponse builds the receipt a node serves for the
// fixture transaction: a standard-shaped failed receipt keyed by the wire
// hash, so the fetcher's receipt machinery can only match it when the
// conversion carries the node's hash.
func rawPathFailedReceiptResponse() map[string]any {
	return map[string]any{
		"transactionHash":   rawTxHash,
		"transactionIndex":  "0x0",
		"blockHash":         rawBlockHash,
		"blockNumber":       testBlockNumberHex,
		"cumulativeGasUsed": "0x5208",
		"gasUsed":           "0x5208",
		"logsBloom":         "0x" + strings.Repeat("0", 512),
		"logs":              []any{},
		"status":            "0x0",
		"effectiveGasPrice": "0x4a817c800",
	}
}

// TestFetchBlockMergesReceiptOutcome proves the fetcher's receipt machinery
// works end to end on the single pipeline: the batch receipt keyed by the
// node's wire hash is accepted as covering the block, and the failed
// execution outcome overrides the conversion default. When the conversion
// carries any other hash, the batch is rejected as incomplete coverage and
// the per-tx lookups with that hash resolve to nothing.
func TestFetchBlockMergesReceiptOutcome(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return rawZksyncBlockResponse(), nil
		case ethGetBlockReceipts:
			return []any{rawPathFailedReceiptResponse()}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client := newSinglePathClient(t, server.URL)
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), testBlockNumber)
	require.NoError(t, err)
	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)

	require.Len(t, bundle.Transactions, 1)
	tx := bundle.Transactions[0]
	assert.Equal(t, rawTxHash, tx.Hash, "the receipt merge must key on the node's hash")
	assert.False(t, tx.Status, "the receipt's failed outcome must override the conversion default")
	assert.Equal(t, "21000", tx.GasUsed)
	assert.Equal(t, "21000", tx.CumulativeGasUsed)
	assert.Equal(t, "20000000000", tx.EffectiveGasPrice)

	require.Len(t, bundle.Receipts, 1)
	assert.Equal(t, rawTxHash, bundle.Receipts[0].TransactionHash)
}

func TestTransaction_JSONOmitsEmptyYParity(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(fakeTx("0xabc"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "yParity",
		"a tx without a yParity from the RPC omits the field and the JSON output stays byte-identical")

	txWithParity := fakeTx("0xabc")
	txWithParity.YParity = "0x1"
	data, err = json.Marshal(txWithParity)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"yParity":"0x1"`)
}
