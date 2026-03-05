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
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-app-sdk/pkg/pruner"
	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/server"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/snapshot"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sourcenetwork/defradb/client"
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
	pruner                    *pruner.Pruner        // Document pruner for removing old blocks
	snapshotter               *snapshot.Snapshotter // Snapshot exporter for archiving blocks
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

		logger.Sugar.Debugf("P2P config: ListenAddr: '%s', BootstrapPeers: %v",
			appCfg.DefraDB.P2P.ListenAddr, appCfg.DefraDB.P2P.BootstrapPeers)
		logger.Sugar.Debugf("P2P config (original): ListenAddr: '%s', Enabled: %t",
			cfg.DefraDB.P2P.ListenAddr, cfg.DefraDB.P2P.Enabled)

		// When accept_incoming is false (default), reject all incoming P2P documents.
		// The indexer is the source of truth from the chain and should not accept
		// relayed data from peers to avoid storing multiple signatures.
		var replicationFilter client.ReplicationFilter
		if !cfg.DefraDB.P2P.AcceptIncoming {
			replicationFilter = &indexerReplicationFilter{}
		}

		defraNode, networkHandler, err := appsdk.StartDefraInstance(appCfg,
			appsdk.NewSchemaApplierFromProvidedSchema(schema.GetSchemaForBuild()), nil, replicationFilter, constants.AllCollections...)
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

		// Get the identity context for block signing
		identityCtx, err := appsdk.GetIdentityContext(ctx, appCfg)
		if err != nil {
			logger.Sugar.Warnf("Failed to get identity context for block signing: %v (block signatures may not work)", err)
		} else {
			ctx = identityCtx
			logger.Sugar.Info("Identity context initialized for block signing")
		}

	} else {
		// Using external DefraDB - wait for it and apply schema via HTTP
		err := defra.WaitForDefraDB(cfg.DefraDB.Url)
		if err != nil {
			return err
		}

		err = applySchemaViaHTTP(cfg.DefraDB.Url)
		if err != nil && !errors.IsErrAlreadyExists(err) {
			return fmt.Errorf("failed to apply schema to external DefraDB: %v", err)
		}
	}

	if i.defraNode == nil {
		return fmt.Errorf("defraNode is required - external DefraDB via HTTP is no longer supported")
	}

	blockHandler, err := defra.NewBlockHandler(i.defraNode, cfg.Indexer.MaxDocsPerTxn)
	if err != nil {
		return fmt.Errorf("failed to create block handler: %v", err)
	}
	logger.Sugar.Infof("Using direct DB access for embedded DefraDB (maxDocsPerTxn=%d)", cfg.Indexer.MaxDocsPerTxn)

	// Connect to Ethereum client early — needed for latest block query and indexing
	client, err := rpc.NewEthereumClient(cfg.Geth.NodeURL, cfg.Geth.WsURL, cfg.Geth.APIKey)
	if err != nil {
		logCtx := errors.LogContext(err)
		logger.Sugar.With(logCtx).Fatalf("Failed to connect to Ethereum client: %v", err)
	}
	defer client.Close()

	// Determine start height: DB state takes priority, then config, then latest chain block
	configuredHeight := int64(cfg.Indexer.StartHeight)
	var highestExisting int64
	var pruneQueue *pruner.IndexerQueue

	if cfg.Pruner.Enabled {
		pruneQueue = pruner.NewIndexerQueue()
		queueFilePath := filepath.Join(cfg.DefraDB.Store.Path, "prune_queue.gob")
		if loaded, err := pruneQueue.LoadFromFile(queueFilePath); err != nil {
			logger.Sugar.Warnf("Failed to load prune queue from disk: %v", err)
		} else if loaded > 0 {
			logger.Sugar.Infof("Restored %d entries from prune queue file", loaded)
		}
		highestExisting = pruneQueue.HighestBlockNumber()
	}

	if highestExisting == 0 {
		nBlock, err := blockHandler.GetHighestBlockNumber(ctx)
		if err != nil {
			logger.Sugar.Debugf("No existing blocks found in DB: %v", err)
		} else {
			highestExisting = nBlock
		}
	}

	// Query chain tip — used for gap detection and fresh-start fallback
	latestBlock, err := client.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest block number from RPC: %w", err)
	}
	chainTip := latestBlock.Int64()

	startBuffer := int64(cfg.Indexer.StartBuffer)

	if highestExisting > 0 {
		resumeFrom := highestExisting + 1
		gap := chainTip - highestExisting
		if gap > startBuffer {
			// Too far behind (e.g. VM was down) — skip ahead to near chain tip
			resumeFrom = chainTip - startBuffer
			logger.Sugar.Infof("Gap of %d blocks, skipping ahead to %d (chain tip: %d)", gap, resumeFrom, chainTip)
		}
		cfg.Indexer.StartHeight = int(resumeFrom)
		logger.Sugar.Infof("Resuming from block %d (highest existing: %d, chain tip: %d)", cfg.Indexer.StartHeight, highestExisting, chainTip)
	} else if configuredHeight > 0 {
		// DB is empty, specific start height configured — use it
		logger.Sugar.Infof("Starting from configured height %d (chain tip: %d)", configuredHeight, chainTip)
	} else {
		// DB is empty, no start height — start near chain tip
		cfg.Indexer.StartHeight = int(chainTip - startBuffer)
		if cfg.Indexer.StartHeight < 0 {
			cfg.Indexer.StartHeight = 0
		}
		logger.Sugar.Infof("No existing blocks, starting from %d (chain tip: %d)", cfg.Indexer.StartHeight, chainTip)
	}

	// create indexing bool
	i.shouldIndex = true

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
		if i.defraNode != nil {
			i.healthServer.SetDefraNode(i.defraNode)
		}

		go func() {
			if err := i.healthServer.Start(); err != nil {
				logger.Sugar.Errorf("Health server failed: %v", err)
			}
		}()

		if cfg.Indexer.OpenBrowserOnStart {
			go func() {
				time.Sleep(500 * time.Millisecond) // Give the server time to start
				openBrowser(fmt.Sprintf("http://localhost:%d/health", cfg.Indexer.HealthServerPort))
				logger.Sugar.Infof("Opened health page in browser")
			}()
		}
	}

	if cfg.Pruner.Enabled && i.defraNode != nil {
		i.pruner = pruner.NewPruner(&cfg.Pruner, i.defraNode)

		// pruneQueue was already created and loaded in the resume logic above
		if pruneQueue == nil {
			pruneQueue = pruner.NewIndexerQueue()
		}

		i.pruner.SetQueue(pruneQueue)
		blockHandler.SetDocIDTracker(&indexerQueueTracker{queue: pruneQueue})
		logger.Sugar.Infof("Prune queue ready (queue=%d, max_blocks=%d)", pruneQueue.Len(), cfg.Pruner.MaxBlocks)

		if err := i.pruner.Start(ctx); err != nil {
			logger.Sugar.Warnf("Failed to start pruner: %v", err)
		}
	}

	// Start snapshotter if enabled
	if cfg.Snapshot.Enabled && i.defraNode != nil {
		i.snapshotter = snapshot.New(&cfg.Snapshot, i.defraNode)
		if err := i.snapshotter.Start(ctx); err != nil {
			logger.Sugar.Warnf("Failed to start snapshotter: %v", err)
		}
		if i.healthServer != nil {
			i.healthServer.SetSnapshotter(i.snapshotter)
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
				if errors.IsErrNotFound(err) {
					// Block doesn't exist yet (we're ahead of the chain) - wait 3 seconds and try again
					logger.Sugar.Infof("Block %d not available yet (ahead of chain), waiting 3s before retry...", nextBlockToProcess)
					time.Sleep(3 * time.Second)
					continue
				} else if errors.IsErrAlreadyExists(err) {
					// Block already processed, move to next
					logger.Sugar.Infof("Block %d already processed, moving to next", nextBlockToProcess)
					nextBlockToProcess++
					i.hasIndexedAtLeastOneBlock = true
					continue
				} else if errors.IsErrUnsupportedTxType(err) {
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

	return i.processBlockBatch(ctx, ethClient, blockHandler, block, blockNum)
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

		if errors.IsErrAlreadyExists(err) {
			// Block exists via P2P, but we still need to sign it with our identity
			if _, signErr := blockHandler.CreateBlockSignatureForExistingBlock(ctx, blockNum, block.Hash, block, transactions, receipts); signErr != nil {
				logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", blockNum, signErr)
			}
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

func (i *ChainIndexer) StopIndexing() {
	i.shouldIndex = false
	i.isStarted = false

	// Stop snapshotter before pruner (capture data before it's pruned)
	if i.snapshotter != nil {
		i.snapshotter.Stop()
		i.snapshotter = nil
	}

	// Stop pruner
	if i.pruner != nil {
		i.pruner.Stop()
		i.pruner = nil
	}

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

	// Use NetworkHandler to determine if P2P is active
	networkActive := i.networkHandler != nil && i.networkHandler.IsNetworkActive()

	// Get this node's own peer info (listening addresses)
	ownAddresses, err := i.defraNode.DB.PeerInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("error fetching own peer info: %w", err)
	}
	ownPeers, _ := appsdk.BootstrapIntoPeers(ownAddresses)

	var selfInfo *server.PeerInfo
	if len(ownPeers) > 0 {
		// Collect all addresses for our own peer ID
		var addresses []string
		for _, p := range ownPeers {
			addresses = append(addresses, p.Addresses...)
		}
		selfInfo = &server.PeerInfo{
			ID:        ownPeers[0].ID,
			Addresses: addresses,
			PublicKey: extractPublicKeyFromPeerID(ownPeers[0].ID),
		}
	}

	// Get actually connected peers (may fail if P2P is not initialized)
	activePeerStrings, err := i.defraNode.DB.ActivePeers(ctx)
	if err != nil {
		activePeerStrings = nil // P2P not available, treat as no peers
	}
	activePeers, _ := appsdk.BootstrapIntoPeers(activePeerStrings)

	// Deduplicate peers by ID and merge addresses
	peerMap := make(map[string]*server.PeerInfo)
	for _, peer := range activePeers {
		if existing, ok := peerMap[peer.ID]; ok {
			existing.Addresses = append(existing.Addresses, peer.Addresses...)
		} else {
			peerMap[peer.ID] = &server.PeerInfo{
				ID:        peer.ID,
				Addresses: peer.Addresses,
				PublicKey: extractPublicKeyFromPeerID(peer.ID),
			}
		}
	}
	serverPeerInfo := make([]server.PeerInfo, 0, len(peerMap))
	for _, p := range peerMap {
		serverPeerInfo = append(serverPeerInfo, *p)
	}

	return &server.P2PInfo{
		Self:     selfInfo,
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

// GetPrunerMetrics returns the current pruner metrics, or nil if pruner is not enabled
func (i *ChainIndexer) GetPrunerMetrics() *pruner.Metrics {
	if i.pruner == nil {
		return nil
	}
	metrics := i.pruner.GetMetrics()
	return &metrics
}

// indexerQueueTracker adapts app-sdk's IndexerQueue to the local DocIDTrackerInterface.
type indexerQueueTracker struct {
	queue *pruner.IndexerQueue
}

func (t *indexerQueueTracker) TrackBlock(_ context.Context, blockNumber int64, result *defra.BlockCreationResult) error {
	otherDocIDs := map[string][]string{
		constants.CollectionTransaction:     result.TransactionIDs,
		constants.CollectionLog:             result.LogIDs,
		constants.CollectionAccessListEntry: result.AccessListIDs,
	}
	return t.queue.TrackBlockDocIDs(blockNumber, result.BlockID, otherDocIDs, result.BlockSignatureID)
}
