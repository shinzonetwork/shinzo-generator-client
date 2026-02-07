package indexer

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/server"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sourcenetwork/defradb/node"

	appConfig "github.com/shinzonetwork/shinzo-app-sdk/pkg/config"
	appsdk "github.com/shinzonetwork/shinzo-app-sdk/pkg/defra"
	"github.com/shinzonetwork/shinzo-app-sdk/pkg/signer"
)

const (
	// Default configuration constants - can be made configurable via config file
	DefaultBlocksToIndexAtOnce = 10
	DefaultRetryAttempts       = 3
	DefaultSchemaWaitTimeout   = 15 * time.Second
	DefaultDefraReadyTimeout   = 30 * time.Second
	// DefaultBlockOffset is the number of blocks behind the latest block to process
	// This prevents "transaction type not supported" errors from very recent blocks
	DefaultBlockOffset = 3
)

var requiredPeers []string = []string{} // Here, we can consider adding any "big peers" we need - these requiredPeers can be used as a quick start point to speed up the peer discovery process

const defaultListenAddress string = "/ip4/127.0.0.1/tcp/9171"

type ChainIndexer struct {
	cfg                       *config.Config
	shouldIndex               bool
	isStarted                 bool
	hasIndexedAtLeastOneBlock bool
	defraNode                 *node.Node             // Embedded DefraDB node (nil if using external)
	networkHandler            *appsdk.NetworkHandler // P2P network handler (nil if using external)
	healthServer              *server.HealthServer
	currentBlock              int64
	lastProcessedTime         time.Time
	mutex                     sync.RWMutex
}

func (i *ChainIndexer) IsStarted() bool {
	return i.isStarted
}

func (i *ChainIndexer) HasIndexedAtLeastOneBlock() bool {
	return i.hasIndexedAtLeastOneBlock
}

// GetDefraDBPort returns the port of the embedded DefraDB node, or -1 if using external DefraDB
func (i *ChainIndexer) GetDefraDBPort() int {
	if i.defraNode == nil {
		return -1
	}
	return defra.GetPort(i.defraNode)
}

func CreateIndexer(cfg *config.Config) (*ChainIndexer, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError(
			"indexer",
			"CreateIndexer",
			"config is nil",
			"host=nil, port=nil",
			nil,
			errors.WithMetadata("host", "nil"),
			errors.WithMetadata("port", "nil"))
	}
	return &ChainIndexer{
		cfg:                       cfg,
		shouldIndex:               false,
		isStarted:                 false,
		hasIndexedAtLeastOneBlock: false,
	}, nil
}

func toAppConfig(cfg *config.Config) *appConfig.Config {
	if cfg == nil {
		return nil
	}

	return &appConfig.Config{
		DefraDB: appConfig.DefraDBConfig{
			Url:           cfg.DefraDB.Url,
			KeyringSecret: cfg.DefraDB.KeyringSecret,
			P2P: appConfig.DefraP2PConfig{
				Enabled:             cfg.DefraDB.P2P.Enabled,
				BootstrapPeers:      cfg.DefraDB.P2P.BootstrapPeers,
				ListenAddr:          cfg.DefraDB.P2P.ListenAddr,
				MaxRetries:          cfg.DefraDB.P2P.MaxRetries,
				RetryBaseDelayMs:    cfg.DefraDB.P2P.RetryBaseDelayMs,
				ReconnectIntervalMs: cfg.DefraDB.P2P.ReconnectIntervalMs,
				EnableAutoReconnect: cfg.DefraDB.P2P.EnableAutoReconnect,
			},
			Store: appConfig.DefraStoreConfig{
				Path:                    cfg.DefraDB.Store.Path,
				BlockCacheMB:            cfg.DefraDB.Store.BlockCacheMB,
				MemTableMB:              cfg.DefraDB.Store.MemTableMB,
				IndexCacheMB:            cfg.DefraDB.Store.IndexCacheMB,
				NumCompactors:           cfg.DefraDB.Store.NumCompactors,
				NumLevelZeroTables:      cfg.DefraDB.Store.NumLevelZeroTables,
				NumLevelZeroTablesStall: cfg.DefraDB.Store.NumLevelZeroTablesStall,
			},
		},
	}
}

func (i *ChainIndexer) StartIndexing(defraStarted bool) error {
	ctx := context.Background()
	cfg := i.cfg

	if cfg == nil {
		return fmt.Errorf("configuration is required - use config.LoadConfig() to load configuration")
	}
	cfg.DefraDB.P2P.BootstrapPeers = append(cfg.DefraDB.P2P.BootstrapPeers, requiredPeers...)

	// Only initialize logger if it hasn't been initialized yet (e.g., in tests)
	if logger.Sugar == nil {
		logger.Init(cfg.Logger.Development)
	}

	if !defraStarted {
		// Use app-sdk to start DefraDB instance with persistent keys
		// Convert indexer config to app-sdk config
		appCfg := toAppConfig(cfg)
		// Note: app-sdk P2P config has no Enabled field - P2P should be enabled by ListenAddr

		// Debug: Log the P2P configuration being passed to app-sdk
		logger.Sugar.Warnf("=== P2P DEBUG === ListenAddr: '%s', BootstrapPeers: %v",
			appCfg.DefraDB.P2P.ListenAddr, appCfg.DefraDB.P2P.BootstrapPeers)
		logger.Sugar.Warnf("=== P2P DEBUG === Original config - ListenAddr: '%s', Enabled: %t",
			cfg.DefraDB.P2P.ListenAddr, cfg.DefraDB.P2P.Enabled)

		defraNode, networkHandler, err := appsdk.StartDefraInstance(appCfg,
			appsdk.NewSchemaApplierFromProvidedSchema(schema.GetSchemaForBuild()), nil, constants.AllCollections...)
		if err != nil {
			return fmt.Errorf("Failed to start DefraDB instance with app-sdk: %v", err)
		}
		// Store the defraNode and networkHandler references
		i.defraNode = defraNode
		i.networkHandler = networkHandler

		// Use the actual DefraDB URL from the started node, not the config URL
		actualDefraURL := defraNode.APIURL
		err = defra.WaitForDefraDB(actualDefraURL)
		if err != nil {
			return err
		}

		// Get the identity context for batch signing
		identityCtx, err := appsdk.GetIdentityContext(ctx, appCfg)
		if err != nil {
			logger.Sugar.Warnf("Failed to get identity context for batch signing: %v (batch signatures may not work)", err)
		} else {
			ctx = identityCtx
			logger.Sugar.Info("Identity context initialized for batch signing")
		}

	} else {
		// Using external DefraDB - wait for it and apply schema via HTTP
		err := defra.WaitForDefraDB(cfg.DefraDB.Url)
		if err != nil {
			return err
		}

		err = applySchemaViaHTTP(cfg.DefraDB.Url)
		if err != nil && !strings.Contains(err.Error(), "collection already exists") {
			return fmt.Errorf("failed to apply schema to external DefraDB: %v", err)
		}
	}

	var blockHandler *defra.BlockHandler
	var blockHandlerErr error
	if !defraStarted && i.defraNode != nil {
		blockHandler, blockHandlerErr = defra.NewBlockHandlerWithNode(i.defraNode, cfg.Indexer.MaxDocsPerTxn)
		if blockHandlerErr != nil {
			return fmt.Errorf("failed to create block handler with node: %v", blockHandlerErr)
		}
		logger.Sugar.Infof("Using direct DB access for embedded DefraDB (maxDocsPerTxn=%d)", cfg.Indexer.MaxDocsPerTxn)
	} else {
		blockHandler, blockHandlerErr = defra.NewBlockHandler(cfg.DefraDB.Url)
		if blockHandlerErr != nil {
			return fmt.Errorf("failed to create block handler for block check: %v", blockHandlerErr)
		}
		logger.Sugar.Info("Using HTTP access for external DefraDB")
	}

	startHeight := int64(cfg.Indexer.StartHeight)

	nBlock, err := blockHandler.GetHighestBlockNumber(ctx)
	if err != nil {
		// if error.
		// If no blocks exist, start from configured start height (error is expected)
		logger.Sugar.Info("No existing blocks found, starting from configured height")
	} else if nBlock > 0 && nBlock > startHeight {
		// if nBlock is greater than startHeight; use block from defra
		// if yes increment by 1
		cfg.Indexer.StartHeight = int(nBlock + 1)
		logger.Sugar.Infof("Found existing blocks up to %d, starting from %d", nBlock, cfg.Indexer.StartHeight)
	} else {
		// if nBlock is less than startHeight
		logger.Sugar.Infof("No existing blocks found, starting from configured height")
	}

	// create indexing bool
	i.shouldIndex = true

	// Connect to Ethereum client with WebSocket and HTTP support
	client, err := rpc.NewEthereumClient(cfg.Geth.NodeURL, cfg.Geth.WsURL, cfg.Geth.APIKey)
	if err != nil {
		logCtx := errors.LogContext(err)
		logger.Sugar.With(logCtx).Fatalf("Failed to connect to Ethereum client: %v", err)
	}
	defer client.Close()

	// Reuse the block handler created earlier for processing
	// (blockHandler was already created above for the block check)

	logger.Sugar.Info("Starting indexer - will process latest blocks from Geth ", cfg.Geth.NodeURL)

	// Get starting block number
	nextBlockToProcess := int64(cfg.Indexer.StartHeight)

	if cfg.Indexer.HealthServerPort > 0 {
		var healthDefraURL string
		if cfg.DefraDB.Url != "" {
			healthDefraURL = cfg.DefraDB.Url
		} else if i.defraNode != nil {
			healthDefraURL = fmt.Sprintf("http://localhost:%d", defra.GetPort(i.defraNode))
		}
		i.healthServer = server.NewHealthServer(cfg.Indexer.HealthServerPort, i, healthDefraURL)

		go func() {
			if err := i.healthServer.Start(); err != nil {
				logger.Sugar.Errorf("Health server failed: %v", err)
			}
		}()

		if cfg.Indexer.OpenBrowserOnStart {
			go func() {
				time.Sleep(2 * time.Second)
				openBrowser(fmt.Sprintf("http://localhost:%d/health", cfg.Indexer.HealthServerPort))
			}()
		}
	}

	// Use concurrent processing if configured and using embedded DefraDB
	if cfg.Indexer.ConcurrentBlocks >= 1 && i.defraNode != nil {
		logger.Sugar.Infof("Using concurrent block processing with %d workers",
			cfg.Indexer.ConcurrentBlocks)
		return i.runConcurrentIndexing(ctx, client, blockHandler, nextBlockToProcess, cfg)
	}

	// Sequential processing (original behavior)
	for i.shouldIndex {
		i.isStarted = true

		select {
		case <-ctx.Done():
			logger.Sugar.Info("Real-time indexing stopped")
			return nil
		default:
			// Process the specific block we want (nextBlockToProcess)
			logger.Sugar.Infof("=== Processing block %d ===", nextBlockToProcess)

			err := i.processBlock(ctx, client, blockHandler, nextBlockToProcess)
			if err != nil {
				if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
					// Block doesn't exist yet (we're ahead of the chain) - wait 3 seconds and try again
					logger.Sugar.Infof("Block %d not available yet (ahead of chain), waiting 3s before retry...", nextBlockToProcess)
					time.Sleep(3 * time.Second)
					continue
				} else if strings.Contains(err.Error(), "already exists") {
					// Block already processed, move to next
					logger.Sugar.Infof("Block %d already processed, moving to next", nextBlockToProcess)
					nextBlockToProcess++
					i.hasIndexedAtLeastOneBlock = true
					continue
				} else if strings.Contains(err.Error(), "transaction type not supported") {
					// Skip problematic block
					logger.Sugar.Warnf("Block %d has unsupported transaction types, skipping", nextBlockToProcess)
					nextBlockToProcess++
					i.hasIndexedAtLeastOneBlock = true
					continue
				} else {
					// Other error - retry in 3 seconds
					logger.Sugar.Errorf("Failed to process block %d: %v, retrying in 3s", nextBlockToProcess, err)
					time.Sleep(3 * time.Second)
					continue
				}
			}

			// Success! Move to next block (Step 3: increment by 1 and repeat)
			logger.Sugar.Infof("Successfully processed block %d", nextBlockToProcess)
			nextBlockToProcess++
			i.hasIndexedAtLeastOneBlock = true
		}
	}

	return nil
}

// runConcurrentIndexing runs the indexer with concurrent block processing.
func (i *ChainIndexer) runConcurrentIndexing(
	ctx context.Context,
	client *rpc.EthereumClient,
	blockHandler *defra.BlockHandler,
	startBlock int64,
	cfg *config.Config,
) error {
	i.shouldIndex = true
	i.isStarted = true

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		client,
		cfg.Indexer.ConcurrentBlocks,
		cfg.Indexer.ReceiptWorkers,
		cfg.Indexer.BlocksPerMinute,
	)

	return processor.ProcessBlocks(ctx, startBlock, func(blockNum int64) {
		i.updateBlockInfo(blockNum)
		i.hasIndexedAtLeastOneBlock = true
	})
}

// processBlock fetches and stores a single block with retry logic
func (i *ChainIndexer) processBlock(ctx context.Context, ethClient *rpc.EthereumClient, blockHandler *defra.BlockHandler, blockNum int64) error {
	var block *types.Block
	var err error

	// Retry logic for fetching block from Ethereum
	for attempt := range DefaultRetryAttempts {
		block, err = ethClient.GetBlockByNumber(ctx, big.NewInt(blockNum))
		if err == nil {
			break
		}

		if attempt < DefaultRetryAttempts-1 {
			retryDelay := time.Duration(attempt+1) * time.Second
			logger.Sugar.Warnf("Failed to fetch block %d (attempt %d/%d): %v, retrying in %v",
				blockNum, attempt+1, DefaultRetryAttempts, err, retryDelay)
			time.Sleep(retryDelay)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to fetch block %d after %d attempts: %w", blockNum, DefaultRetryAttempts, err)
	}

	if i.defraNode != nil {
		return i.processBlockBatch(ctx, ethClient, blockHandler, block, blockNum)
	}

	return i.processSingleBlock(ctx, ethClient, blockHandler, block, blockNum)
}

// processBlockBatch creates all documents for a block using optimized batch mutations.
// This streams receipts as they arrive and processes them concurrently with fetching,
// reducing latency compared to waiting for all receipts before processing.
func (i *ChainIndexer) processBlockBatch(ctx context.Context, ethClient *rpc.EthereumClient, blockHandler *defra.BlockHandler, block *types.Block, blockNum int64) error {
	type txWithReceipt struct {
		tx      *types.Transaction
		receipt *types.TransactionReceipt
	}
	receiptWorkers := i.cfg.Indexer.ReceiptWorkers
	receiptChan := make(chan txWithReceipt, receiptWorkers*2)

	var fetchWg sync.WaitGroup
	receiptSem := make(chan struct{}, receiptWorkers)

	for idx := range block.Transactions {
		tx := &block.Transactions[idx]
		fetchWg.Add(1)
		go func(tx *types.Transaction) {
			defer fetchWg.Done()
			receiptSem <- struct{}{}
			defer func() { <-receiptSem }()

			receipt, err := ethClient.GetTransactionReceipt(ctx, tx.Hash)
			if err != nil {
				logger.Sugar.Warnf("Failed to get receipt for tx %s: %v", tx.Hash, err)
				return
			}
			receiptChan <- txWithReceipt{tx: tx, receipt: receipt}
		}(tx)
	}

	go func() {
		fetchWg.Wait()
		close(receiptChan)
	}()

	var transactions []*types.Transaction
	var receipts []*types.TransactionReceipt
	for result := range receiptChan {
		transactions = append(transactions, result.tx)
		receipts = append(receipts, result.receipt)
	}

	var err error
	for attempt := range DefaultRetryAttempts {
		_, err = blockHandler.CreateBlockBatch(ctx, block, transactions, receipts)
		if err == nil {
			break
		}

		if strings.Contains(err.Error(), "already exists") {
			logger.Sugar.Infof("Block %d already exists in DefraDB, skipping...", blockNum)
			return nil
		}

		if attempt < DefaultRetryAttempts-1 {
			retryDelay := time.Duration(attempt+1) * time.Second
			logger.Sugar.Warnf("Failed to batch create block %d (attempt %d/%d): %v, retrying in %v",
				blockNum, attempt+1, DefaultRetryAttempts, err, retryDelay)
			time.Sleep(retryDelay)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to batch create block %d after %d attempts: %w", blockNum, DefaultRetryAttempts, err)
	}

	logger.Sugar.Infof("Successfully batch processed block %d with %d transactions", blockNum, len(block.Transactions))
	i.updateBlockInfo(blockNum)
	return nil
}

// processSingleBlock creates documents one at a time
func (i *ChainIndexer) processSingleBlock(ctx context.Context, ethClient *rpc.EthereumClient, blockHandler *defra.BlockHandler, block *types.Block, blockNum int64) error {
	var err error
	var blockId string

	// Retry logic for storing block in DefraDB
	for attempt := range DefaultRetryAttempts {
		blockId, err = blockHandler.CreateBlock(ctx, block)
		if err == nil {
			break
		}

		// Handle duplicate block - skip if already exists
		if strings.Contains(err.Error(), "already exists") {
			logger.Sugar.Infof("Block %d already exists in DefraDB, skipping...", blockNum)
			return nil
		}

		if attempt < DefaultRetryAttempts-1 {
			retryDelay := time.Duration(attempt+1) * time.Second
			logger.Sugar.Warnf("Failed to create block %d in DefraDB (attempt %d/%d): %v, retrying in %v",
				blockNum, attempt+1, DefaultRetryAttempts, err, retryDelay)
			time.Sleep(retryDelay)
		}
	}
	if err != nil {
		return fmt.Errorf("failed to create block %d in DefraDB after %d attempts: %w", blockNum, DefaultRetryAttempts, err)
	}

	// Check if schema uses branchable collections - requires sequential processing
	if schema.IsBranchable() {
		// Process transactions sequentially to avoid transaction conflicts with branchable collections
		for idx := range block.Transactions {
			tx := block.Transactions[idx]
			i.processTransaction(ctx, ethClient, blockHandler, &tx, blockId)
		}
	} else {
		// Process transactions in parallel for better performance (non-branchable)
		var wg sync.WaitGroup
		txSemaphore := make(chan struct{}, 20)

		for idx := range block.Transactions {
			tx := block.Transactions[idx]
			wg.Add(1)
			go func(tx types.Transaction) {
				defer wg.Done()
				txSemaphore <- struct{}{}
				defer func() { <-txSemaphore }()
				i.processTransaction(ctx, ethClient, blockHandler, &tx, blockId)
			}(tx)
		}
		wg.Wait()
	}

	logger.Sugar.Infof("Successfully processed block %d with %d transactions", blockNum, len(block.Transactions))
	i.updateBlockInfo(blockNum)
	return nil
}

// processTransaction handles a single transaction with its logs and access list entries
func (i *ChainIndexer) processTransaction(ctx context.Context, ethClient *rpc.EthereumClient, blockHandler *defra.BlockHandler, tx *types.Transaction, blockId string) {
	// Retry logic for creating transaction
	var txId string
	var txErr error
	for attempt := range DefaultRetryAttempts {
		txId, txErr = blockHandler.CreateTransaction(ctx, tx, blockId)
		if txErr == nil {
			break
		}

		if attempt < DefaultRetryAttempts-1 {
			retryDelay := time.Duration(attempt+1) * time.Second
			logger.Sugar.Warnf("Failed to create transaction %s (attempt %d/%d): %v, retrying in %v",
				tx.Hash, attempt+1, DefaultRetryAttempts, txErr, retryDelay)
			time.Sleep(retryDelay)
		}
	}
	if txErr != nil {
		logger.Sugar.Errorf("Failed to create transaction %s after %d attempts: %v", tx.Hash, DefaultRetryAttempts, txErr)
		return
	}

	// Fetch transaction receipt
	receipt, txErr := ethClient.GetTransactionReceipt(ctx, tx.Hash)
	if txErr != nil {
		logger.Sugar.Errorf("Failed to get receipt for transaction %s: %v", tx.Hash, txErr)
		return
	}

	// Store access list entries
	blockNumStr := strings.TrimPrefix(tx.BlockNumber, "0x")
	blockNumBig, _ := new(big.Int).SetString(blockNumStr, 16)
	blockNum := blockNumBig.Int64()
	for _, accessListEntry := range tx.AccessList {
		_, err := blockHandler.CreateAccessListEntry(ctx, &accessListEntry, txId, blockNum)
		if err != nil {
			logger.Sugar.Errorf("Failed to create access list entry for tx %s: %v", tx.Hash, err)
			continue
		}
	}

	// Store transaction logs from receipt
	for _, log := range receipt.Logs {
		_, err := blockHandler.CreateLog(ctx, &log, blockId, txId)
		if err != nil {
			logger.Sugar.Errorf("Failed to create log for tx %s: %v", tx.Hash, err)
			continue
		}
	}

	logger.Sugar.Infof("Processed transaction %s with %d access list entries and %d logs", tx.Hash, len(tx.AccessList), len(receipt.Logs))
}

func (i *ChainIndexer) StopIndexing() {
	i.shouldIndex = false
	i.isStarted = false

	// Stop health server
	if i.healthServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		i.healthServer.Stop(ctx)
	}

	// Stop P2P network handler before closing the node
	if i.networkHandler != nil {
		i.networkHandler.StopNetwork()
		i.networkHandler = nil
	}

	// Close embedded DefraDB node if it exists
	if i.defraNode != nil {
		i.defraNode.Close(context.Background())
		i.defraNode = nil
	}
}

// Health checker interface implementation
func (i *ChainIndexer) IsHealthy() bool {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	// Consider healthy if started and processed at least one block recently
	if !i.isStarted {
		return false
	}

	// If we've never processed a block, we're still healthy (starting up)
	if i.lastProcessedTime.IsZero() {
		return true
	}

	// Consider unhealthy if no blocks processed in last 10 minutes
	return time.Since(i.lastProcessedTime) < 10*time.Minute
}

func (i *ChainIndexer) GetCurrentBlock() int64 {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.currentBlock
}

func (i *ChainIndexer) GetLastProcessedTime() time.Time {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.lastProcessedTime
}

// GetPeerInfo returns DefraDB P2P network information
func (i *ChainIndexer) GetPeerInfo() (*server.P2PInfo, error) {
	i.mutex.RLock()
	defer i.mutex.RUnlock()

	// If no embedded DefraDB node, return nil
	if i.defraNode == nil {
		return nil, nil
	}

	ctx := context.Background()

	// Get peer information from DefraDB
	peerInfoString, err := i.defraNode.DB.PeerInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("error fetching peer info: %w", err)
	}
	peerInfo, errors := appsdk.BootstrapIntoPeers(peerInfoString)
	if len(errors) > 0 {
		return nil, fmt.Errorf("error turning bootstrap peer strings into peer info objects: %v", errors)
	}

	// Convert addresses to string slice
	serverPeerInfo := make([]server.PeerInfo, len(peerInfo))
	for idx, peer := range peerInfo {
		publicKey := extractPublicKeyFromPeerID(peer.ID)
		serverPeerInfo[idx] = server.PeerInfo{
			ID:        peer.ID,
			Addresses: peer.Addresses,
			PublicKey: publicKey,
		}
	}

	// Use NetworkHandler to determine if P2P is active
	networkActive := i.networkHandler != nil && i.networkHandler.IsNetworkActive()

	return &server.P2PInfo{
		PeerInfo: serverPeerInfo,
		Enabled:  networkActive,
	}, nil
}

// extractPublicKeyFromPeerID attempts to extract the public key from a libp2p PeerID
func extractPublicKeyFromPeerID(peerID string) string {
	// Parse the PeerID string into a libp2p peer.ID
	id, err := peer.Decode(peerID)
	if err != nil {
		logger.Sugar.Warnf("Failed to decode PeerID %s: %v", peerID, err)
		return ""
	}

	// Extract the public key from the PeerID
	pubKey, err := id.ExtractPublicKey()
	if err != nil {
		logger.Sugar.Warnf("Failed to extract public key from PeerID %s: %v", peerID, err)
		return ""
	}

	// Convert public key to bytes and then to hex string
	pubKeyBytes, err := pubKey.Raw()
	if err != nil {
		logger.Sugar.Warnf("Failed to get raw bytes from public key: %v", err)
		return ""
	}

	// Return hex-encoded public key
	return hex.EncodeToString(pubKeyBytes)
}

// updateBlockInfo updates the current block and last processed time
func (i *ChainIndexer) updateBlockInfo(blockNum int64) {
	i.mutex.Lock()
	defer i.mutex.Unlock()
	i.currentBlock = blockNum
	i.lastProcessedTime = time.Now()
}

// openBrowser opens the specified URL in the default browser
func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // linux and others
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		logger.Sugar.Warnf("Failed to open browser: %v", err)
		return
	}
	logger.Sugar.Infof("Opened health page in browser: %s", url)
}

func applySchemaViaHTTP(defraUrl string) error {
	fmt.Println("Applying schema via HTTP...")

	schema := schema.GetSchema()
	// Apply schema via REST API endpoint
	schemaURL := fmt.Sprintf("%s/api/v0/schema", defraUrl)
	resp, err := http.Post(schemaURL, "application/schema", bytes.NewBuffer([]byte(schema)))
	if err != nil {
		return fmt.Errorf("Failed to send schema: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Schema application failed with status %d: %s", resp.StatusCode, string(body))
	}

	fmt.Println("Schema applied successfully!")
	return nil
}

func (i *ChainIndexer) SignMessages(message string) (server.DefraPKRegistration, server.PeerIDRegistration, error) {
	signedMsg, err := signer.SignWithDefraKeys(message, i.defraNode, toAppConfig(i.cfg))
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, err
	}

	// Sign with peer ID
	peerSignedMsg, err := signer.SignWithP2PKeys(message, i.defraNode, toAppConfig(i.cfg))
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, err
	}

	// Get node and peer public keys from signer helpers
	nodePubKey, err := i.GetNodePublicKey()
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, fmt.Errorf("failed to get node public key: %w", err)
	}

	peerPubKey, err := i.GetPeerPublicKey()
	if err != nil {
		return server.DefraPKRegistration{}, server.PeerIDRegistration{}, fmt.Errorf("failed to get peer public key: %w", err)
	}

	return server.DefraPKRegistration{
			PublicKey:   nodePubKey,
			SignedPKMsg: signedMsg,
		}, server.PeerIDRegistration{
			PeerID:        peerPubKey,
			SignedPeerMsg: peerSignedMsg,
		}, nil
}

func (i *ChainIndexer) GetNodePublicKey() (string, error) {
	return signer.GetDefraPublicKey(i.defraNode, toAppConfig(i.cfg))
}

func (i *ChainIndexer) GetPeerPublicKey() (string, error) {
	return signer.GetP2PPublicKey(i.defraNode, toAppConfig(i.cfg))
}
