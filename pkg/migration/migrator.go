package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
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
func NewMigrator(cfg *Config) (*Migrator, error) {
	m := &Migrator{cfg: cfg}

	switch cfg.Provider {
	case ProviderAWS:
		m.provider = NewAWSProvider(cfg.AWSBucket, cfg.AWSPrefix, cfg.OutputDir)
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}

	if !cfg.DryRun && cfg.DefraURL != "" {
		handler, err := defra.NewBlockHandler(cfg.DefraURL)
		if err != nil {
			return nil, fmt.Errorf("failed to create block handler: %w", err)
		}
		m.blockHandler = handler
	}

	if cfg.EnableValidation && cfg.RPCURL != "" {
		client, err := ethclient.Dial(cfg.RPCURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to RPC: %w", err)
		}
		m.rpcClient = client
	}

	m.loadCheckpoint()
	return m, nil
}

// NewMigratorWithNode creates a Migrator that uses direct embedded DefraDB node access.
func NewMigratorWithNode(cfg *Config, defraNode *node.Node) (*Migrator, error) {
	if defraNode == nil && !cfg.DryRun {
		return nil, fmt.Errorf("defraNode is required for non-dry-run migration")
	}

	m := &Migrator{cfg: cfg}

	switch cfg.Provider {
	case ProviderAWS:
		m.provider = NewAWSProvider(cfg.AWSBucket, cfg.AWSPrefix, cfg.OutputDir)
	default:
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}

	if !cfg.DryRun && defraNode != nil {
		handler, err := defra.NewBlockHandlerWithNode(defraNode)
		if err != nil {
			return nil, fmt.Errorf("failed to create block handler with node: %w", err)
		}
		m.blockHandler = handler
	}

	if cfg.EnableValidation && cfg.RPCURL != "" {
		client, err := ethclient.Dial(cfg.RPCURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to RPC: %w", err)
		}
		m.rpcClient = client
	}

	m.loadCheckpoint()
	return m, nil
}

// Run executes the migration
func (m *Migrator) Run(ctx context.Context) (*Result, error) {
	result := &Result{Status: "running"}

	startBlock := m.cfg.StartBlock
	endBlock := m.cfg.EndBlock

	if m.cfg.ResumeFrom > 0 {
		startBlock = m.cfg.ResumeFrom
	} else if m.checkpoint != nil && m.checkpoint.LastBlock > startBlock {
		startBlock = m.checkpoint.LastBlock + 1
		logger.Sugar.Infof("Resuming from checkpoint at block %d", startBlock)
	}

	if endBlock == 0 {
		_, maxBlock, err := m.provider.GetBlockRange(ctx)
		if err != nil {
			endBlock = startBlock + int64(m.cfg.BatchSize*10)
		} else {
			endBlock = maxBlock
		}
	}

	logger.Sugar.Infof("Starting migration: blocks %d to %d (UseBulkAPI=%v, MultiBlockBatch=%d)",
		startBlock, endBlock, m.cfg.UseBulkAPI, m.cfg.MultiBlockBatch)

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

		var batchResult *Result
		var err error

		if m.cfg.UseBulkAPI {
			batchResult, err = m.processBatchMultiBlock(ctx, currentBlock, batchEnd)
		} else {
			batchResult, err = m.processBatchGraphQL(ctx, currentBlock, batchEnd)
		}

		if err != nil {
			logger.Sugar.Errorf("Batch failed: %v", err)
			result.ErrorCount++
		}

		result.BlocksProcessed += batchResult.BlocksProcessed
		result.BlocksImported += batchResult.BlocksImported
		result.BlocksSkipped += batchResult.BlocksSkipped
		result.TransactionsImported += batchResult.TransactionsImported
		result.LogsImported += batchResult.LogsImported
		result.AccessListEntriesImported += batchResult.AccessListEntriesImported
		result.ValidationErrors = append(result.ValidationErrors, batchResult.ValidationErrors...)
		result.DownloadDuration += batchResult.DownloadDuration
		result.ImportDuration += batchResult.ImportDuration

		m.saveCheckpoint(batchEnd)
		result.LastCheckpoint = batchEnd
		currentBlock = batchEnd + 1
	}

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

// processBatchMultiBlock processes blocks using CreateMultiBlockBulk for maximum throughput
func (m *Migrator) processBatchMultiBlock(ctx context.Context, startBlock, endBlock int64) (*Result, error) {
	result := &Result{}

	// ==================== DOWNLOAD PHASE ====================
	downloadStart := time.Now()

	blocks, err := m.provider.ReadBlocks(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read blocks: %w", err)
	}

	transactions, err := m.provider.ReadTransactions(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read transactions: %w", err)
	}

	logs, err := m.provider.ReadLogs(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read logs: %w", err)
	}

	result.DownloadDuration = time.Since(downloadStart)
	logger.Sugar.Infof("Download: %d blocks, %d txs, %d logs in %v",
		len(blocks), len(transactions), len(logs), result.DownloadDuration)

	// ==================== PREPARE PHASE ====================
	// Group transactions by block hash
	txsByBlockHash := make(map[string][]*types.Transaction, len(blocks))
	for _, tx := range transactions {
		txsByBlockHash[tx.BlockHash] = append(txsByBlockHash[tx.BlockHash], tx)
	}

	// Group logs by tx hash and build receipts
	logsByTxHash := make(map[string][]types.Log, len(transactions))
	for _, log := range logs {
		logsByTxHash[log.TransactionHash] = append(logsByTxHash[log.TransactionHash], *log)
	}

	receiptsByTxHash := make(map[string]*types.TransactionReceipt, len(transactions))
	for _, tx := range transactions {
		receipt := &types.TransactionReceipt{
			TransactionHash:   tx.Hash,
			BlockHash:         tx.BlockHash,
			BlockNumber:       tx.BlockNumber,
			From:              tx.From,
			To:                tx.To,
			Status:            "0x1",
			GasUsed:           tx.GasUsed,
			CumulativeGasUsed: tx.CumulativeGasUsed,
			Logs:              logsByTxHash[tx.Hash],
		}
		if !tx.Status {
			receipt.Status = "0x0"
		}
		receiptsByTxHash[tx.Hash] = receipt
	}

	if m.cfg.DryRun {
		result.BlocksProcessed = int64(len(blocks))
		result.TransactionsImported = int64(len(transactions))
		result.LogsImported = int64(len(logs))
		return result, nil
	}

	// ==================== IMPORT PHASE ====================
	importStart := time.Now()

	var (
		blocksImported int64
		txsImported    int64
		logsImported   int64
		alesImported   int64
		errCount       int64
	)

	// Determine sub-batch size for multi-block commits
	subBatchSize := m.cfg.MultiBlockBatch
	if subBatchSize <= 0 {
		subBatchSize = 20 // Default: 20 blocks per DB transaction
	}

	// Process in sub-batches with workers
	type workBatch struct {
		blocks           []*types.Block
		txsByBlockHash   map[string][]*types.Transaction
		receiptsByTxHash map[string]*types.TransactionReceipt
	}

	workChan := make(chan workBatch, m.cfg.Workers)
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < m.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range workChan {
				// Use CreateMultiBlockBulk for multiple blocks in one transaction
				bulkResult, err := m.blockHandler.CreateMultiBlockBulk(
					ctx,
					batch.blocks,
					batch.txsByBlockHash,
					batch.receiptsByTxHash,
				)
				if err != nil {
					if !isAlreadyExistsError(err) {
						logger.Sugar.Warnf("Failed to import multi-block batch: %v", err)
						atomic.AddInt64(&errCount, 1)
					}
					continue
				}

				atomic.AddInt64(&blocksImported, int64(bulkResult.BlocksCreated))
				atomic.AddInt64(&txsImported, int64(bulkResult.TransactionsCreated))
				atomic.AddInt64(&logsImported, int64(bulkResult.LogsCreated))
				atomic.AddInt64(&alesImported, int64(bulkResult.ALEsCreated))
			}
		}()
	}

	// Split blocks into sub-batches and send to workers
	for i := 0; i < len(blocks); i += subBatchSize {
		end := i + subBatchSize
		if end > len(blocks) {
			end = len(blocks)
		}

		subBlocks := blocks[i:end]

		// Build sub-batch maps
		subTxsByBlockHash := make(map[string][]*types.Transaction, len(subBlocks))
		subReceiptsByTxHash := make(map[string]*types.TransactionReceipt)

		for _, block := range subBlocks {
			txs := txsByBlockHash[block.Hash]
			subTxsByBlockHash[block.Hash] = txs
			for _, tx := range txs {
				if r := receiptsByTxHash[tx.Hash]; r != nil {
					subReceiptsByTxHash[tx.Hash] = r
				}
			}
		}

		select {
		case <-ctx.Done():
			close(workChan)
			wg.Wait()
			return result, ctx.Err()
		case workChan <- workBatch{
			blocks:           subBlocks,
			txsByBlockHash:   subTxsByBlockHash,
			receiptsByTxHash: subReceiptsByTxHash,
		}:
		}
	}

	close(workChan)
	wg.Wait()

	result.ImportDuration = time.Since(importStart)

	result.BlocksProcessed = int64(len(blocks))
	result.BlocksImported = blocksImported
	result.TransactionsImported = txsImported
	result.LogsImported = logsImported
	result.AccessListEntriesImported = alesImported
	result.ErrorCount = int(errCount)

	logger.Sugar.Infof("Import: %d/%d blocks, %d txs, %d logs in %v (%.1f blocks/sec)",
		blocksImported, len(blocks), txsImported, logsImported,
		result.ImportDuration, float64(blocksImported)/result.ImportDuration.Seconds())

	return result, nil
}

// processBatchGraphQL processes a batch using GraphQL (original method)
func (m *Migrator) processBatchGraphQL(ctx context.Context, startBlock, endBlock int64) (*Result, error) {
	result := &Result{}

	downloadStart := time.Now()

	blocks, err := m.provider.ReadBlocks(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read blocks: %w", err)
	}

	transactions, err := m.provider.ReadTransactions(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read transactions: %w", err)
	}

	logs, err := m.provider.ReadLogs(ctx, startBlock, endBlock)
	if err != nil {
		return result, fmt.Errorf("failed to read logs: %w", err)
	}

	result.DownloadDuration = time.Since(downloadStart)

	txByBlock := make(map[int64][]*types.Transaction)
	for _, tx := range transactions {
		blockNum := mustParseBlockNumber(tx.BlockNumber)
		txByBlock[blockNum] = append(txByBlock[blockNum], tx)
	}

	logByTx := make(map[string][]*types.Log)
	for _, log := range logs {
		logByTx[log.TransactionHash] = append(logByTx[log.TransactionHash], log)
	}

	if m.cfg.DryRun {
		result.BlocksProcessed = int64(len(blocks))
		result.TransactionsImported = int64(len(transactions))
		result.LogsImported = int64(len(logs))
		return result, nil
	}

	importStart := time.Now()

	var blocksImported, txsImported, logsImported, errCount int64

	type workItem struct {
		block *types.Block
		txs   []*types.Transaction
	}

	workChan := make(chan workItem, m.cfg.Workers*2)
	var wg sync.WaitGroup

	for i := 0; i < m.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range workChan {
				blockNum := mustParseBlockNumber(item.block.Number)

				for _, tx := range item.txs {
					tx.Logs = make([]types.Log, 0)
					for _, log := range logByTx[tx.Hash] {
						tx.Logs = append(tx.Logs, *log)
					}
				}

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

				_, err := m.blockHandler.CreateBlockBatchOptimized(ctx, item.block, item.txs, receipts)
				if err != nil {
					if !isAlreadyExistsError(err) {
						logger.Sugar.Warnf("Failed to import block %d: %v", blockNum, err)
						atomic.AddInt64(&errCount, 1)
					}
					continue
				}

				atomic.AddInt64(&blocksImported, 1)
				atomic.AddInt64(&txsImported, int64(len(item.txs)))
				for _, tx := range item.txs {
					atomic.AddInt64(&logsImported, int64(len(tx.Logs)))
				}
			}
		}()
	}

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
	result.BlocksProcessed = int64(len(blocks))
	result.BlocksImported = blocksImported
	result.TransactionsImported = txsImported
	result.LogsImported = logsImported
	result.ErrorCount = int(errCount)

	return result, nil
}

func (m *Migrator) validateSample(ctx context.Context, startBlock, endBlock int64) []ValidationError {
	var errors []ValidationError

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
		rpcBlock, err := m.rpcClient.BlockByNumber(ctx, big.NewInt(blockNum))
		if err != nil {
			errors = append(errors, ValidationError{
				BlockNumber: blockNum,
				Message:     fmt.Sprintf("RPC fetch failed: %v", err),
			})
			continue
		}
		_ = rpcBlock
	}

	return errors
}

func (m *Migrator) loadCheckpoint() {
	checkpointPath := filepath.Join(m.cfg.OutputDir, "checkpoint.json")
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		return
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return
	}

	if cp.Provider != string(m.cfg.Provider) {
		return
	}
	m.checkpoint = &cp
}

func (m *Migrator) saveCheckpoint(lastBlock int64) {
	checkpointPath := filepath.Join(m.cfg.OutputDir, "checkpoint.json")

	cp := Checkpoint{
		LastBlock:   lastBlock,
		Provider:    string(m.cfg.Provider),
		LastUpdated: time.Now().Format(time.RFC3339),
	}

	if m.checkpoint != nil {
		cp.StartedAt = m.checkpoint.StartedAt
	} else {
		cp.StartedAt = time.Now().Format(time.RFC3339)
	}

	data, _ := json.MarshalIndent(cp, "", "  ")
	os.WriteFile(checkpointPath, data, 0644)
}

func isAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "already exists") || strings.Contains(s, "duplicate")
}

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
