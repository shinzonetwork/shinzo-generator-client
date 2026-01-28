package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
)

// EthereumClient wraps both JSON-RPC and fallback HTTP client
type EthereumClient struct {
	httpClient *ethclient.Client
	wsClient   *ethclient.Client
	nodeURL    string
	wsURL      string
	apiKey     string
}

// NewEthereumClient creates a new JSON-RPC Ethereum client with HTTP and WebSocket support
func NewEthereumClient(httpNodeURL, wsURL, apiKey string) (*EthereumClient, error) {
	client := &EthereumClient{
		nodeURL: httpNodeURL,
		wsURL:   wsURL,
		apiKey:  apiKey,
	}

	// Establish HTTP client with API key authentication
	if httpNodeURL != "" {
		var httpClient *ethclient.Client
		var err error

		if apiKey != "" {
			logger.Sugar.Infof("Creating HTTP client with API key authentication for %s", httpNodeURL)
			// Create RPC client with custom headers for API key authentication using modern approach
			rpcClient, err := ethrpc.DialOptions(context.Background(), httpNodeURL, ethrpc.WithHTTPClient(&http.Client{
				Transport: &apiKeyTransport{
					apiKey: apiKey,
					base:   http.DefaultTransport,
				},
			}))
			if err != nil {
				logger.Sugar.Errorf("Failed to create HTTP client with API key: %v", err)
				return nil, errors.NewRPCConnectionFailed("rpc", "NewEthereumClient", httpNodeURL, err)
			}
			httpClient = ethclient.NewClient(rpcClient)
			logger.Sugar.Info("HTTP client with API key created successfully")
		} else {
			logger.Sugar.Info("Creating HTTP client without API key")
			// Standard connection without API key
			httpClient, err = ethclient.Dial(httpNodeURL)
			if err != nil {
				return nil, errors.NewRPCConnectionFailed("rpc", "NewEthereumClient", httpNodeURL, err)
			}
		}
		client.httpClient = httpClient
	}

	// Establish WebSocket client with API key authentication if provided
	if wsURL != "" {
		logger.Sugar.Infof("Attempting WebSocket connection to %s", wsURL)
		var wsClient *ethclient.Client
		var err error
		var wsConnected bool

		if apiKey != "" {
			// Create WebSocket connection with custom headers for GCP authentication
			logger.Sugar.Info("Creating WebSocket connection with X-goog-api-key header")
			wsClient, err = createWebSocketWithHeaders(wsURL, apiKey)
			if err != nil {
				logger.Sugar.Warnf("Failed to establish WebSocket connection with API key header: %v", err)
				// Try fallback without API key
				logger.Sugar.Info("Trying standard WebSocket connection as fallback")
				wsClient, err = ethclient.Dial(wsURL)
				if err != nil {
					logger.Sugar.Errorf("Failed to establish WebSocket connection: %v", err)
					// Only return error if HTTP client is also unavailable
					if client.httpClient == nil {
						return nil, errors.NewRPCConnectionFailed("rpc", "NewEthereumClient", wsURL,
							fmt.Errorf("WebSocket connection failed with both API key and standard methods: %w", err))
					}
					logger.Sugar.Warn("WebSocket unavailable, will use HTTP-only mode (may have reduced performance)")
				} else {
					logger.Sugar.Info("WebSocket fallback connection successful")
					client.wsClient = wsClient
					wsConnected = true
				}
			} else {
				logger.Sugar.Info("WebSocket connection with API key header successful")
				client.wsClient = wsClient
				wsConnected = true
			}
		} else {
			// Standard WebSocket connection without API key
			wsClient, err = ethclient.Dial(wsURL)
			if err != nil {
				logger.Sugar.Errorf("Failed to establish WebSocket connection: %v", err)
				// Only return error if HTTP client is also unavailable
				if client.httpClient == nil {
					return nil, errors.NewRPCConnectionFailed("rpc", "NewEthereumClient", wsURL, err)
				}
				logger.Sugar.Warn("WebSocket unavailable, will use HTTP-only mode (may have reduced performance)")
			} else {
				logger.Sugar.Info("Standard WebSocket connection successful")
				client.wsClient = wsClient
				wsConnected = true
			}
		}

		// Log performance implications if WebSocket failed but HTTP succeeded
		if !wsConnected && client.httpClient != nil {
			logger.Sugar.Warn("WebSocket connection failed but HTTP is available - indexer performance may be reduced")
		}
	}

	// Ensure at least one client is available
	if client.httpClient == nil && client.wsClient == nil {
		return nil, errors.NewRPCConnectionFailed("rpc", "NewEthereumClient", "all endpoints",
			fmt.Errorf("no valid connections established - both HTTP (%s) and WebSocket (%s) failed", httpNodeURL, wsURL))
	}

	return client, nil
}

// apiKeyTransport adds API key header to HTTP requests
type apiKeyTransport struct {
	apiKey string
	base   http.RoundTripper
}

func (t *apiKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	logger.Sugar.Debugf("HTTP Request: %s %s", req.Method, req.URL.String())
	logger.Sugar.Debugf("Setting X-goog-api-key header: %s", t.apiKey[:10]+"...")
	req.Header.Set("X-goog-api-key", t.apiKey)

	// Log request headers (excluding sensitive data)
	logger.Sugar.Debugf("Request headers: Content-Type=%s, User-Agent=%s",
		req.Header.Get("Content-Type"), req.Header.Get("User-Agent"))

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		logger.Sugar.Debugf("HTTP request failed: %v", err)
	} else {
		logger.Sugar.Debugf("HTTP response: %s (Content-Length: %s)",
			resp.Status, resp.Header.Get("Content-Length"))
		logger.Sugar.Debugf("HTTP request successful, status: %s", resp.Status)
	}
	return resp, err
}

// GetBlockByNumber fetches a block by number with Arbitrum transaction type support
func (c *EthereumClient) GetBlockByNumber(ctx context.Context, blockNumber *big.Int) (*types.Block, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}

	// Try go-ethereum first for standard transactions
	gethBlock, err := client.BlockByNumber(ctx, blockNumber)
	if err != nil {
		if strings.Contains(err.Error(), "transaction type not supported") ||
			strings.Contains(err.Error(), "invalid transaction type") {
			// Fall back to raw JSON-RPC for Arbitrum transactions
			logger.Sugar.Infof("Go-ethereum failed with transaction type error, using raw JSON-RPC for block %s", blockNumber.String())
			return c.getBlockByNumberRaw(ctx, blockNumber)
		}
		return nil, fmt.Errorf("failed to get block %s: %w", blockNumber.String(), err)
	}

	return c.convertGethBlock(gethBlock), nil
}

// getBlockByNumberRaw fetches a block using raw JSON-RPC to handle Arbitrum transactions
func (c *EthereumClient) getBlockByNumberRaw(ctx context.Context, blockNumber *big.Int) (*types.Block, error) {
	// Use raw HTTP client for JSON-RPC call
	if c.nodeURL == "" {
		return nil, fmt.Errorf("HTTP URL not available for raw JSON-RPC")
	}

	// Prepare JSON-RPC request
	blockNumHex := fmt.Sprintf("0x%x", blockNumber)
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_getBlockByNumber",
		"params":  []interface{}{blockNumHex, true}, // true = include full transaction details
		"id":      1,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	// Make HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", c.nodeURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-goog-api-key", c.apiKey)
	}

	// Create HTTP client for raw request
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Parse response using raw JSON structure that matches Arbitrum RPC format
	var rpcResponse struct {
		Result *rawBlock `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rpcResponse); err != nil {
		return nil, fmt.Errorf("failed to decode JSON-RPC response: %w", err)
	}

	if rpcResponse.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error: %s", rpcResponse.Error.Message)
	}

	if rpcResponse.Result == nil {
		return nil, fmt.Errorf("block not found")
	}

	// Convert raw block to our types.Block structure
	return c.convertRawBlock(rpcResponse.Result)
}

// rawBlock represents the JSON-RPC block structure with hex string fields
type rawBlock struct {
	Hash         string           `json:"hash"`
	Number       string           `json:"number"`    // hex string
	Timestamp    string           `json:"timestamp"` // hex string
	ParentHash   string           `json:"parentHash"`
	GasUsed      string           `json:"gasUsed"`  // hex string
	GasLimit     string           `json:"gasLimit"` // hex string
	Transactions []rawTransaction `json:"transactions"`
	// Add other fields as needed
}

// rawTransaction represents the JSON-RPC transaction structure with hex string fields
type rawTransaction struct {
	Hash             string `json:"hash"`
	BlockHash        string `json:"blockHash"`
	BlockNumber      string `json:"blockNumber"`      // hex string
	TransactionIndex string `json:"transactionIndex"` // hex string
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`    // hex string
	Gas              string `json:"gas"`      // hex string
	GasPrice         string `json:"gasPrice"` // hex string
	Input            string `json:"input"`
	Nonce            string `json:"nonce"`   // hex string
	Type             string `json:"type"`    // hex string
	ChainId          string `json:"chainId"` // hex string
	V                string `json:"v"`       // hex string
	R                string `json:"r"`       // hex string
	S                string `json:"s"`       // hex string
}

// convertRawBlock converts a raw JSON-RPC block to our types.Block structure
func (c *EthereumClient) convertRawBlock(raw *rawBlock) (*types.Block, error) {
	if raw == nil {
		return nil, fmt.Errorf("raw block is nil")
	}

	// Convert hex number to int
	blockNumber, err := hexToInt(raw.Number)
	if err != nil {
		return nil, fmt.Errorf("failed to convert block number %s: %w", raw.Number, err)
	}

	// Convert transactions
	transactions := make([]types.Transaction, 0, len(raw.Transactions))
	for i, rawTx := range raw.Transactions {
		tx, err := c.convertRawTransaction(&rawTx, raw, i)
		if err != nil {
			logger.Sugar.Warnf("Warning: Failed to convert transaction %s: %v", rawTx.Hash, err)
			continue
		}
		transactions = append(transactions, *tx)
	}

	return &types.Block{
		Hash:         raw.Hash,
		Number:       blockNumber,
		Timestamp:    raw.Timestamp,
		ParentHash:   raw.ParentHash,
		GasUsed:      raw.GasUsed,
		GasLimit:     raw.GasLimit,
		Transactions: transactions,
		// Set other fields with defaults or convert as needed
	}, nil
}

// convertRawTransaction converts a raw JSON-RPC transaction to our types.Transaction structure
func (c *EthereumClient) convertRawTransaction(raw *rawTransaction, block *rawBlock, index int) (*types.Transaction, error) {
	if raw == nil {
		return nil, fmt.Errorf("raw transaction is nil")
	}

	// Convert hex numbers to int
	blockNumber, err := hexToInt(raw.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to convert block number %s: %w", raw.BlockNumber, err)
	}

	transactionIndex, err := hexToInt(raw.TransactionIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to convert transaction index %s: %w", raw.TransactionIndex, err)
	}

	return &types.Transaction{
		Hash:             raw.Hash,
		BlockHash:        raw.BlockHash,
		BlockNumber:      blockNumber,
		TransactionIndex: transactionIndex,
		From:             raw.From,
		To:               raw.To,
		Value:            raw.Value,
		Gas:              raw.Gas,
		GasPrice:         raw.GasPrice,
		Input:            raw.Input,
		Nonce:            raw.Nonce,
		Type:             raw.Type,
		ChainId:          raw.ChainId,
		V:                raw.V,
		R:                raw.R,
		S:                raw.S,
		// Initialize other fields with defaults
		AccessList: []types.AccessListEntry{},
	}, nil
}

// hexToInt converts a hex string to int
func hexToInt(hexStr string) (int, error) {
	if hexStr == "" || hexStr == "0x" {
		return 0, nil
	}

	// Remove 0x prefix if present
	if strings.HasPrefix(hexStr, "0x") {
		hexStr = hexStr[2:]
	}

	// Parse hex string to int64, then convert to int
	val, err := strconv.ParseInt(hexStr, 16, 64)
	if err != nil {
		return 0, err
	}

	return int(val), nil
}

// GetTransactionReceipt fetches a transaction receipt by hash
func (c *EthereumClient) GetTransactionReceipt(ctx context.Context, txHash string) (*types.TransactionReceipt, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}

	hash := common.HexToHash(txHash)
	receipt, err := client.TransactionReceipt(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction receipt: %w", err)
	}
	return c.convertGethReceipt(receipt), nil
}

// convertGethReceipt converts go-ethereum receipt to our custom receipt type
func (c *EthereumClient) convertGethReceipt(receipt *ethtypes.Receipt) *types.TransactionReceipt {
	if receipt == nil {
		return nil
	}

	// Convert logs
	logs := make([]types.Log, len(receipt.Logs))
	for i, log := range receipt.Logs {
		logs[i] = c.convertGethLog(log)
	}

	return &types.TransactionReceipt{
		TransactionHash:   receipt.TxHash.Hex(),
		TransactionIndex:  fmt.Sprintf("%d", receipt.TransactionIndex),
		BlockHash:         receipt.BlockHash.Hex(),
		BlockNumber:       receipt.BlockNumber.String(),
		CumulativeGasUsed: fmt.Sprintf("%d", receipt.CumulativeGasUsed),
		GasUsed:           fmt.Sprintf("%d", receipt.GasUsed),
		ContractAddress:   getContractAddress(receipt),
		Logs:              logs,
		Status:            getReceiptStatus(receipt),
	}
}

// MergeTransactionWithReceipt merges transaction data with receipt data
func (c *EthereumClient) MergeTransactionWithReceipt(tx *types.Transaction, receipt *types.TransactionReceipt) {
	if tx == nil || receipt == nil {
		return
	}

	// Merge receipt fields into transaction
	tx.ContractAddress = receipt.ContractAddress
	tx.CumulativeGasUsed = receipt.CumulativeGasUsed
	tx.GasUsed = receipt.GasUsed
	tx.Status = receipt.Status
	tx.LogsBloom = "" // Will be populated from block if needed

	// Convert logs to match new schema
	logs := make([]types.Log, len(receipt.Logs))
	copy(logs, receipt.Logs)
	tx.Logs = logs
}

// convertGethLog converts go-ethereum log to our custom log type
func (c *EthereumClient) convertGethLog(log *ethtypes.Log) types.Log {
	// Convert topics
	topics := make([]string, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = topic.Hex()
	}

	return types.Log{
		Address:          log.Address.Hex(),
		Topics:           topics,
		Data:             "0x" + common.Bytes2Hex(log.Data),
		BlockNumber:      int(log.BlockNumber),
		TransactionHash:  log.TxHash.Hex(),
		TransactionIndex: int(log.TxIndex),
		BlockHash:        log.BlockHash.Hex(),
		LogIndex:         int(log.Index),
		Removed:          log.Removed,
	}
}

// Helper functions for receipt conversion
func getContractAddress(receipt *ethtypes.Receipt) string {
	if receipt.ContractAddress == (common.Address{}) {
		return ""
	}
	return receipt.ContractAddress.Hex()
}

func getReceiptStatus(receipt *ethtypes.Receipt) string {
	if receipt.Status == ethtypes.ReceiptStatusSuccessful {
		return "0x1"
	}
	return "0x0"
}

// convertGethBlock converts go-ethereum Block to our custom Block type
func (c *EthereumClient) convertGethBlock(gethBlock *ethtypes.Block) *types.Block {
	if gethBlock == nil {
		return nil
	}

	// Convert transactions
	transactions := make([]types.Transaction, 0, len(gethBlock.Transactions()))

	for i, tx := range gethBlock.Transactions() {
		// Skip transaction conversion if it fails (continue with others)
		localTx, err := c.convertTransaction(tx, gethBlock, i)
		if err != nil {
			logger.Sugar.Warnf("Warning: Failed to convert transaction %s: %v", tx.Hash().Hex(), err)
			continue
		}

		transactions = append(transactions, *localTx)
	}

	// Convert the block to match new schema
	return &types.Block{
		BaseFeePerGas: getBaseFeePerGas(gethBlock),
		Difficulty:    gethBlock.Difficulty().String(),
		ExtraData:     "0x" + common.Bytes2Hex(gethBlock.Extra()),
		GasLimit:      fmt.Sprintf("%d", gethBlock.GasLimit()),
		GasUsed:       fmt.Sprintf("%d", gethBlock.GasUsed()),
		Hash:          gethBlock.Hash().Hex(),
		L1BlockNumber: "", // Arbitrum specific - will be populated from block data if available
		LogsBloom:     "0x" + common.Bytes2Hex(gethBlock.Bloom().Bytes()),
		MixHash:       gethBlock.MixDigest().Hex(),
		Nonce:         fmt.Sprintf("%d", gethBlock.Nonce()),
		Number:        int(gethBlock.NumberU64()),
		ParentHash:    gethBlock.ParentHash().Hex(),
		ReceiptsRoot:  gethBlock.ReceiptHash().Hex(),
		SendCount:     "", // Arbitrum specific - will be populated if available
		SendRoot:      "", // Arbitrum specific - will be populated if available
		Sha3Uncles:    gethBlock.UncleHash().Hex(),
		Size:          fmt.Sprintf("%d", gethBlock.Size()),
		StateRoot:     gethBlock.Root().Hex(),
		Timestamp:     fmt.Sprintf("%d", gethBlock.Time()),
		Transactions:  transactions,
	}
}

// convertTransaction safely converts a single transaction
func (c *EthereumClient) convertTransaction(tx *ethtypes.Transaction, gethBlock *ethtypes.Block, index int) (*types.Transaction, error) {
	// Get transaction details with error handling
	fromAddr, err := GetFromAddress(tx)
	var fromAddrStr string
	if err != nil {
		// For unsigned transactions or other errors, use zero address
		logger.Sugar.Warnf("Warning: Failed to convert transaction %s: %v", tx.Hash().Hex(), err)
		fromAddrStr = "0x0000000000000000000000000000000000000000"
	} else if fromAddr != nil {
		fromAddrStr = fromAddr.Hex()
	} else {
		fromAddrStr = "0x0000000000000000000000000000000000000000"
	}
	toAddr := getToAddress(tx)

	// Handle different transaction types
	var gasPrice *big.Int
	switch tx.Type() {
	case ethtypes.LegacyTxType, ethtypes.AccessListTxType:
		gasPrice = tx.GasPrice()
	case ethtypes.DynamicFeeTxType:
		// For EIP-1559 transactions, use effective gas price if available
		// Fall back to gas fee cap if not
		gasPrice = tx.GasFeeCap()
	case 0x6a: // Arbitrum internal transaction type
		// Arbitrum internal transactions may have zero gas price
		gasPrice = tx.GasPrice()
		if gasPrice == nil {
			gasPrice = big.NewInt(0)
		}
	default:
		// For unknown transaction types, try to get gas price
		// If it fails, we'll catch it in the calling function
		gasPrice = tx.GasPrice()
		if gasPrice == nil {
			gasPrice = big.NewInt(0)
		}
	}

	// Extract signature components with error handling for Arbitrum transactions
	v, r, s := tx.RawSignatureValues()

	// Handle cases where signature components might be nil (e.g., Arbitrum internal transactions)
	if v == nil {
		v = big.NewInt(0)
	}
	if r == nil {
		r = big.NewInt(0)
	}
	if s == nil {
		s = big.NewInt(0)
	}

	// Get access list for EIP-2930/EIP-1559 transactions
	accessList := make([]types.AccessListEntry, 0)
	if tx.AccessList() != nil {
		for _, entry := range tx.AccessList() {
			storageKeys := make([]string, len(entry.StorageKeys))
			for i, key := range entry.StorageKeys {
				storageKeys[i] = key.Hex()
			}
			accessList = append(accessList, types.AccessListEntry{
				Address:     entry.Address.Hex(),
				StorageKeys: storageKeys,
			})
		}
	}

	localTx := types.Transaction{
		// Transaction fields
		BlockHash:        gethBlock.Hash().Hex(),
		BlockNumber:      int(gethBlock.NumberU64()),
		From:             fromAddrStr,
		Gas:              fmt.Sprintf("%d", tx.Gas()),
		GasPrice:         gasPrice.String(),
		Hash:             tx.Hash().Hex(),
		Input:            "0x" + common.Bytes2Hex(tx.Data()),
		Nonce:            fmt.Sprintf("%d", tx.Nonce()),
		To:               toAddr,
		TransactionIndex: index,
		Value:            tx.Value().String(),
		Type:             fmt.Sprintf("0x%x", tx.Type()),
		ChainId:          getChainId(tx),
		V:                v.String(),
		R:                r.String(),
		S:                s.String(),
		// Receipt fields - will be populated when receipt is fetched
		ContractAddress:   "",
		CumulativeGasUsed: "",
		EffectiveGasPrice: "",
		GasUsed:           "",
		GasUsedForL1:      "",
		L1BlockNumber:     "",
		Status:            "",
		Timeboosted:       false,
		LogsBloom:         "",
		Logs:              []types.Log{},
	}

	return &localTx, nil
}

// Helper functions for transaction conversion
func GetFromAddress(tx *ethtypes.Transaction) (*common.Address, error) {
	chainId := tx.ChainId()

	// Handle pre-EIP-155 transactions (before block 2,675,000)
	if chainId == nil || chainId.Sign() <= 0 {
		// Use HomesteadSigner for pre-EIP-155 transactions
		homesteadSigner := ethtypes.HomesteadSigner{}
		if from, err := ethtypes.Sender(homesteadSigner, tx); err == nil {
			return &from, nil
		}

		// Fallback to FrontierSigner for even older transactions
		frontierSigner := ethtypes.FrontierSigner{}
		if from, err := ethtypes.Sender(frontierSigner, tx); err == nil {
			return &from, nil
		}

		return nil, fmt.Errorf("unable to recover sender from pre-EIP-155 transaction")
	}

	// Try different signers to handle various transaction types (post-EIP-155)
	signers := []ethtypes.Signer{
		ethtypes.LatestSignerForChainID(chainId),
		ethtypes.NewEIP155Signer(chainId),
		ethtypes.NewLondonSigner(chainId),
	}

	for _, signer := range signers {
		if from, err := ethtypes.Sender(signer, tx); err == nil {
			return &from, nil
		}
	}

	return nil, fmt.Errorf("no sender (from) address found")
}

func getToAddress(tx *ethtypes.Transaction) string {
	if tx.To() == nil {
		return "" // Contract creation
	}
	return tx.To().Hex()
}

// getBaseFeePerGas extracts base fee from EIP-1559 blocks
func getBaseFeePerGas(block *ethtypes.Block) string {
	if block.BaseFee() == nil {
		return "" // Not an EIP-1559 block
	}
	return block.BaseFee().String()
}

// getMaxFeePerGas extracts max fee per gas from EIP-1559 transactions
func getMaxFeePerGas(tx *ethtypes.Transaction) string {
	if tx.Type() == ethtypes.DynamicFeeTxType {
		return tx.GasFeeCap().String()
	}
	return ""
}

// getMaxPriorityFeePerGas extracts max priority fee per gas from EIP-1559 transactions
func getMaxPriorityFeePerGas(tx *ethtypes.Transaction) string {
	if tx.Type() == ethtypes.DynamicFeeTxType {
		return tx.GasTipCap().String()
	}
	return ""
}

// getChainId extracts chain ID from transaction
func getChainId(tx *ethtypes.Transaction) string {
	if tx.ChainId() == nil {
		return ""
	}
	return tx.ChainId().String()
}

// Close closes the connections
func (c *EthereumClient) Close() error {
	if c.httpClient != nil {
		c.httpClient.Close()
	}
	if c.wsClient != nil {
		c.wsClient.Close()
	}
	return nil
}

// getPreferredClient returns WebSocket client if available, otherwise HTTP client
// Prioritizes WebSocket for real-time blockchain data streaming
func (c *EthereumClient) getPreferredClient() *ethclient.Client {
	if c.wsClient != nil {
		logger.Sugar.Debug("Using WebSocket client for real-time blockchain streaming")
		return c.wsClient
	}
	if c.httpClient != nil {
		logger.Sugar.Debug("Using HTTP client with API key authentication (WebSocket unavailable)")
		return c.httpClient
	}

	logger.Sugar.Error("No client available - both WebSocket and HTTP connections failed")
	return nil
}

// createWebSocketWithHeaders creates a WebSocket connection with API key for GCP authentication
func createWebSocketWithHeaders(wsURL, apiKey string) (*ethclient.Client, error) {
	// GCP WebSocket endpoints may support API key as query parameter
	// Try multiple approaches for WebSocket authentication

	ctx := context.Background()

	// Approach 1: Try with API key as query parameter
	wsURLWithKey := wsURL
	if strings.Contains(wsURL, "?") {
		wsURLWithKey = wsURL + "&key=" + apiKey
	} else {
		wsURLWithKey = wsURL + "?key=" + apiKey
	}

	logger.Sugar.Debugf("Trying WebSocket with API key parameter: %s", wsURLWithKey)
	rpcClient, err := ethrpc.DialOptions(ctx, wsURLWithKey)
	if err != nil {
		// Approach 2: Try with different parameter name
		if strings.Contains(wsURL, "?") {
			wsURLWithKey = wsURL + "&api_key=" + apiKey
		} else {
			wsURLWithKey = wsURL + "?api_key=" + apiKey
		}

		logger.Sugar.Debugf("Trying WebSocket with api_key parameter: %s", wsURLWithKey)
		rpcClient, err = ethrpc.DialOptions(ctx, wsURLWithKey)
		if err != nil {
			return nil, fmt.Errorf("failed to dial WebSocket with API key: %w", err)
		}
	}

	// Create ethclient from the RPC client
	return ethclient.NewClient(rpcClient), nil
}
