package rpc

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
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

// GetLatestBlock fetches the latest block
func (c *EthereumClient) GetLatestBlock(ctx context.Context) (*types.Block, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}

	// Get the latest block number first
	latestHeader, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest header: %w", err)
	}

	// For GCP Erigon nodes, start with blocks that are significantly behind
	// to avoid transaction type compatibility issues
	const initialBlocksBack = 100
	targetBlockNumber := big.NewInt(1).Sub(latestHeader.Number, big.NewInt(initialBlocksBack))
	logger.Sugar.Infof("Latest block: %s, targeting block: %s (%d blocks behind for Erigon compatibility)",
		latestHeader.Number.String(), targetBlockNumber.String(), initialBlocksBack)

	var gethBlock *ethtypes.Block

	// Try progressively older blocks if transaction type errors occur
	for retries := 0; retries < 8; retries++ {
		gethBlock, err = client.BlockByNumber(ctx, targetBlockNumber)
		if err != nil {
			if strings.Contains(err.Error(), "transaction type not supported") ||
				strings.Contains(err.Error(), "invalid transaction type") {

				if retries < 7 {
					// Go back exponentially further: 100, 200, 400, 800, 1600, 3200, 6400 blocks
					blocksBack := initialBlocksBack * (1 << uint(retries+1))
					targetBlockNumber = big.NewInt(1).Sub(latestHeader.Number, big.NewInt(int64(blocksBack)))
					logger.Sugar.Warnf("Retry %d: Transaction type error with Erigon, going back %d blocks total...",
						retries+1, blocksBack)

					// Add progressive delay to prevent API rate limiting
					time.Sleep(time.Duration(retries+1) * time.Second)
					continue
				} else {
					logger.Sugar.Errorf("Failed after %d retries due to transaction type compatibility with Erigon", retries+1)
					return nil, fmt.Errorf("transaction type not supported by GCP Erigon node after %d retries", retries+1)
				}
			}
			// For non-transaction-type errors, fail immediately
			return nil, fmt.Errorf("failed to get block: %w", err)
		}

		// Success - log which block we're actually processing
		if retries > 0 {
			logger.Sugar.Infof("Successfully retrieved block %s after %d retries (Erigon compatibility)",
				targetBlockNumber.String(), retries)
		}
		break
	}

	return c.convertGethBlock(gethBlock), nil
}

// GetBlockByNumber fetches a block by number
func (c *EthereumClient) GetBlockByNumber(ctx context.Context, blockNumber *big.Int) (*types.Block, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}

	gethBlock, err := client.BlockByNumber(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %v: %w", blockNumber, err)
	}

	return c.convertGethBlock(gethBlock), nil
}

// GetNetworkID returns the network ID
func (c *EthereumClient) GetNetworkID(ctx context.Context) (*big.Int, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}

	return client.NetworkID(ctx)
}

// GetLatestBlockNumber returns just the latest block number (not the offset block)
func (c *EthereumClient) GetLatestBlockNumber(ctx context.Context) (*big.Int, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}

	latestHeader, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest header: %w", err)
	}

	return latestHeader.Number, nil
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

// GetBlockReceipts fetches all receipts for a block in a single RPC call.
func (c *EthereumClient) GetBlockReceipts(ctx context.Context, blockNumber *big.Int) ([]*types.TransactionReceipt, error) {
	client := c.getPreferredClient()
	if client == nil {
		return nil, fmt.Errorf("no client available")
	}
	receipts, err := client.BlockReceipts(ctx, ethrpc.BlockNumberOrHashWithNumber(ethrpc.BlockNumber(blockNumber.Int64())))
	if err != nil {
		return nil, fmt.Errorf("failed to get block receipts for block %v: %w", blockNumber, err)
	}
	result := make([]*types.TransactionReceipt, len(receipts))
	for i, receipt := range receipts {
		result[i] = c.convertGethReceipt(receipt)
	}
	return result, nil
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
		Data:             common.Bytes2Hex(log.Data),
		BlockNumber:      fmt.Sprintf("%d", log.BlockNumber),
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
		return "1"
	}
	return "0"
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

	// Convert uncles
	uncles := make([]string, len(gethBlock.Uncles()))
	for i, uncle := range gethBlock.Uncles() {
		uncles[i] = uncle.Hash().Hex()
	}

	// Convert the block
	return &types.Block{
		Hash:             gethBlock.Hash().Hex(),
		Number:           fmt.Sprintf("%d", gethBlock.NumberU64()),
		Timestamp:        fmt.Sprintf("%d", gethBlock.Time()),
		ParentHash:       gethBlock.ParentHash().Hex(),
		Difficulty:       gethBlock.Difficulty().String(),
		TotalDifficulty:  "", // Will be populated separately if needed
		GasUsed:          fmt.Sprintf("%d", gethBlock.GasUsed()),
		GasLimit:         fmt.Sprintf("%d", gethBlock.GasLimit()),
		BaseFeePerGas:    getBaseFeePerGas(gethBlock),
		Nonce:            fmt.Sprintf("%d", gethBlock.Nonce()),
		Miner:            gethBlock.Coinbase().Hex(),
		Size:             fmt.Sprintf("%d", gethBlock.Size()),
		StateRoot:        gethBlock.Root().Hex(),
		Sha3Uncles:       gethBlock.UncleHash().Hex(),
		TransactionsRoot: gethBlock.TxHash().Hex(),
		ReceiptsRoot:     gethBlock.ReceiptHash().Hex(),
		LogsBloom:        common.Bytes2Hex(gethBlock.Bloom().Bytes()),
		ExtraData:        common.Bytes2Hex(gethBlock.Extra()),
		MixHash:          gethBlock.MixDigest().Hex(),
		Uncles:           uncles,
		Transactions:     transactions,
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
	default:
		// For unknown transaction types, try to get gas price
		// If it fails, we'll catch it in the calling function
		gasPrice = tx.GasPrice()
	}

	// Extract signature components
	v, r, s := tx.RawSignatureValues()

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
		Hash:                 tx.Hash().Hex(),                          // string
		BlockHash:            gethBlock.Hash().Hex(),                   // string
		BlockNumber:          fmt.Sprintf("%d", gethBlock.NumberU64()), // string
		From:                 fromAddrStr,                              // string
		To:                   toAddr,                                   // string
		Value:                tx.Value().String(),                      // string
		Gas:                  fmt.Sprintf("%d", tx.Gas()),              // string
		GasPrice:             gasPrice.String(),                        // string
		MaxFeePerGas:         getMaxFeePerGas(tx),                      // string
		MaxPriorityFeePerGas: getMaxPriorityFeePerGas(tx),              // string
		Input:                "0x" + common.Bytes2Hex(tx.Data()),       // string
		Nonce:                fmt.Sprintf("%d", tx.Nonce()),            // string
		TransactionIndex:     index,                                    // int
		Type:                 fmt.Sprintf("%d", tx.Type()),             // string
		ChainId:              getChainId(tx),                           // string
		AccessList:           accessList,                               // []accessListEntry
		V:                    v.String(),                               // string
		R:                    r.String(),                               // string
		S:                    s.String(),                               // string
		Status:               true,                                     // Default to true, will be updated from receipt
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
