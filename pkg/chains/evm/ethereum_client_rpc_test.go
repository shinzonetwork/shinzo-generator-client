package evm

// Tests for the client's RPC methods against the shared mock JSON-RPC server
// (see helpers_test.go): GetLatestBlockNumber, GetNetworkID, GetBlockByNumber,
// GetTransactionReceipt, GetBlockReceipts and the GetLatestBlock retry paths.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"testing"

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
				NumberFieldValue:         testBlockNumberHex,
				HashKeyValue:             "0x0000000000000000000000000000000000000000000000000000000000000001",
				ParentHashKeyValue:       "0x0000000000000000000000000000000000000000000000000000000000000000",
				NonceKeyValue:            "0x0000000000000000",
				Sha3UnclesKeyValue:       "0x0000000000000000000000000000000000000000000000000000000000000000",
				LogsBloomKeyValue:        "0x" + fmt.Sprintf("%0512x", 0),
				TransactionsRootKeyValue: "0x0000000000000000000000000000000000000000000000000000000000000000",
				StateRootKeyValue:        "0x0000000000000000000000000000000000000000000000000000000000000000",
				ReceiptsRootKeyValue:     "0x0000000000000000000000000000000000000000000000000000000000000000",
				MinerKeyValue:            "0x0000000000000000000000000000000000000000",
				DifficultyKeyValue:       "0x0",
				ExtraDataKeyValue:        "0x",
				GasLimitKeyValue:         "0x1000000",
				GasUsedKeyValue:          "0x0",
				TimestampKeyValue:        "0x0",
				MixHashKeyValue:          "0x0000000000000000000000000000000000000000000000000000000000000000",
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

// --- GetTransactionReceipt with mock server ---

func TestGetTransactionReceipt_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetTransactionReceipt:
			return map[string]any{
				TransactionHashKeyValue:   "0x0000000000000000000000000000000000000000000000000000000000000abc",
				TransactionIndexKeyValue:  "0x0",
				BlockHashKeyValue:         "0x0000000000000000000000000000000000000000000000000000000000000001",
				BlockNumberKeyValue:       testBlockNumberHex,
				"from":                    "0x0000000000000000000000000000000000000001",
				"to":                      "0x0000000000000000000000000000000000000002",
				CumulativeGasUsedKeyValue: "0x5208",
				GasUsedKeyValue:           "0x5208",
				"contractAddress":         nil,
				"logs":                    []any{},
				LogsBloomKeyValue:         "0x" + fmt.Sprintf("%0512x", 0),
				StatusKeyValue:            "0x1",
				EffectiveGasPriceKeyValue: "0x4a817c800",
				TypeKeyValue:              "0x0",
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
					TransactionHashKeyValue:   "0x0000000000000000000000000000000000000000000000000000000000000abc",
					TransactionIndexKeyValue:  "0x0",
					BlockHashKeyValue:         "0x0000000000000000000000000000000000000000000000000000000000000001",
					BlockNumberKeyValue:       testBlockNumberHex,
					CumulativeGasUsedKeyValue: "0x5208",
					GasUsedKeyValue:           "0x5208",
					"logs":                    []any{},
					LogsBloomKeyValue:         "0x" + fmt.Sprintf("%0512x", 0),
					StatusKeyValue:            "0x1",
					EffectiveGasPriceKeyValue: "0x4a817c800",
					TypeKeyValue:              "0x0",
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

	receipts, err := client.GetBlockReceipts(context.Background(), big.NewInt(testBlockNumber))
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.Equal(t, "1", receipts[0].Status)
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

	_, err = client.GetBlockReceipts(context.Background(), big.NewInt(999999))
	assert.Error(t, err)
}

// --- GetLatestBlock with mock server ---

func TestGetLatestBlock_Success(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			// Both HeaderByNumber and BlockByNumber use this method.
			// The mock returns the same block for all requests — that's fine.
			return fullBlockResponse("0xc8", nil), nil // 200
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, "200", block.Number) // mock always returns same block
}

func TestGetLatestBlock_HeaderError(t *testing.T) {
	t.Parallel()
	server := newMockRPCServer(func(_ string, _ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("connection refused")
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetLatestBlock(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get latest header")
}

func TestGetLatestBlock_BlockError_NonTxType(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			callCount++
			if callCount == 1 {
				// First call is HeaderByNumber (params: [nil, false])
				return fullBlockResponse("0xc8", nil), nil // 200
			}
			// Second call is BlockByNumber - return non-tx-type error
			return nil, fmt.Errorf("server error")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	_, err = client.GetLatestBlock(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get block")
}

func TestGetLatestBlock_SuccessAfterRetry(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			callCount++
			if callCount == 1 {
				// HeaderByNumber call
				return fullBlockResponse("0xc8", nil), nil // 200
			}
			// First retry gets success
			return fullBlockResponse(testBlockNumberHex, nil), nil // 100
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

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
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

// --- GetLatestBlock with unsupported tx type error ---

func TestGetLatestBlock_UnsupportedTxType_Exhausted(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping slow retry test")
	}
	// Test that all 8 retries are exhausted for unsupported tx type errors
	callCount := 0
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			callCount++
			if callCount == 1 {
				return fullBlockResponse("0xc8", nil), nil
			}
			return nil, fmt.Errorf("transaction type not supported")
		}
		return "0x1", nil
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() {
		_ = client.Close()
	}()

	_, err = client.GetLatestBlock(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "transaction type not supported")
}

func TestGetLatestBlock_UnsupportedTxType_SuccessAfterRetry(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping slow retry test")
	}
	// Test success on the second attempt after one unsupported tx type error
	callCount := 0
	server := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		if method == ethGetBlockByNumber {
			callCount++
			if callCount == 1 {
				return fullBlockResponse("0xc8", nil), nil // HeaderByNumber
			}
			if callCount == 2 {
				return nil, fmt.Errorf("transaction type not supported") // First retry fails
			}
			return fullBlockResponse(testBlockNumberHex, nil), nil // Second retry succeeds
		}
		return "0x1", nil
	})
	defer server.Close()

	client, err := NewEthereumClient(t.Context(), server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
}
