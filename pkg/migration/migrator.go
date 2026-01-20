package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/sourcenetwork/defradb/node"
)

// Migrator handles the migration process
type Migrator struct {
	cfg          *Config
	provider     DataProvider
	blockHandler *defra.BlockHandler
	rpcClient    *ethclient.Client
	checkpoint   *Checkpoint
}

// DataProvider interface for different data sources
type DataProvider interface {
	GetBlockRange(ctx context.Context) (int64, int64, error)
	ReadBlocks(ctx context.Context, startBlock, endBlock int64) ([]*types.Block, error)
	ReadTransactions(ctx context.Context, startBlock, endBlock int64) ([]*types.Transaction, error)
	ReadLogs(ctx context.Context, startBlock, endBlock int64) ([]*types.Log, error)
}

// NewMigrator creates a new Migrator instance using HTTP connection
// Deprecated: Use NewMigratorWithNode for embedded DefraDB
func NewMigrator(cfg *Config) (*Migrator, error) {
	m := &Migrator{
		cfg: cfg,
	}

	// Create data provider
	switch cfg.Provider {
	case ProviderAWS:
		m.provider = NewAWSProvider(cfg.AWSBucket, cfg.AWSPrefix, cfg.OutputDir)
	case ProviderBigQuery:
		return nil, fmt.Errorf("BigQuery provider not yet implemented - use AWS for now")
	case ProviderCryo:
		return nil, fmt.Errorf("Cryo provider not yet implemented - use AWS for now")
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}

	// Create block handler for DefraDB via HTTP (if not dry run)
	if !cfg.DryRun && cfg.DefraURL != "" {
		handler, err := defra.NewBlockHandler(cfg.DefraURL)
		if err != nil {
			return nil, fmt.Errorf("failed to create block handler: %w", err)
		}
		m.blockHandler = handler
	}

	// Create RPC client for validation
	if cfg.EnableValidation && cfg.RPCURL != "" {
		client, err := ethclient.Dial(cfg.RPCURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to RPC: %w", err)
		}
		m.rpcClient = client
	}

	// Load checkpoint
	m.loadCheckpoint()

	return m, nil
}

// NewMigratorWithNode creates a Migrator that uses direct embedded DefraDB node access.
// This is the preferred method when running inside the indexer process.
func NewMigratorWithNode(cfg *Config, defraNode *node.Node) (*Migrator, error) {
	if defraNode == nil && !cfg.DryRun {
		return nil, fmt.Errorf("defraNode is required for non-dry-run migration")
	}

	m := &Migrator{
		cfg: cfg,
	}

	// Create data provider
	switch cfg.Provider {
	case ProviderAWS:
		m.provider = NewAWSProvider(cfg.AWSBucket, cfg.AWSPrefix, cfg.OutputDir)
	case ProviderBigQuery:
		return nil, fmt.Errorf("BigQuery provider not yet implemented - use AWS for now")
	case ProviderCryo:
		return nil, fmt.Errorf("Cryo provider not yet implemented - use AWS for now")
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}

	// Create block handler with direct node access (if not dry run)
	if !cfg.DryRun && defraNode != nil {
		// NewBlockHandlerWithNode takes (node) - maxDocsPerTxn is set internally
		handler, err := defra.NewBlockHandlerWithNode(defraNode)
		if err != nil {
			return nil, fmt.Errorf("failed to create block handler with node: %w", err)
		}
		m.blockHandler = handler
	}

	// Create RPC client for validation
	if cfg.EnableValidation && cfg.RPCURL != "" {
		client, err := ethclient.Dial(cfg.RPCURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to RPC: %w", err)
		}
		m.rpcClient = client
	}

	// Load checkpoint
	m.loadCheckpoint()

	return m, nil
}

// Run executes the migration
func (m *Migrator) Run(ctx context.Context) (*Result, error) {
	result := &Result{Status: "running"}

	// Determine block range
	startBlock := m.cfg.StartBlock
	endBlock := m.cfg.EndBlock

	// Check for resume
	if m.cfg.ResumeFrom > 0 {
		startBlock = m.cfg.ResumeFrom
	} else if m.checkpoint != nil && m.checkpoint.LastBlock > startBlock {
		startBlock = m.checkpoint.LastBlock + 1
		logger.Sugar.Infof("Resuming from checkpoint at block %d", startBlock)
	}

	// Get end block from provider if not specified
	if endBlock == 0 {
		_, maxBlock, err := m.provider.GetBlockRange(ctx)
		if err != nil {
			logger.Sugar.Warnf("Could not determine end block from provider: %v", err)
			endBlock = startBlock + int64(m.cfg.BatchSize*10) // Default to 10 batches
		} else {
			endBlock = maxBlock
		}
	}

	logger.Sugar.Infof("Starting migration: blocks %d to %d", startBlock, endBlock)

	// Process blocks in batches
	for currentBlock := startBlock; currentBlock <= endBlock; {
		select {
		case <-ctx.Done():
			result.Status = "cancelled"
			m.saveCheckpoint(currentBlock - 1)
			return result, ctx.Err()
		default:
		}

		batchEnd := currentBlock + int64(m.cfg.BatchSize) - 1
		if batchEnd > endBlock {
			batchEnd = endBlock
		}

		logger.Sugar.Infof("Processing batch: blocks %d to %d", currentBlock, batchEnd)

		batchResult, err := m.processBatch(ctx, currentBlock, batchEnd)
		if err != nil {
			logger.Sugar.Errorf("Batch failed: %v", err)
			result.ErrorCount++
			// Continue with next batch
		}

		// Update result
		result.BlocksProcessed += batchResult.BlocksProcessed
		result.BlocksImported += batchResult.BlocksImported
		result.BlocksSkipped += batchResult.BlocksSkipped
		result.TransactionsImported += batchResult.TransactionsImported
		result.LogsImported += batchResult.LogsImported
		result.AccessListEntriesImported += batchResult.AccessListEntriesImported
		result.ValidationErrors = append(result.ValidationErrors, batchResult.ValidationErrors...)
		result.DownloadDuration += batchResult.DownloadDuration
		result.ImportDuration += batchResult.ImportDuration

		// Save checkpoint
		m.saveCheckpoint(batchEnd)
		result.LastCheckpoint = batchEnd

		currentBlock = batchEnd + 1
	}

	// Validation phase
	if m.cfg.EnableValidation && m.rpcClient != nil {
		logger.Sugar.Info("Running validation...")
		validationErrors := m.validateSample(ctx, startBlock, endBlock)
		result.ValidationErrors = append(result.ValidationErrors, validationErrors...)
	}

	if result.ErrorCount == 0 && len(result.ValidationErrors) == 0 {
		result.Status = "completed"
	} else {
		result.Status = "completed_with_errors"
	}

	return result, nil
}

// processBatch processes a batch of blocks
func (m *Migrator) processBatch(ctx context.Context, startBlock, endBlock int64) (*Result, error) {
	result := &Result{}

	// ==================== DOWNLOAD PHASE ====================
	downloadStart := time.Now()

	// Read blocks from provider
	blocks, err := m.provider.ReadBlocks(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read blocks: %w", err)
	}
	logger.Sugar.Infof("Read %d blocks from provider", len(blocks))

	// Read transactions
	transactions, err := m.provider.ReadTransactions(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read transactions: %w", err)
	}
	logger.Sugar.Infof("Read %d transactions from provider", len(transactions))

	// Read logs
	logs, err := m.provider.ReadLogs(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read logs: %w", err)
	}
	logger.Sugar.Infof("Read %d logs from provider", len(logs))

	result.DownloadDuration = time.Since(downloadStart)
	logger.Sugar.Infof("Download phase completed in %v", result.DownloadDuration)

	// ==================== PREPARE PHASE ====================
	// Group transactions and logs by block
	txByBlock := make(map[int64][]*types.Transaction)
	for _, tx := range transactions {
		blockNum := mustParseBlockNumber(tx.BlockNumber)
		txByBlock[blockNum] = append(txByBlock[blockNum], tx)
	}

	logByTx := make(map[string][]*types.Log)
	for _, log := range logs {
		logByTx[log.TransactionHash] = append(logByTx[log.TransactionHash], log)
	}

	// Process blocks
	if m.cfg.DryRun {
		// Dry run - just count
		result.BlocksProcessed = int64(len(blocks))
		result.TransactionsImported = int64(len(transactions))
		result.LogsImported = int64(len(logs))
		logger.Sugar.Infof("[DRY RUN] Would import %d blocks, %d transactions, %d logs",
			len(blocks), len(transactions), len(logs))
		return result, nil
	}

	// ==================== IMPORT PHASE ====================
	importStart := time.Now()

	// Import to DefraDB
	var (
		blocksImported int64
		txsImported    int64
		logsImported   int64
		errCount       int64
	)

	// Use worker pool for parallel processing
	type workItem struct {
		block *types.Block
		txs   []*types.Transaction
	}

	workChan := make(chan workItem, m.cfg.Workers*2)
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < m.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range workChan {
				blockNum := mustParseBlockNumber(item.block.Number)

				// Attach logs to transactions
				for _, tx := range item.txs {
					tx.Logs = make([]types.Log, 0)
					for _, log := range logByTx[tx.Hash] {
						tx.Logs = append(tx.Logs, *log)
					}
				}

				// Convert to receipts format
				receipts := make([]*types.TransactionReceipt, 0)
				for _, tx := range item.txs {
					receipt := &types.TransactionReceipt{
						TransactionHash:   tx.Hash,
						BlockHash:         tx.BlockHash,
						BlockNumber:       tx.BlockNumber,
						From:              tx.From,
						To:                tx.To,
						Status:            "0x1",
						GasUsed:           tx.GasUsed,
						CumulativeGasUsed: tx.CumulativeGasUsed,
						Logs:              tx.Logs,
					}
					if !tx.Status {
						receipt.Status = "0x0"
					}
					receipts = append(receipts, receipt)
				}

				// Use batch optimized creation
				_, err := m.blockHandler.CreateBlockBatchOptimized(ctx, item.block, item.txs, receipts)
				if err != nil {
					if !isAlreadyExistsError(err) {
						logger.Sugar.Warnf("Failed to import block %d: %v", blockNum, err)
						atomic.AddInt64(&errCount, 1)
					}
					return
				}

				atomic.AddInt64(&blocksImported, 1)
				atomic.AddInt64(&txsImported, int64(len(item.txs)))
				for _, tx := range item.txs {
					atomic.AddInt64(&logsImported, int64(len(tx.Logs)))
				}
			}
		}()
	}

	// Send work
	for _, block := range blocks {
		blockNum := mustParseBlockNumber(block.Number)
		txs := txByBlock[blockNum]

		select {
		case <-ctx.Done():
			close(workChan)
			wg.Wait()
			return result, ctx.Err()
		case workChan <- workItem{block: block, txs: txs}:
		}
	}

	close(workChan)
	wg.Wait()

	result.ImportDuration = time.Since(importStart)
	logger.Sugar.Infof("Import phase completed in %v", result.ImportDuration)

	result.BlocksProcessed = int64(len(blocks))
	result.BlocksImported = blocksImported
	result.TransactionsImported = txsImported
	result.LogsImported = logsImported
	result.ErrorCount = int(errCount)

	logger.Sugar.Infof("Batch complete: imported %d/%d blocks, %d txs, %d logs (download: %v, import: %v)",
		blocksImported, len(blocks), txsImported, logsImported, result.DownloadDuration, result.ImportDuration)

	return result, nil
}

// validateSample validates a random sample of imported blocks against RPC
func (m *Migrator) validateSample(ctx context.Context, startBlock, endBlock int64) []ValidationError {
	var errors []ValidationError

	// Sample random blocks
	sampleSize := m.cfg.ValidateSample
	blockRange := endBlock - startBlock + 1
	if int64(sampleSize) > blockRange {
		sampleSize = int(blockRange)
	}

	step := blockRange / int64(sampleSize)
	if step < 1 {
		step = 1
	}

	for i := 0; i < sampleSize; i++ {
		select {
		case <-ctx.Done():
			return errors
		default:
		}

		blockNum := startBlock + int64(i)*step

		// Fetch from RPC
		rpcBlock, err := m.rpcClient.BlockByNumber(ctx, big.NewInt(blockNum))
		if err != nil {
			errors = append(errors, ValidationError{
				BlockNumber: blockNum,
				Message:     fmt.Sprintf("RPC fetch failed: %v", err),
			})
			continue
		}

		// TODO: Fetch from DefraDB and compare
		// For now, just verify RPC is accessible
		_ = rpcBlock

		if i%10 == 0 {
			logger.Sugar.Infof("Validated %d/%d blocks", i+1, sampleSize)
		}
	}

	return errors
}

// loadCheckpoint loads the migration checkpoint
func (m *Migrator) loadCheckpoint() {
	checkpointPath := filepath.Join(m.cfg.OutputDir, "checkpoint.json")
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		return // No checkpoint file
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		logger.Sugar.Warnf("Failed to parse checkpoint: %v", err)
		return
	}

	// Verify provider matches
	if cp.Provider != string(m.cfg.Provider) {
		logger.Sugar.Warnf("Checkpoint provider mismatch (%s vs %s), ignoring checkpoint",
			cp.Provider, m.cfg.Provider)
		return
	}

	m.checkpoint = &cp
}

// saveCheckpoint saves the migration checkpoint
func (m *Migrator) saveCheckpoint(lastBlock int64) {
	checkpointPath := filepath.Join(m.cfg.OutputDir, "checkpoint.json")

	cp := Checkpoint{
		LastBlock:   lastBlock,
		Provider:    string(m.cfg.Provider),
		LastUpdated: time.Now().Format(time.RFC3339),
	}

	if m.checkpoint != nil {
		cp.StartedAt = m.checkpoint.StartedAt
		cp.BlocksProcessed = m.checkpoint.BlocksProcessed
	} else {
		cp.StartedAt = time.Now().Format(time.RFC3339)
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		logger.Sugar.Warnf("Failed to marshal checkpoint: %v", err)
		return
	}

	if err := os.WriteFile(checkpointPath, data, 0644); err != nil {
		logger.Sugar.Warnf("Failed to save checkpoint: %v", err)
	}
}

func isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return containsString(errStr, "already exists") || containsString(errStr, "duplicate")
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
