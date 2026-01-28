package rpc

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

func TestMain(m *testing.M) {
	// Initialize logger for tests
	logger.InitConsoleOnly(true)

	// Run tests
	code := m.Run()

	// Exit with the test result code
	os.Exit(code)
}

func TestNewEthereumClient_HTTPOnly(t *testing.T) {
	// Start a mock Ethereum JSON-RPC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock response for eth_chainId
		response := `{"jsonrpc":"2.0","id":1,"result":"0x1"}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}))
	defer server.Close()

	// Test HTTP-only functionality using mock server
	client, err := NewEthereumClient(server.URL, "", "")
	if err != nil {
		t.Fatalf("NewEthereumClient failed: %v", err)
	}
	defer client.Close()

	if client.httpClient == nil {
		t.Error("HTTP client should not be nil")
	}

	if client.nodeURL != server.URL {
		t.Errorf("Expected nodeURL %s, got %s", server.URL, client.nodeURL)
	}
}

func TestNewEthereumClient_InvalidHTTP(t *testing.T) {
	_, err := NewEthereumClient("invalid-url", "", "")
	if err == nil {
		t.Error("Expected error for invalid HTTP URL, got nil")
	}
}

func TestNewEthereumClient_InvalidWebSocket(t *testing.T) {
	// Start a mock HTTP server that works
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"jsonrpc":"2.0","id":1,"result":"0x1"}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}))
	defer server.Close()

	// Test that invalid WebSocket URL causes failure even with valid HTTP
	client, err := NewEthereumClient(server.URL, "ws://invalid-websocket-url:9999", "")
	if err != nil {
		t.Fatalf("Expected HTTP-only client to succeed when WebSocket is invalid, got error: %v", err)
	}
	if client == nil {
		t.Fatalf("Expected non-nil client when HTTP is available")
	}
}

func TestNewEthereumClient_NoEndpoints(t *testing.T) {
	// Test that providing no endpoints returns an error
	_, err := NewEthereumClient("", "", "")
	if err == nil {
		t.Error("Expected error when no endpoints provided, got nil")
	}
}

func TestEthereumClient_GetNetworkID_MockClient(t *testing.T) {
	// Create a mock HTTP server for testing
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"jsonrpc":"2.0","id":1,"result":"0x1"}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}))
	defer server.Close()

	// We can't easily mock ethclient.Client, so we'll test the client creation only
	client := &EthereumClient{
		nodeURL: server.URL,
	}

	// This would typically require a real Ethereum node or complex mocking
	// For now, we'll test that the function doesn't panic when httpClient is nil
	_, err := client.GetNetworkID(context.Background())
	if err == nil {
		t.Error("Expected error when httpClient is nil")
	}
}

func TestConvertGethBlock(t *testing.T) {
	// Initialize logger for testing
	logger.InitConsoleOnly(true)

	// Create a mock Geth block
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

	// Create transactions
	tx1 := ethtypes.NewTransaction(
		1,
		common.HexToAddress("0xto"),
		big.NewInt(1000),
		21000,
		big.NewInt(20000000000),
		[]byte("data"),
	)

	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{Transactions: []*ethtypes.Transaction{tx1}}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localBlock := client.convertGethBlock(gethBlock)

	if localBlock == nil {
		t.Fatal("Converted block should not be nil")
	}

	if localBlock.Hash != gethBlock.Hash().Hex() {
		t.Errorf("Expected hash %s, got %s", gethBlock.Hash().Hex(), localBlock.Hash)
	}

	if int64(localBlock.Number) != gethBlock.Number().Int64() {
		t.Errorf("Expected number %d, got %d", gethBlock.Number().Int64(), localBlock.Number)
	}

	if len(localBlock.Transactions) != 1 {
		t.Errorf("Expected 1 transaction, got %d", len(localBlock.Transactions))
	}

	// Test transaction conversion within block
	tx := localBlock.Transactions[0]
	if tx.Hash != tx1.Hash().Hex() {
		t.Errorf("Expected tx hash %s, got %s", tx1.Hash().Hex(), tx.Hash)
	}
}

func TestConvertTransaction(t *testing.T) {
	// Create a mock Geth transaction
	tx := ethtypes.NewTransaction(
		1,                           // nonce
		common.HexToAddress("0xto"), // to
		big.NewInt(1000),            // value
		21000,                       // gas
		big.NewInt(20000000000),     // gas price
		[]byte("test data"),         // data
	)

	// Create a mock block
	header := &ethtypes.Header{
		Number: big.NewInt(1234567),
	}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{Transactions: []*ethtypes.Transaction{}}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	if err != nil {
		t.Fatalf("convertTransaction failed: %v", err)
	}

	if localTx.Hash != tx.Hash().Hex() {
		t.Errorf("Expected hash %s, got %s", tx.Hash().Hex(), localTx.Hash)
	}

	if int64(localTx.BlockNumber) != gethBlock.Number().Int64() {
		t.Errorf("Expected block number %d, got %d", gethBlock.Number().Int64(), localTx.BlockNumber)
	}

	if localTx.To != tx.To().Hex() {
		t.Errorf("Expected to %s, got %s", tx.To().Hex(), localTx.To)
	}

	if localTx.Value != tx.Value().String() {
		t.Errorf("Expected value %s, got %s", tx.Value().String(), localTx.Value)
	}
}

func TestConvertTransaction_ContractCreation(t *testing.T) {
	// Create a contract creation transaction (to = nil)
	tx := ethtypes.NewContractCreation(
		1,                           // nonce
		big.NewInt(0),               // value
		21000,                       // gas
		big.NewInt(20000000000),     // gas price
		[]byte("contract bytecode"), // data
	)

	header := &ethtypes.Header{
		Number: big.NewInt(1234567),
	}
	gethBlock := ethtypes.NewBlock(header, &ethtypes.Body{Transactions: []*ethtypes.Transaction{}}, nil, trie.NewStackTrie(nil))

	client := &EthereumClient{}
	localTx, err := client.convertTransaction(tx, gethBlock, 0)

	if err != nil {
		t.Fatalf("convertTransaction failed: %v", err)
	}

	// For contract creation, To should be empty
	if localTx.To != "" {
		t.Errorf("Expected empty To for contract creation, got %s", localTx.To)
	}
}

func TestGetFromAddress(t *testing.T) {
	// This is a complex test because it requires proper signature setup
	// For now, we'll test that the function doesn't panic
	tx := ethtypes.NewTransaction(
		1,
		common.HexToAddress("0xto"),
		big.NewInt(1000),
		21000,
		big.NewInt(20000000000),
		[]byte("data"),
	)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("getFromAddress should not panic: %v", r)
		}
	}()

	// This will likely fail because the transaction isn't properly signed
	// but it shouldn't panic
	addr, err := GetFromAddress(tx)

	// The address might be nil due to invalid signature
	if err != nil || addr == nil {
		logger.Test("Got error or nil address, which is expected for unsigned transaction")
	}
}

func TestGetToAddress(t *testing.T) {
	// Test with regular transaction
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := ethtypes.NewTransaction(
		1,
		to,
		big.NewInt(1000),
		21000,
		big.NewInt(20000000000),
		[]byte("data"),
	)

	result := getToAddress(tx)
	expected := to.Hex()

	if result != expected {
		t.Errorf("Expected %s, got %s", expected, result)
	}

	// Test with contract creation (to = nil)
	contractTx := ethtypes.NewContractCreation(
		1,
		big.NewInt(0),
		21000,
		big.NewInt(20000000000),
		[]byte("contract bytecode"),
	)

	result = getToAddress(contractTx)
	if result != "" {
		t.Errorf("Expected empty string for contract creation, got %s", result)
	}
}

func TestClose(t *testing.T) {
	client := &EthereumClient{}

	// Test closing with no connections
	err := client.Close()
	if err != nil {
		t.Errorf("Close should not error with nil connections: %v", err)
	}

	// Test with mock connections would require complex setup
	// The current implementation should handle nil connections gracefully
}

func TestEthereumClient_NilBlock(t *testing.T) {
	client := &EthereumClient{}

	// Test convertGethBlock with nil block
	result := client.convertGethBlock(nil)
	if result != nil {
		t.Error("convertGethBlock should return nil for nil input")
	}
}
