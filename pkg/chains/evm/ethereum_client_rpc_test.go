package evm

// Tests for the client's RPC methods against the shared mock JSON-RPC server
// (see helpers_test.go): GetLatestBlockNumber, GetNetworkID, GetBlockByNumber,
// GetTransactionReceipt and GetBlockReceipts.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- RPC methods with mock server ---

func TestGetLatestBlockNumber_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			// Return a full block header with all required fields
			return map[string]any{
				constants.NumberFieldName: "0x64",
				constants.HashFieldName:   "0x0000000000000000000000000000000000000000000000000000000000000001",
				ParentHashFieldName:       "0x0000000000000000000000000000000000000000000000000000000000000000",
				NonceFieldName:            "0x0000000000000000",
				Sha3UnclesFieldName:       "0x0000000000000000000000000000000000000000000000000000000000000000",
				LogsBloomFieldName:        "0x" + fmt.Sprintf("%0512x", 0),
				TransactionsRootFieldName: "0x0000000000000000000000000000000000000000000000000000000000000000",
				StateRootFieldName:        "0x0000000000000000000000000000000000000000000000000000000000000000",
				ReceiptsRootFieldName:     "0x0000000000000000000000000000000000000000000000000000000000000000",
				MinerFieldName:            "0x0000000000000000000000000000000000000000",
				DifficultyFieldName:       "0x0",
				ExtraDataFieldName:        "0x",
				GasLimitFieldName:         "0x1000000",
				GasUsedFieldName:          "0x0",
				TimestampFieldName:        "0x0",
				MixHashFieldName:          "0x0000000000000000000000000000000000000000000000000000000000000000",
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	blockNum, err := client.GetLatestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, blockNum)
	assert.Equal(t, int64(testBlockNumber), blockNum.Int64())
}

func TestGetNetworkID_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethNetVersion:
			return "1", nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	networkID, err := client.GetNetworkID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(1), networkID)
}

// --- GetBlockByNumber with mock server ---

func TestGetBlockByNumber_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fullBlockResponse(testBlockNumberHex, nil), nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(testBlockNumber))
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, strconv.Itoa(testBlockNumber), block.Number)
}

func TestGetBlockByNumber_Error(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return nil, fmt.Errorf("block not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetBlockByNumber(context.Background(), big.NewInt(999999))
	assert.Error(t, err)
}

// --- GetHeaderByHash with mock server ---

func TestGetHeaderByHash_Success(t *testing.T) {
	t.Parallel()
	servedHashCh := make(chan string, 1)
	servedFullTxCh := make(chan bool, 1)
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByHash:
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err != nil || len(rawParams) < 2 {
				return nil, fmt.Errorf("bad params")
			}
			var requested string
			_ = json.Unmarshal(rawParams[0], &requested)
			servedHashCh <- requested
			var fullTx bool
			_ = json.Unmarshal(rawParams[1], &fullTx)
			servedFullTxCh <- fullTx
			return fullBlockResponse(testBlockNumberHex, nil), nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	testHash := fakeHash("by-hash")
	header, err := client.GetHeaderByHash(context.Background(), testHash)
	require.NoError(t, err)
	require.NotNil(t, header)
	assert.Equal(t, int64(testBlockNumber), header.Number.Int64())
	assert.Equal(t, strings.ToLower(testHash), <-servedHashCh)
	assert.False(t, <-servedFullTxCh, "the header fetch must not request transaction bodies")
}

func TestGetHeaderByHash_NotFoundIsClassified(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByHash:
			return nil, fmt.Errorf("block not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetHeaderByHash(context.Background(), fakeHash("missing"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get header by hash")
	assert.True(t, errors.IsErrNotFound(err),
		"the by-hash not-found error must keep its message so IsErrNotFound classifies it for the shared retry logic")
}

func TestGetHeaderByHash_InvalidHash(t *testing.T) {
	t.Parallel()
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	for _, hash := range []string{
		"", "0xdeadbeef", strings.TrimPrefix(fakeHash("no-prefix"), "0x"),
		fakeHash("too-long") + "ab",
	} {
		_, err = client.GetHeaderByHash(context.Background(), hash)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "block hash")
		assert.False(t, errors.IsErrNotFound(err),
			"malformed hashes must not classify as not-found: they are caller bugs, not node misses")
	}
}

// --- GetFinalizedBlockNumber with mock server ---

func TestGetFinalizedBlockNumber_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			tag, ok := requestedBlockTag(params)
			if !ok {
				return nil, fmt.Errorf("bad params")
			}
			switch tag {
			case ethGetBlockTagFinalized:
				return fullBlockResponse("0x10", nil), nil
			default:
				return nil, fmt.Errorf("unexpected tag %q", tag)
			}
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	height, err := client.GetFinalizedBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(16), height)
}

func TestGetFinalizedBlockNumber_SafeFallback(t *testing.T) {
	t.Parallel()
	var tagMu sync.Mutex
	var servedTags []string
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			tag, ok := requestedBlockTag(params)
			if !ok {
				return nil, fmt.Errorf("bad params")
			}
			tagMu.Lock()
			servedTags = append(servedTags, tag)
			tagMu.Unlock()
			switch tag {
			case ethGetBlockTagSafe:
				return fullBlockResponse("0x8", nil), nil
			default:
				return nil, fmt.Errorf("%s header not found", tag)
			}
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	height, err := client.GetFinalizedBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(8), height)

	tagMu.Lock()
	defer tagMu.Unlock()
	assert.Equal(t, []string{ethGetBlockTagFinalized, ethGetBlockTagSafe}, servedTags,
		"the safe height must be served only after the finalized tag failed")
}

func TestGetFinalizedBlockNumber_BothTagsFail(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			tag, ok := requestedBlockTag(params)
			if !ok {
				return nil, fmt.Errorf("bad params")
			}
			return nil, fmt.Errorf("%s header not found", tag)
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetFinalizedBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finalized block number unavailable")
}

// --- GetTransactionReceipt with mock server ---

func TestGetTransactionReceipt_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetTransactionReceipt:
			return map[string]any{
				TransactionHashFieldName:       "0x0000000000000000000000000000000000000000000000000000000000000abc",
				TransactionIndexFieldName:      "0x0",
				constants.BlockHashFieldName:   "0x0000000000000000000000000000000000000000000000000000000000000001",
				constants.BlockNumberFieldName: "0x64",
				"from":                         "0x0000000000000000000000000000000000000001",
				"to":                           "0x0000000000000000000000000000000000000002",
				CumulativeGasUsedFieldName:     "0x5208",
				GasUsedFieldName:               "0x5208",
				"contractAddress":              nil,
				"logs":                         []any{},
				LogsBloomFieldName:             "0x" + fmt.Sprintf("%0512x", 0),
				StatusFieldName:                "0x1",
				EffectiveGasPriceFieldName:     "0x4a817c800",
				TypeFieldName:                  "0x0",
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	receipt, err := client.GetTransactionReceipt(context.Background(), "0x0000000000000000000000000000000000000000000000000000000000000abc")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, "1", receipt.Status)
}

func TestGetTransactionReceipt_Error(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetTransactionReceipt:
			return nil, fmt.Errorf("not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetTransactionReceipt(context.Background(), "0xdeadbeef")
	assert.Error(t, err)
}

// --- GetBlockReceipts with mock server ---

func TestGetBlockReceipts_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockReceipts:
			return []any{
				map[string]any{
					TransactionHashFieldName:       "0x0000000000000000000000000000000000000000000000000000000000000abc",
					TransactionIndexFieldName:      "0x0",
					constants.BlockHashFieldName:   "0x0000000000000000000000000000000000000000000000000000000000000001",
					constants.BlockNumberFieldName: "0x64",
					CumulativeGasUsedFieldName:     "0x5208",
					GasUsedFieldName:               "0x5208",
					"logs":                         []any{},
					LogsBloomFieldName:             "0x" + fmt.Sprintf("%0512x", 0),
					StatusFieldName:                "0x1",
					EffectiveGasPriceFieldName:     "0x4a817c800",
					TypeFieldName:                  "0x0",
				},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	receipts, err := client.GetBlockReceipts(context.Background(), fakeHash("pinned"))
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.Equal(t, "1", receipts[0].Status)
}

// TestGetBlockReceipts_RequestPinsBlockHash proves the wire shape: the
// eth_getBlockReceipts request selects the block by its CONTENT HASH, not by
// number, so the receipts always answer for the exact block the caller
// holds, whichever branch it sits on.
func TestGetBlockReceipts_RequestPinsBlockHash(t *testing.T) {
	t.Parallel()

	var receiptParams json.RawMessage
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		if method == ethGetBlockReceipts {
			receiptParams = params
			return []any{}, nil
		}
		return "0x1", nil
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	blockHash := fakeHash("race-closure")
	_, err = client.GetBlockReceipts(context.Background(), blockHash)
	require.NoError(t, err)

	require.NotEmpty(t, receiptParams, "the receipts RPC request must have been made")
	var rawParams []map[string]any
	require.NoError(t, json.Unmarshal(receiptParams, &rawParams))
	require.Len(t, rawParams, 1)
	assert.Equal(t, blockHash, rawParams[0]["blockHash"],
		"eth_getBlockReceipts must be requested with a block-hash selector, not a block number")
}

func TestGetBlockReceipts_Error(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockReceipts:
			return nil, fmt.Errorf("block receipts not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetBlockReceipts(context.Background(), fakeHash("missing"))
	assert.Error(t, err)
}

// --- GetLatestBlockNumber error path ---

func TestGetLatestBlockNumber_Error(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return nil, fmt.Errorf("header error")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() {
		_ = client.Close()
	}()

	_, err = client.GetLatestBlockNumber(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get latest header")
}
