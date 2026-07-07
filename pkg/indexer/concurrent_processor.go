package indexer

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

const (
	// BlockNotFoundRetryDelay is the delay before retrying when a block is not yet available on chain.
	BlockNotFoundRetryDelay = 3 * time.Second

	// RPCErrorRetryBaseDelay is the base delay for retrying RPC errors (multiplied by attempt number).
	RPCErrorRetryBaseDelay = 500 * time.Millisecond

	// MaxRPCRetries is the maximum number of retries for non-"not found" RPC errors.
	MaxRPCRetries = 3

	// TransactionConflictRetryBaseDelay is the base delay for retrying transaction conflicts.
	TransactionConflictRetryBaseDelay = 50 * time.Millisecond

	// SigningQueueSize is the buffer size for the background block signing channel.
	SigningQueueSize = 64

	// DispatchThrottleDelay is the delay when the processor is too far ahead of committed blocks.
	DispatchThrottleDelay = 100 * time.Millisecond
)

// BlockResult holds the result of processing a block.
type BlockResult struct {
	BlockNum int64
	BlockID  string
	Success  bool
	Error    error
}

// signingJob holds the data needed to sign an existing block in the background.
type signingJob struct {
	blockNum     int64
	blockHash    string
	block        *types.Block
	transactions []*types.Transaction
	receipts     []*types.TransactionReceipt
}

// ConcurrentBlockProcessor processes multiple blocks concurrently.
type ConcurrentBlockProcessor struct {
	blockHandler    *defra.BlockHandler
	ethClient       *rpc.EthereumClient
	workers         int
	receiptWorkers  int
	blocksPerMinute int
	resultChan      chan *BlockResult
	inFlight        atomic.Int64 // blocks dispatched but not yet committed
	signingChan     chan signingJob
}

// NewConcurrentBlockProcessor creates a new concurrent processor.
func NewConcurrentBlockProcessor(
	blockHandler *defra.BlockHandler,
	ethClient *rpc.EthereumClient,
	workers int,
	receiptWorkers int,
	blocksPerMinute int,
) *ConcurrentBlockProcessor {
	return &ConcurrentBlockProcessor{
		blockHandler:    blockHandler,
		ethClient:       ethClient,
		workers:         workers,
		receiptWorkers:  receiptWorkers,
		blocksPerMinute: blocksPerMinute,
		resultChan:      make(chan *BlockResult, workers*DefaultWorkersAhead),
		signingChan:     make(chan signingJob, SigningQueueSize),
	}
}

// ProcessBlocks dispatches blocks to workers and commits results as they complete.
func (p *ConcurrentBlockProcessor) ProcessBlocks(
	ctx context.Context,
	startBlock int64,
	onBlockProcessed func(blockNum int64),
) error {
	workChan, wg, collectWg, signingWg := p.startWorkers(ctx, onBlockProcessed)

	shutdown := func() {
		close(workChan)
		wg.Wait()
		close(p.resultChan)
		collectWg.Wait()
		close(p.signingChan)
		signingWg.Wait()
	}

	return p.dispatchLoop(ctx, startBlock, workChan, shutdown)
}

// startWorkers launches signing, processing, and result-collection goroutines.
func (p *ConcurrentBlockProcessor) startWorkers(ctx context.Context, onBlockProcessed func(blockNum int64)) (chan int64, *sync.WaitGroup, *sync.WaitGroup, *sync.WaitGroup) {
	var signingWg sync.WaitGroup
	signingWg.Go(func() {
		for job := range p.signingChan {
			if ctx.Err() != nil {
				continue
			}
			if _, err := p.blockHandler.CreateBlockSignatureForExistingBlock(
				ctx, job.blockNum, job.blockHash, job.block, job.transactions, job.receipts,
			); err != nil {
				logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", job.blockNum, err)
			}
		}
	})

	workChan := make(chan int64, p.workers*DefaultWorkersAhead)

	var wg sync.WaitGroup
	for range p.workers {
		wg.Go(func() {
			for blockNum := range workChan {
				result := p.fetchAndProcessBlock(ctx, blockNum)
				select {
				case p.resultChan <- result:
				case <-ctx.Done():
					return
				}
			}
		})
	}

	var collectWg sync.WaitGroup
	collectWg.Go(func() {
		p.collectResults(onBlockProcessed)
	})

	return workChan, &wg, &collectWg, &signingWg
}

// collectResults commits blocks as they complete without requiring sequential ordering.
func (p *ConcurrentBlockProcessor) collectResults(onBlockProcessed func(blockNum int64)) {
	for result := range p.resultChan {
		if result.Success {
			if result.BlockID != "existing" {
				logger.Sugar.Infof("Committed block %d (ID: %s)", result.BlockNum, result.BlockID)
			} else {
				logger.Sugar.Infof("Block %d already existed, skipping", result.BlockNum)
			}
			if onBlockProcessed != nil {
				onBlockProcessed(result.BlockNum)
			}
		} else {
			logger.Sugar.Warnf("Block %d failed: %v", result.BlockNum, result.Error)
		}
		p.inFlight.Add(-1)
	}
}

// dispatchLoop sends block numbers to workChan with optional rate limiting.
func (p *ConcurrentBlockProcessor) dispatchLoop(ctx context.Context, startBlock int64, workChan chan int64, shutdown func()) error {
	var minInterval time.Duration
	if p.blocksPerMinute > 0 {
		minInterval = time.Minute / time.Duration(p.blocksPerMinute)
		logger.Sugar.Infof("Rate limiting enabled: %d blocks/minute (interval: %v)", p.blocksPerMinute, minInterval)
	}

	lastDispatch := time.Now().Add(-minInterval)
	nextBlock := startBlock

	for {
		if minInterval > 0 {
			elapsed := time.Since(lastDispatch)
			if elapsed < minInterval {
				select {
				case <-ctx.Done():
					shutdown()
					return ctx.Err()
				case <-time.After(minInterval - elapsed):
				}
			}
		}

		if p.inFlight.Load() >= int64(p.workers*DefaultWorkersAhead) {
			select {
			case <-ctx.Done():
				shutdown()
				return ctx.Err()
			case <-time.After(DispatchThrottleDelay):
				continue
			}
		}

		select {
		case <-ctx.Done():
			shutdown()
			return ctx.Err()
		case workChan <- nextBlock:
			p.inFlight.Add(1)
			lastDispatch = time.Now()
			nextBlock++
		}
	}
}

// fetchAndProcessBlock fetches a block and processes it into DefraDB.
func (p *ConcurrentBlockProcessor) fetchAndProcessBlock(ctx context.Context, blockNum int64) *BlockResult {
	result := &BlockResult{BlockNum: blockNum}

	block, err := p.fetchBlockWithRetry(ctx, blockNum)
	if err != nil {
		result.Error = err
		return result
	}

	transactions, validReceipts := p.fetchTransactionsAndReceipts(ctx, block, blockNum)

	return p.createBlockBatchWithRetry(ctx, block, blockNum, transactions, validReceipts, result)
}

// fetchBlockWithRetry fetches a block by number, retrying on not-found and RPC errors.
func (p *ConcurrentBlockProcessor) fetchBlockWithRetry(ctx context.Context, blockNum int64) (*types.Block, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		block, err := p.ethClient.GetBlockByNumber(ctx, big.NewInt(blockNum))
		if err == nil {
			return block, nil
		}

		if errors.IsErrNotFound(err) {
			logger.Sugar.Infof("Block %d not available yet, waiting...", blockNum)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(BlockNotFoundRetryDelay):
			}
			continue
		}

		otherErrors++
		if otherErrors >= MaxRPCRetries {
			return nil, fmt.Errorf("failed to fetch block: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(otherErrors) * RPCErrorRetryBaseDelay):
		}
	}
}

// fetchTransactionsAndReceipts fetches receipts for a block, falling back to individual fetches.
func (p *ConcurrentBlockProcessor) fetchTransactionsAndReceipts(ctx context.Context, block *types.Block, blockNum int64) ([]*types.Transaction, []*types.TransactionReceipt) {
	transactions := make([]*types.Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}

	batchReceipts, batchErr := p.ethClient.GetBlockReceipts(ctx, big.NewInt(blockNum))
	if batchErr == nil {
		return transactions, batchReceipts
	}

	if ctx.Err() == nil {
		logger.Sugar.Debugf("Block %d: eth_getBlockReceipts not available, falling back to individual fetches: %v", blockNum, batchErr)
	}

	receipts := make([]*types.TransactionReceipt, len(block.Transactions))
	var wg sync.WaitGroup
	sem := make(chan struct{}, p.receiptWorkers)

	for i, tx := range block.Transactions {
		wg.Add(1)
		go func(idx int, txHash string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			receipt, err := p.ethClient.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				if ctx.Err() == nil {
					logger.Sugar.Warnf("Failed to fetch receipt for tx %s: %v", txHash, err)
				}
				return
			}
			receipts[idx] = receipt
		}(i, tx.Hash)
	}
	wg.Wait()

	validReceipts := make([]*types.TransactionReceipt, 0, len(receipts))
	for _, r := range receipts {
		if r != nil {
			validReceipts = append(validReceipts, r)
		}
	}

	return transactions, validReceipts
}

// createBlockBatchWithRetry attempts to write the block batch to DefraDB with retries.
func (p *ConcurrentBlockProcessor) createBlockBatchWithRetry(ctx context.Context, block *types.Block, blockNum int64, transactions []*types.Transaction, validReceipts []*types.TransactionReceipt, result *BlockResult) *BlockResult {
	for attempt := range MaxRPCRetries {
		if ctx.Err() != nil {
			result.Error = ctx.Err()
			return result
		}

		blockID, err := p.blockHandler.CreateBlockBatch(ctx, block, transactions, validReceipts)
		if err == nil {
			result.Success = true
			result.BlockID = blockID
			return result
		}

		if errors.IsErrAlreadyExists(err) {
			select {
			case p.signingChan <- signingJob{
				blockNum:     blockNum,
				blockHash:    block.Hash,
				block:        block,
				transactions: transactions,
				receipts:     validReceipts,
			}:
			default:
				logger.Sugar.Warnf("Block %d: signing queue full, skipping block signature", blockNum)
			}
			result.Success = true
			result.BlockID = "existing"
			return result
		}

		if errors.IsErrTransactionConflict(err) && attempt < MaxRPCRetries-1 {
			logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, MaxRPCRetries)
			select {
			case <-ctx.Done():
				result.Error = ctx.Err()
				return result
			case <-time.After(time.Duration(attempt+1) * TransactionConflictRetryBaseDelay):
			}
			continue
		}

		result.Error = fmt.Errorf("failed to create block batch: %w", err)
		return result
	}

	return result
}
