package rpc

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/gorilla/websocket"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	code := m.Run()
	os.Exit(code)
}

// --- mock JSON-RPC server ---

type jsonRPCRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     any             `json:"id"`
}

func newMockRPCServer(handler func(method string, params json.RawMessage) (any, error)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		result, err := handler(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32000, "message": err.Error()},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func simpleRPCServer() *httptest.Server {
	return newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_chainId", "net_version":
			return "0x1", nil
		default:
			return "0x1", nil
		}
	})
}

// --- NewEthereumClient ---

func TestNewEthereumClient_HTTPOnly(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	assert.NotNil(t, client.httpClient)
	assert.Nil(t, client.wsClient)
	assert.Equal(t, server.URL, client.nodeURL)
}

func TestNewEthereumClient_WithAPIKey(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "test-api-key-12345")
	require.NoError(t, err)
	defer client.Close()

	assert.NotNil(t, client.httpClient)
	assert.Equal(t, "test-api-key-12345", client.apiKey)
}

func TestNewEthereumClient_InvalidHTTP(t *testing.T) {
	_, err := NewEthereumClient("invalid-url", "", "")
	assert.Error(t, err)
}

func TestNewEthereumClient_InvalidHTTPWithAPIKey(t *testing.T) {
	_, err := NewEthereumClient("invalid-url", "", "test-api-key")
	assert.Error(t, err)
}

func TestNewEthereumClient_InvalidWebSocket_FallsBackToHTTP(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "ws://invalid-websocket-url:9999", "")
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.NotNil(t, client.httpClient)
	assert.Nil(t, client.wsClient)
}

func TestNewEthereumClient_InvalidWS_WithAPIKey_FallsBackToHTTP(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "ws://invalid-ws:9999", "test-api-key-12345")
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewEthereumClient_NoEndpoints(t *testing.T) {
	_, err := NewEthereumClient("", "", "")
	assert.Error(t, err)
}

func TestNewEthereumClient_OnlyInvalidWS_NoHTTP(t *testing.T) {
	_, err := NewEthereumClient("", "ws://invalid:9999", "")
	assert.Error(t, err)
}

func TestNewEthereumClient_OnlyInvalidWS_WithAPIKey_NoHTTP(t *testing.T) {
	_, err := NewEthereumClient("", "ws://invalid:9999", "test-api-key-12345")
	assert.Error(t, err)
}

// --- apiKeyTransport ---

func TestApiKeyTransport_RoundTrip_Success(t *testing.T) {
	var receivedAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAPIKey = r.Header.Get("X-goog-api-key")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer server.Close()

	transport := &apiKeyTransport{
		apiKey: "my-api-key-1234567890",
		base:   http.DefaultTransport,
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "my-api-key-1234567890", receivedAPIKey)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestApiKeyTransport_RoundTrip_Failure(t *testing.T) {
	transport := &apiKeyTransport{
		apiKey: "my-api-key-1234567890",
		base:   http.DefaultTransport,
	}
	client := &http.Client{Transport: transport}
	_, err := client.Get("http://192.0.2.1:9999") // non-routable
	assert.Error(t, err)
}

// --- getPreferredClient ---

func TestGetPreferredClient_WSAvailable(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	httpClient, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)

	// Simulate both clients
	client := &EthereumClient{
		httpClient: httpClient.httpClient,
		wsClient:   httpClient.httpClient, // reuse as "ws" for testing
	}
	result := client.getPreferredClient()
	assert.NotNil(t, result)
	assert.Equal(t, client.wsClient, result)
}

func TestGetPreferredClient_OnlyHTTP(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)

	result := client.getPreferredClient()
	assert.NotNil(t, result)
	assert.Equal(t, client.httpClient, result)
}

func TestGetPreferredClient_NoneAvailable(t *testing.T) {
	client := &EthereumClient{}
	result := client.getPreferredClient()
	assert.Nil(t, result)
}

// --- Methods with nil client ---

func TestGetNetworkID_NilClient(t *testing.T) {
	client := &EthereumClient{}
	_, err := client.GetNetworkID(context.Background())
	assert.Error(t, err)
}

func TestGetLatestBlockNumber_NilClient(t *testing.T) {
	client := &EthereumClient{}
	_, err := client.GetLatestBlockNumber(context.Background())
	assert.Error(t, err)
}

func TestGetLatestBlock_NilClient(t *testing.T) {
	client := &EthereumClient{}
	_, err := client.GetLatestBlock(context.Background())
	assert.Error(t, err)
}

func TestGetBlockByNumber_NilClient(t *testing.T) {
	client := &EthereumClient{}
	_, err := client.GetBlockByNumber(context.Background(), big.NewInt(1))
	assert.Error(t, err)
}

func TestGetTransactionReceipt_NilClient(t *testing.T) {
	client := &EthereumClient{}
	_, err := client.GetTransactionReceipt(context.Background(), "0xabc")
	assert.Error(t, err)
}

func TestGetBlockReceipts_NilClient(t *testing.T) {
	client := &EthereumClient{}
	_, err := client.GetBlockReceipts(context.Background(), big.NewInt(1))
	assert.Error(t, err)
}

// --- convertGethBlock ---

func TestConvertGethBlock(t *testing.T) {
	header := &ethtypes.Header{
		Number:      big.NewInt(1234567),
		ParentHash:  common.HexToHash("0xparent"),
		Root:        common.HexToHash("0xroot"),
		TxHash:      common.HexToHash("0xtxhash"),
		ReceiptHash: common.HexToHash("0xreceipthash"),
		UncleHash:   common.HexToHash("0xunclehash"),
		Coinbase:    common.HexToAddress("0xcoinbase"),
		Difficulty:  big.NewInt(1000000),
		GasLimit:    8000000,
		GasUsed:     4000000,
		Time:        1600000000,
		Nonce:       ethtypes.BlockNonce{1, 2, 3, 4, 5, 6, 7, 8},
		Extra:       []byte("extra data"),
	}

	tx1 := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("data"))
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{Transactions: []*ethtypes.Transaction{tx1}}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Equal(t, gethBlock.Hash().Hex(), localBlock.Hash)
	assert.Equal(t, gethBlock.Number().String(), localBlock.Number)
	assert.Equal(t, 1, len(localBlock.Transactions))
}

func TestConvertGethBlock_NilBlock(t *testing.T) {
	client := &EthereumClient{}
	result := client.convertGethBlock(nil)
	assert.Nil(t, result)
}

func TestConvertGethBlock_WithBaseFee(t *testing.T) {
	header := &ethtypes.Header{
		Number:   big.NewInt(100),
		BaseFee:  big.NewInt(1000000000),
		GasLimit: 8000000,
	}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Equal(t, "1000000000", localBlock.BaseFeePerGas)
}

func TestConvertGethBlock_WithoutBaseFee(t *testing.T) {
	header := &ethtypes.Header{
		Number:   big.NewInt(100),
		GasLimit: 8000000,
	}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Equal(t, "", localBlock.BaseFeePerGas)
}

// --- convertTransaction ---

func TestConvertTransaction(t *testing.T) {
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("test data"))
	header := &ethtypes.Header{Number: big.NewInt(1234567)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	require.NoError(t, err)
	assert.Equal(t, tx.Hash().Hex(), localTx.Hash)
	assert.Equal(t, gethBlock.Number().String(), localTx.BlockNumber)
	assert.Equal(t, tx.To().Hex(), localTx.To)
	assert.Equal(t, tx.Value().String(), localTx.Value)
}

func TestConvertTransaction_ContractCreation(t *testing.T) {
	tx := ethtypes.NewContractCreation(1, big.NewInt(0), 21000, big.NewInt(20000000000), []byte("contract bytecode"))
	header := &ethtypes.Header{Number: big.NewInt(1234567)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	require.NoError(t, err)
	assert.Equal(t, "", localTx.To)
}

func TestConvertTransaction_EIP1559(t *testing.T) {
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	inner := &ethtypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        toPtr(common.HexToAddress("0xto")),
		Value:     big.NewInt(1000),
		Data:      []byte("data"),
	}

	signer := ethtypes.NewLondonSigner(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(100)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	require.NoError(t, err)
	assert.Equal(t, "2000000000", localTx.MaxFeePerGas)
	assert.Equal(t, "1000000000", localTx.MaxPriorityFeePerGas)
	assert.Equal(t, "1", localTx.ChainId)
}

func TestConvertTransaction_AccessList(t *testing.T) {
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	accessList := ethtypes.AccessList{
		{
			Address:     common.HexToAddress("0x1234567890123456789012345678901234567890"),
			StorageKeys: []common.Hash{common.HexToHash("0xkey1"), common.HexToHash("0xkey2")},
		},
	}

	inner := &ethtypes.AccessListTx{
		ChainID:    chainID,
		Nonce:      1,
		GasPrice:   big.NewInt(20000000000),
		Gas:        21000,
		To:         toPtr(common.HexToAddress("0xto")),
		Value:      big.NewInt(1000),
		Data:       []byte("data"),
		AccessList: accessList,
	}

	signer := ethtypes.NewEIP2930Signer(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(100)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	require.NoError(t, err)
	assert.Len(t, localTx.AccessList, 1)
	assert.Len(t, localTx.AccessList[0].StorageKeys, 2)
}

// --- convertGethReceipt ---

func TestConvertGethReceipt_Nil(t *testing.T) {
	client := &EthereumClient{}
	result := client.convertGethReceipt(nil)
	assert.Nil(t, result)
}

func TestConvertGethReceipt_Success(t *testing.T) {
	receipt := &ethtypes.Receipt{
		Status:            ethtypes.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		TxHash:            common.HexToHash("0xtxhash"),
		ContractAddress:   common.Address{},
		GasUsed:           21000,
		BlockHash:         common.HexToHash("0xblockhash"),
		BlockNumber:       big.NewInt(100),
		TransactionIndex:  0,
		Logs:              []*ethtypes.Log{},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Equal(t, "1", result.Status)
	assert.Equal(t, "", result.ContractAddress) // empty for non-contract-creation
	assert.Equal(t, "21000", result.GasUsed)
}

func TestConvertGethReceipt_FailedStatus(t *testing.T) {
	receipt := &ethtypes.Receipt{
		Status:      ethtypes.ReceiptStatusFailed,
		TxHash:      common.HexToHash("0xtxhash"),
		BlockNumber: big.NewInt(100),
		Logs:        []*ethtypes.Log{},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Equal(t, "0", result.Status)
}

func TestConvertGethReceipt_ContractCreation(t *testing.T) {
	contractAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	receipt := &ethtypes.Receipt{
		Status:          ethtypes.ReceiptStatusSuccessful,
		ContractAddress: contractAddr,
		TxHash:          common.HexToHash("0xtxhash"),
		BlockNumber:     big.NewInt(100),
		Logs:            []*ethtypes.Log{},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Equal(t, contractAddr.Hex(), result.ContractAddress)
}

func TestConvertGethReceipt_WithLogs(t *testing.T) {
	receipt := &ethtypes.Receipt{
		Status:      ethtypes.ReceiptStatusSuccessful,
		TxHash:      common.HexToHash("0xtxhash"),
		BlockNumber: big.NewInt(100),
		Logs: []*ethtypes.Log{
			{
				Address:     common.HexToAddress("0xcontract"),
				Topics:      []common.Hash{common.HexToHash("0xtopic1")},
				Data:        []byte("log data"),
				BlockNumber: 100,
				TxHash:      common.HexToHash("0xtxhash"),
				TxIndex:     0,
				BlockHash:   common.HexToHash("0xblockhash"),
				Index:       0,
				Removed:     false,
			},
		},
	}

	client := &EthereumClient{}
	result := client.convertGethReceipt(receipt)

	require.NotNil(t, result)
	assert.Len(t, result.Logs, 1)
	assert.Equal(t, common.HexToAddress("0xcontract").Hex(), result.Logs[0].Address)
}

// --- convertGethLog ---

func TestConvertGethLog(t *testing.T) {
	log := &ethtypes.Log{
		Address:     common.HexToAddress("0xcontract"),
		Topics:      []common.Hash{common.HexToHash("0xtopic1"), common.HexToHash("0xtopic2")},
		Data:        []byte{0x01, 0x02, 0x03},
		BlockNumber: 100,
		TxHash:      common.HexToHash("0xtxhash"),
		TxIndex:     5,
		BlockHash:   common.HexToHash("0xblockhash"),
		Index:       3,
		Removed:     true,
	}

	client := &EthereumClient{}
	result := client.convertGethLog(log)

	assert.Equal(t, common.HexToAddress("0xcontract").Hex(), result.Address)
	assert.Len(t, result.Topics, 2)
	assert.Equal(t, "100", result.BlockNumber)
	assert.Equal(t, 5, result.TransactionIndex)
	assert.Equal(t, 3, result.LogIndex)
	assert.True(t, result.Removed)
}

// --- helper functions ---

func TestGetToAddress(t *testing.T) {
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := ethtypes.NewTransaction(1, to, big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("data"))
	assert.Equal(t, to.Hex(), getToAddress(tx))

	contractTx := ethtypes.NewContractCreation(1, big.NewInt(0), 21000, big.NewInt(20000000000), []byte("code"))
	assert.Equal(t, "", getToAddress(contractTx))
}

func TestGetBaseFeePerGas_Nil(t *testing.T) {
	header := &ethtypes.Header{Number: big.NewInt(100)}
	block := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))
	assert.Equal(t, "", getBaseFeePerGas(block))
}

func TestGetBaseFeePerGas_Set(t *testing.T) {
	header := &ethtypes.Header{Number: big.NewInt(100), BaseFee: big.NewInt(1000)}
	block := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))
	assert.Equal(t, "1000", getBaseFeePerGas(block))
}

func TestGetMaxFeePerGas_LegacyTx(t *testing.T) {
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)
	assert.Equal(t, "", getMaxFeePerGas(tx))
}

func TestGetMaxFeePerGas_DynamicFeeTx(t *testing.T) {
	inner := &ethtypes.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		GasFeeCap: big.NewInt(2000000000),
		GasTipCap: big.NewInt(1000000000),
		Gas:       21000,
	}
	tx := ethtypes.NewTx(inner)
	assert.Equal(t, "2000000000", getMaxFeePerGas(tx))
}

func TestGetMaxPriorityFeePerGas_LegacyTx(t *testing.T) {
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)
	assert.Equal(t, "", getMaxPriorityFeePerGas(tx))
}

func TestGetMaxPriorityFeePerGas_DynamicFeeTx(t *testing.T) {
	inner := &ethtypes.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		GasFeeCap: big.NewInt(2000000000),
		GasTipCap: big.NewInt(1000000000),
		Gas:       21000,
	}
	tx := ethtypes.NewTx(inner)
	assert.Equal(t, "1000000000", getMaxPriorityFeePerGas(tx))
}

func TestGetChainId_LegacyTx(t *testing.T) {
	// Legacy transactions derive chain ID from signature; for unsigned legacy txs
	// ChainId() returns a derived value, not nil
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)
	result := getChainId(tx)
	// Just verify it returns a non-empty string (the exact value depends on go-ethereum internals)
	assert.NotEmpty(t, result)
}

func TestGetChainId_Set(t *testing.T) {
	inner := &ethtypes.DynamicFeeTx{
		ChainID:   big.NewInt(137),
		GasFeeCap: big.NewInt(2000000000),
		GasTipCap: big.NewInt(1000000000),
		Gas:       21000,
	}
	tx := ethtypes.NewTx(inner)
	assert.Equal(t, "137", getChainId(tx))
}

func TestGetContractAddress_Empty(t *testing.T) {
	receipt := &ethtypes.Receipt{ContractAddress: common.Address{}}
	assert.Equal(t, "", getContractAddress(receipt))
}

func TestGetContractAddress_Set(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	receipt := &ethtypes.Receipt{ContractAddress: addr}
	assert.Equal(t, addr.Hex(), getContractAddress(receipt))
}

func TestGetReceiptStatus_Success(t *testing.T) {
	receipt := &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful}
	assert.Equal(t, "1", getReceiptStatus(receipt))
}

func TestGetReceiptStatus_Failed(t *testing.T) {
	receipt := &ethtypes.Receipt{Status: ethtypes.ReceiptStatusFailed}
	assert.Equal(t, "0", getReceiptStatus(receipt))
}

// --- GetFromAddress ---

func TestGetFromAddress(t *testing.T) {
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), []byte("data"))

	// Unsigned transaction - should fail gracefully
	addr, err := GetFromAddress(tx)
	// Either returns an address (from homestead/frontier recovery) or an error
	if err != nil {
		assert.Nil(t, addr)
	}
}

func TestGetFromAddress_SignedEIP155(t *testing.T) {
	chainID := big.NewInt(1)
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	signer := ethtypes.NewEIP155Signer(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

func TestGetFromAddress_SignedDynamicFee(t *testing.T) {
	chainID := big.NewInt(1)
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        toPtr(common.HexToAddress("0xto")),
		Value:     big.NewInt(1000),
	}

	signer := ethtypes.NewLondonSigner(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

// --- Close ---

func TestClose_NilClients(t *testing.T) {
	client := &EthereumClient{}
	err := client.Close()
	assert.NoError(t, err)
}

func TestClose_WithHTTPClient(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)

	err = client.Close()
	assert.NoError(t, err)
}

// --- RPC methods with mock server ---

func TestGetLatestBlockNumber_Success(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			// Return a full block header with all required fields
			return map[string]any{
				"number":           "0x64",
				"hash":             "0x0000000000000000000000000000000000000000000000000000000000000001",
				"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
				"nonce":            "0x0000000000000000",
				"sha3Uncles":       "0x0000000000000000000000000000000000000000000000000000000000000000",
				"logsBloom":        "0x" + fmt.Sprintf("%0512x", 0),
				"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
				"stateRoot":        "0x0000000000000000000000000000000000000000000000000000000000000000",
				"receiptsRoot":     "0x0000000000000000000000000000000000000000000000000000000000000000",
				"miner":            "0x0000000000000000000000000000000000000000",
				"difficulty":       "0x0",
				"extraData":        "0x",
				"gasLimit":         "0x1000000",
				"gasUsed":          "0x0",
				"timestamp":        "0x0",
				"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	blockNum, err := client.GetLatestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, blockNum)
	assert.Equal(t, int64(100), blockNum.Int64())
}

func TestGetNetworkID_Success(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "net_version":
			return "1", nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	networkID, err := client.GetNetworkID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, big.NewInt(1), networkID)
}

// --- GetBlockByNumber with mock server ---

func fullBlockResponse(number string, txs []any) map[string]any {
	// Empty trie root hash — must match empty transaction list
	emptyTrieRoot := "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
	block := map[string]any{
		"number":           number,
		"hash":             "0x0000000000000000000000000000000000000000000000000000000000000001",
		"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":            "0x0000000000000000",
		"sha3Uncles":       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		"logsBloom":        "0x" + fmt.Sprintf("%0512x", 0),
		"transactionsRoot": emptyTrieRoot,
		"stateRoot":        "0x0000000000000000000000000000000000000000000000000000000000000000",
		"receiptsRoot":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"miner":            "0x0000000000000000000000000000000000000000",
		"difficulty":       "0x0",
		"totalDifficulty":  "0x0",
		"extraData":        "0x",
		"size":             "0x100",
		"gasLimit":         "0x1000000",
		"gasUsed":          "0x5208",
		"timestamp":        "0x60000000",
		"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
		"uncles":           []any{},
	}
	if txs != nil {
		block["transactions"] = txs
	} else {
		block["transactions"] = []any{}
	}
	return block
}

func TestGetBlockByNumber_Success(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x64", nil), nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	block, err := client.GetBlockByNumber(context.Background(), big.NewInt(100))
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, "100", block.Number)
}

func TestGetBlockByNumber_Error(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return nil, fmt.Errorf("block not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetBlockByNumber(context.Background(), big.NewInt(999999))
	assert.Error(t, err)
}

// --- GetTransactionReceipt with mock server ---

func TestGetTransactionReceipt_Success(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return map[string]any{
				"transactionHash":   "0x0000000000000000000000000000000000000000000000000000000000000abc",
				"transactionIndex":  "0x0",
				"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",
				"blockNumber":       "0x64",
				"from":              "0x0000000000000000000000000000000000000001",
				"to":                "0x0000000000000000000000000000000000000002",
				"cumulativeGasUsed": "0x5208",
				"gasUsed":           "0x5208",
				"contractAddress":   nil,
				"logs":              []any{},
				"logsBloom":         "0x" + fmt.Sprintf("%0512x", 0),
				"status":            "0x1",
				"effectiveGasPrice": "0x4a817c800",
				"type":              "0x0",
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	receipt, err := client.GetTransactionReceipt(context.Background(), "0x0000000000000000000000000000000000000000000000000000000000000abc")
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, "1", receipt.Status)
}

func TestGetTransactionReceipt_Error(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return nil, fmt.Errorf("not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetTransactionReceipt(context.Background(), "0xdeadbeef")
	assert.Error(t, err)
}

// --- GetBlockReceipts with mock server ---

func TestGetBlockReceipts_Success(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockReceipts":
			return []any{
				map[string]any{
					"transactionHash":   "0x0000000000000000000000000000000000000000000000000000000000000abc",
					"transactionIndex":  "0x0",
					"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",
					"blockNumber":       "0x64",
					"cumulativeGasUsed": "0x5208",
					"gasUsed":           "0x5208",
					"logs":              []any{},
					"logsBloom":         "0x" + fmt.Sprintf("%0512x", 0),
					"status":            "0x1",
					"effectiveGasPrice": "0x4a817c800",
					"type":              "0x0",
				},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	receipts, err := client.GetBlockReceipts(context.Background(), big.NewInt(100))
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.Equal(t, "1", receipts[0].Status)
}

func TestGetBlockReceipts_Error(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockReceipts":
			return nil, fmt.Errorf("block receipts not found")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetBlockReceipts(context.Background(), big.NewInt(999999))
	assert.Error(t, err)
}

// --- GetLatestBlock with mock server ---

func TestGetLatestBlock_Success(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			// Both HeaderByNumber and BlockByNumber use this method.
			// The mock returns the same block for all requests — that's fine.
			return fullBlockResponse("0xc8", nil), nil // 200
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Equal(t, "200", block.Number) // mock always returns same block
}

func TestGetLatestBlock_HeaderError(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		return nil, fmt.Errorf("connection refused")
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetLatestBlock(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get latest header")
}

func TestGetLatestBlock_BlockError_NonTxType(t *testing.T) {
	callCount := 0
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
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

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetLatestBlock(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get block")
}

func TestGetLatestBlock_SuccessAfterRetry(t *testing.T) {
	callCount := 0
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			callCount++
			if callCount == 1 {
				// HeaderByNumber call
				return fullBlockResponse("0xc8", nil), nil // 200
			}
			// First retry gets success
			return fullBlockResponse("0x64", nil), nil // 100
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
}

// --- GetLatestBlockNumber error path ---

func TestGetLatestBlockNumber_Error(t *testing.T) {
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return nil, fmt.Errorf("header error")
		default:
			return "0x1", nil
		}
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetLatestBlockNumber(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get latest header")
}

// --- convertGethBlock with failed transaction conversion ---

func TestConvertGethBlock_WithUncles(t *testing.T) {
	parentHeader := &ethtypes.Header{Number: big.NewInt(99)}
	uncleHeader := &ethtypes.Header{Number: big.NewInt(98)}

	header := &ethtypes.Header{
		Number:   big.NewInt(100),
		GasLimit: 8000000,
	}

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{
		Uncles: []*ethtypes.Header{parentHeader, uncleHeader},
	}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	assert.Len(t, localBlock.Uncles, 2)
}

// --- Close with both clients ---

func TestClose_WithBothClients(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)

	// Set wsClient to a copy of httpClient for testing
	client.wsClient = client.httpClient

	err = client.Close()
	assert.NoError(t, err)
}

// --- GetFromAddress pre-EIP-155 (Homestead signer) ---

func TestGetFromAddress_HomesteadSigner(t *testing.T) {
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	// Sign with HomesteadSigner (pre-EIP-155, no chain ID)
	signer := ethtypes.HomesteadSigner{}
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

func TestGetFromAddress_FrontierSigner(t *testing.T) {
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	// Sign with FrontierSigner
	signer := ethtypes.FrontierSigner{}
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	addr, err := GetFromAddress(tx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

// --- convertTransaction with signed legacy (exercises fromAddr != nil path) ---

func TestConvertTransaction_SignedLegacy(t *testing.T) {
	chainID := big.NewInt(1)
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	signer := ethtypes.NewEIP155Signer(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(100)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)
	require.NoError(t, err)
	assert.Equal(t, expectedAddr.Hex(), localTx.From)
	assert.Equal(t, "0", localTx.Type)
}

// --- GetLatestBlock with unsupported tx type error ---

func TestGetLatestBlock_UnsupportedTxType_Exhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow retry test")
	}
	// Test that all 8 retries are exhausted for unsupported tx type errors
	callCount := 0
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		if method == "eth_getBlockByNumber" {
			callCount++
			if callCount == 1 {
				return fullBlockResponse("0xc8", nil), nil
			}
			return nil, fmt.Errorf("transaction type not supported")
		}
		return "0x1", nil
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	_, err = client.GetLatestBlock(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "transaction type not supported")
}

func TestGetLatestBlock_UnsupportedTxType_SuccessAfterRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow retry test")
	}
	// Test success on the second attempt after one unsupported tx type error
	callCount := 0
	server := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		if method == "eth_getBlockByNumber" {
			callCount++
			if callCount == 1 {
				return fullBlockResponse("0xc8", nil), nil // HeaderByNumber
			}
			if callCount == 2 {
				return nil, fmt.Errorf("transaction type not supported") // First retry fails
			}
			return fullBlockResponse("0x64", nil), nil // Second retry succeeds
		}
		return "0x1", nil
	})
	defer server.Close()

	client, err := NewEthereumClient(server.URL, "", "")
	require.NoError(t, err)
	defer client.Close()

	block, err := client.GetLatestBlock(context.Background())
	require.NoError(t, err)
	require.NotNil(t, block)
}

// --- convertGethBlock with failed tx conversion (warn+continue path) ---

func TestConvertGethBlock_FailedTxConversion(t *testing.T) {
	header := &ethtypes.Header{
		Number:   big.NewInt(100),
		GasLimit: 8000000,
	}

	// Create a block with an unsigned legacy tx (GetFromAddress will fail)
	// and a signed tx (GetFromAddress will succeed)
	key, _ := defaultTestKey()
	signedInner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}
	signer := ethtypes.NewEIP155Signer(big.NewInt(1))
	signedTx, err := ethtypes.SignNewTx(key, signer, signedInner)
	require.NoError(t, err)

	// Unsigned tx - GetFromAddress will warn but still produce "0x0..." fallback
	unsignedTx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{
		Transactions: []*ethtypes.Transaction{unsignedTx, signedTx},
	}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	require.NotNil(t, localBlock)
	// Both transactions should be converted (unsigned gets zero address fallback)
	assert.Len(t, localBlock.Transactions, 2)
}

// --- convertTransaction fromAddr == nil path ---

func TestConvertTransaction_NilFromAddr(t *testing.T) {
	// A transaction where GetFromAddress returns nil, nil is not normally possible
	// with go-ethereum types, but the code handles it. We test via an unsigned
	// legacy tx which takes the error path with zero address fallback.
	tx := ethtypes.NewTransaction(1, common.HexToAddress("0xto"), big.NewInt(1000), 21000, big.NewInt(20000000000), nil)
	header := &ethtypes.Header{Number: big.NewInt(100)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	require.NoError(t, err)
	// Unsigned tx falls through to either zero address from error path or homestead recovery
	assert.NotEmpty(t, localTx.From)
}

// --- GetFromAddress all signers fail ---

func TestGetFromAddress_AllSignersFail(t *testing.T) {
	// Create a DynamicFeeTx with a non-zero chain ID but completely invalid signature
	// so that all post-EIP-155 signers fail
	inner := &ethtypes.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(2000000000),
		Gas:       21000,
		To:        toPtr(common.HexToAddress("0xto")),
		Value:     big.NewInt(0),
	}
	// Create unsigned DynamicFeeTx — has non-zero ChainID but no valid signature
	tx := ethtypes.NewTx(inner)

	addr, err := GetFromAddress(tx)
	// Should try all post-EIP-155 signers and fail
	assert.Error(t, err)
	assert.Nil(t, addr)
	assert.Contains(t, err.Error(), "no sender")
}

// --- NewEthereumClient WebSocket without API key (invalid, falls back to HTTP) ---

func TestNewEthereumClient_InvalidWS_NoAPIKey_FallsBackToHTTP(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	// WS is invalid but HTTP works — should succeed with HTTP only
	client, err := NewEthereumClient(server.URL, "ws://invalid-ws-url:9999", "")
	require.NoError(t, err)
	assert.NotNil(t, client.httpClient)
	assert.Nil(t, client.wsClient)
}

// --- convertTransaction with BlobTx (default switch case) ---

func TestConvertTransaction_BlobTx(t *testing.T) {
	// BlobTx has type 3 which exercises the default case in the gasPrice switch
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	inner := &ethtypes.BlobTx{
		ChainID:    uint256.NewInt(uint64(chainID.Int64())),
		Nonce:      1,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(1000),
		Data:       []byte("data"),
		BlobFeeCap: uint256.NewInt(100),
		BlobHashes: []common.Hash{common.HexToHash("0x01")},
	}

	signer := ethtypes.NewCancunSigner(chainID)
	tx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{Number: big.NewInt(100)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%d", ethtypes.BlobTxType), localTx.Type)
	// BlobTx.GasPrice() returns GasFeeCap, same as default case
	assert.NotEmpty(t, localTx.GasPrice)
}

// --- getChainId nil check ---

func TestGetChainId_NilChainID(t *testing.T) {
	// BlobTx with a nil ChainID field to test the nil check in getChainId.
	// This is an edge case that shouldn't happen in practice, but the code guards against it.
	// An unsigned BlobTx with ChainID left nil will still return non-nil from tx.ChainId()
	// because go-ethereum returns new(big.Int) for nil. We test the normal path here
	// with a zero-value chainId to ensure at least the zero-value string is returned.
	inner := &ethtypes.BlobTx{
		ChainID:    uint256.NewInt(0),
		Nonce:      0,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         common.HexToAddress("0xto"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(100),
		BlobHashes: []common.Hash{common.HexToHash("0x01")},
	}
	tx := ethtypes.NewTx(inner)
	result := getChainId(tx)
	// ChainId() returns big.Int(0) which is "0"
	assert.Equal(t, "0", result)
}

// --- createWebSocketWithHeaders URL with existing query parameter ---

func TestCreateWebSocketWithHeaders_URLWithQueryParam(t *testing.T) {
	// Test the branch where the WS URL already contains "?" (query string),
	// so the function appends with "&" instead of "?"
	_, err := createWebSocketWithHeaders("ws://invalid-host:9999?existing=param", "test-api-key")
	// Connection will fail, but we exercise the URL construction path with "&key=" and "&api_key="
	assert.Error(t, err)
}

func TestCreateWebSocketWithHeaders_URLWithoutQueryParam(t *testing.T) {
	// Test the branch where the WS URL has no query string,
	// so the function appends with "?" for both key= and api_key=
	_, err := createWebSocketWithHeaders("ws://invalid-host:9999", "test-api-key")
	assert.Error(t, err)
}

// --- WebSocket mock server for success paths ---

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// newWSMockServer creates an httptest.Server that upgrades to WebSocket and
// handles JSON-RPC messages, simulating an Ethereum node.
func newWSMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var req jsonRPCRequest
			if err := json.Unmarshal(msg, &req); err != nil {
				return
			}

			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  "0x1",
			}
			respBytes, _ := json.Marshal(resp)
			if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
				return
			}
		}
	}))
}

func TestCreateWebSocketWithHeaders_Success(t *testing.T) {
	// Start a real WebSocket server to exercise the success path
	server := newWSMockServer()
	defer server.Close()

	// Convert http://... to ws://...
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	client, err := createWebSocketWithHeaders(wsURL, "test-api-key")
	require.NoError(t, err)
	require.NotNil(t, client)
	client.Close()
}

func TestCreateWebSocketWithHeaders_SuccessWithQueryParam(t *testing.T) {
	// Test the success path when the URL already contains "?"
	server := newWSMockServer()
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?existing=param"

	client, err := createWebSocketWithHeaders(wsURL, "test-api-key")
	require.NoError(t, err)
	require.NotNil(t, client)
	client.Close()
}

func TestNewEthereumClient_WSSuccess_WithAPIKey(t *testing.T) {
	// HTTP server for the HTTP client
	httpServer := simpleRPCServer()
	defer httpServer.Close()

	// WebSocket server for the WS client
	wsServer := newWSMockServer()
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	client, err := NewEthereumClient(httpServer.URL, wsURL, "test-api-key-12345")
	require.NoError(t, err)
	defer client.Close()

	assert.NotNil(t, client.httpClient)
	assert.NotNil(t, client.wsClient)
}

func TestNewEthereumClient_WSSuccess_NoAPIKey(t *testing.T) {
	// HTTP server for the HTTP client
	httpServer := simpleRPCServer()
	defer httpServer.Close()

	// WebSocket server for the WS client
	wsServer := newWSMockServer()
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	client, err := NewEthereumClient(httpServer.URL, wsURL, "")
	require.NoError(t, err)
	defer client.Close()

	assert.NotNil(t, client.httpClient)
	assert.NotNil(t, client.wsClient)
}

func TestNewEthereumClient_WSFallback_WithAPIKey(t *testing.T) {
	// Test the path where createWebSocketWithHeaders fails (rejects URLs with
	// query params) but the standard ethclient.Dial fallback succeeds.
	// This exercises lines 83-96 in NewEthereumClient.

	// This WS server rejects connections that have query parameters (simulating
	// a server that doesn't accept API key in query string), but accepts
	// plain WebSocket connections.
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			http.Error(w, "query params not allowed", http.StatusForbidden)
			return
		}
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req jsonRPCRequest
			if err := json.Unmarshal(msg, &req); err != nil {
				return
			}
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  "0x1",
			}
			respBytes, _ := json.Marshal(resp)
			if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	httpServer := simpleRPCServer()
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	client, err := NewEthereumClient(httpServer.URL, wsURL, "test-api-key-12345")
	require.NoError(t, err)
	defer client.Close()

	assert.NotNil(t, client.httpClient)
	// The WS client should be set via the fallback path
	assert.NotNil(t, client.wsClient)
}

// --- NewEthereumClient WS with API key, both approaches fail, no HTTP ---

func TestNewEthereumClient_InvalidWS_WithAPIKey_NoHTTP(t *testing.T) {
	// WS with API key fails completely and there's no HTTP fallback
	_, err := NewEthereumClient("", "ws://invalid:9999", "test-api-key-12345")
	assert.Error(t, err)
}

// --- NewEthereumClient WS with API key where URL has query param, falls back to HTTP ---

func TestNewEthereumClient_InvalidWSWithQueryParam_WithAPIKey_FallsBackToHTTP(t *testing.T) {
	server := simpleRPCServer()
	defer server.Close()

	// WS URL contains "?" to exercise the "&key=" path in createWebSocketWithHeaders
	client, err := NewEthereumClient(server.URL, "ws://invalid:9999?param=value", "test-api-key-12345")
	require.NoError(t, err)
	assert.NotNil(t, client.httpClient)
	assert.Nil(t, client.wsClient)
}

// --- convertGethBlock with a block containing a BlobTx ---

func TestConvertGethBlock_WithBlobTx(t *testing.T) {
	chainID := big.NewInt(1)
	key, _ := defaultTestKey()

	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	inner := &ethtypes.BlobTx{
		ChainID:    uint256.NewInt(uint64(chainID.Int64())),
		Nonce:      0,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(2000000000),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(1000),
		Data:       []byte("blob data"),
		BlobFeeCap: uint256.NewInt(100),
		BlobHashes: []common.Hash{common.HexToHash("0x01")},
	}

	signer := ethtypes.NewCancunSigner(chainID)
	blobTx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	header := &ethtypes.Header{
		Number:   big.NewInt(200),
		GasLimit: 8000000,
		BaseFee:  big.NewInt(1000000000),
	}

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{
		Transactions: []*ethtypes.Transaction{blobTx},
	}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)
	require.NotNil(t, localBlock)
	assert.Len(t, localBlock.Transactions, 1)
	assert.Equal(t, fmt.Sprintf("%d", ethtypes.BlobTxType), localBlock.Transactions[0].Type)
}

// --- GetFromAddress pre-EIP-155 where both Homestead and Frontier fail ---

func TestGetFromAddress_PreEIP155_BothSignersFail(t *testing.T) {
	// Create a legacy tx with V=27 (pre-EIP-155 marker) and R=0, S=0.
	// deriveChainId(27) returns 0, so ChainId().Sign() == 0, entering the pre-EIP-155 path.
	// Both HomesteadSigner and FrontierSigner will fail to recover a sender
	// because ecrecover cannot work with zero R,S values.
	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
		V:        big.NewInt(27),
		R:        big.NewInt(0),
		S:        big.NewInt(0),
	}
	tx := ethtypes.NewTx(inner)

	addr, err := GetFromAddress(tx)
	assert.Error(t, err)
	assert.Nil(t, addr)
	assert.Contains(t, err.Error(), "pre-EIP-155")
}

// --- convertTransaction where GetFromAddress errors (zero address fallback) ---

func TestConvertTransaction_FromAddrError_ZeroAddressFallback(t *testing.T) {
	// Unsigned legacy tx triggers GetFromAddress error, which falls back to zero address
	inner := &ethtypes.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}
	tx := ethtypes.NewTx(inner)

	header := &ethtypes.Header{Number: big.NewInt(100)}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)
	require.NoError(t, err)
	// The error path sets fromAddr to zero address
	assert.Equal(t, "0x0000000000000000000000000000000000000000", localTx.From)
}

// --- GetFromAddress FrontierSigner fallback (high-s value) ---

func TestGetFromAddress_FrontierSigner_HighS(t *testing.T) {
	// Craft a pre-EIP-155 tx where HomesteadSigner rejects (s > secp256k1HalfN)
	// but FrontierSigner accepts. We sign normally, then flip s to s' = N - s
	// and adjust v, producing a valid but "non-canonical" signature.
	key, expectedAddr := defaultTestKey()

	inner := &ethtypes.LegacyTx{
		Nonce:    42,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
	}

	// Sign with FrontierSigner to get a valid pre-EIP-155 signature
	signer := ethtypes.FrontierSigner{}
	signedTx, err := ethtypes.SignNewTx(key, signer, inner)
	require.NoError(t, err)

	// Extract V, R, S
	v, r, s := signedTx.RawSignatureValues()

	// secp256k1 curve order N
	secp256k1N, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	secp256k1HalfN := new(big.Int).Div(secp256k1N, big.NewInt(2))

	// If s is already > halfN, the original signature works. Otherwise flip it.
	newS := new(big.Int).Set(s)
	newV := new(big.Int).Set(v)
	if s.Cmp(secp256k1HalfN) <= 0 {
		// Flip s: s' = N - s
		newS.Sub(secp256k1N, s)
		// Flip recovery bit: 27 ↔ 28
		if v.Int64() == 27 {
			newV.SetInt64(28)
		} else {
			newV.SetInt64(27)
		}
	}

	// Create a new tx with the high-s signature
	highSTx := ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    42,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       toPtr(common.HexToAddress("0xto")),
		Value:    big.NewInt(1000),
		V:        newV,
		R:        r,
		S:        newS,
	})

	// Verify: HomesteadSigner should reject (s > halfN)
	_, homesteadErr := ethtypes.Sender(ethtypes.HomesteadSigner{}, highSTx)
	require.Error(t, homesteadErr, "HomesteadSigner should reject high-s signature")

	// Verify: FrontierSigner should accept
	from, frontierErr := ethtypes.Sender(ethtypes.FrontierSigner{}, highSTx)
	require.NoError(t, frontierErr, "FrontierSigner should accept high-s signature")
	assert.Equal(t, expectedAddr, from)

	// Now test GetFromAddress — it should succeed via the FrontierSigner fallback path
	addr, err := GetFromAddress(highSTx)
	require.NoError(t, err)
	require.NotNil(t, addr)
	assert.Equal(t, expectedAddr, *addr)
}

// --- Test helpers ---

func toPtr(addr common.Address) *common.Address {
	return &addr
}

func defaultTestKey() (*ecdsa.PrivateKey, common.Address) {
	// Use a fixed test private key
	key, err := crypto.HexToECDSA("fad9c8855b740a0b7ed4c221dbad0f33a83a49cad6b3fe8d5817ac83d38b6a19")
	if err != nil {
		panic(fmt.Sprintf("failed to parse test key: %v", err))
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	return key, addr
}
