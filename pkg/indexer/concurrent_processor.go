package indexer

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

// BlockResult holds the result of processing a block
type BlockResult struct {
	BlockNum int64
	BlockID  string
	Success  bool
	Error    error
}

// ConcurrentBlockProcessor processes multiple blocks concurrently
type ConcurrentBlockProcessor struct {
	blockHandler    *defra.BlockHandler
	ethClient       *rpc.EthereumClient
	workers         int
	receiptWorkers  int
	blocksPerMinute int
	resultChan      chan *BlockResult
	pendingMu       sync.Mutex
	pending         map[int64]*BlockResult
	nextToCommit    int64
}

// NewConcurrentBlockProcessor creates a new concurrent processor
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
		resultChan:      make(chan *BlockResult, workers*2),
		pending:         make(map[int64]*BlockResult),
	}
}

// ProcessBlocks processes blocks concurrently while maintaining commit order.
func (p *ConcurrentBlockProcessor) ProcessBlocks(
	ctx context.Context,
	startBlock int64,
	onBlockProcessed func(blockNum int64),
) error {
	p.nextToCommit = startBlock

	var wg sync.WaitGroup
	workChan := make(chan int64, p.workers*2)

	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for blockNum := range workChan {
				result := p.fetchAndProcessBlock(ctx, blockNum)
				select {
				case p.resultChan <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var collectWg sync.WaitGroup
	collectWg.Go(func() {
		for result := range p.resultChan {
			p.pendingMu.Lock()
			p.pending[result.BlockNum] = result

			for {
				next, ok := p.pending[p.nextToCommit]
				if !ok {
					break
				}
				delete(p.pending, p.nextToCommit)

				if next.Success {
					if next.BlockID != "existing" {
						logger.Sugar.Infof("Committed block %d (ID: %s)", next.BlockNum, next.BlockID)
					} else {
						logger.Sugar.Infof("Block %d already existed, skipping", next.BlockNum)
					}
					if onBlockProcessed != nil {
						onBlockProcessed(next.BlockNum)
					}
				} else {
					logger.Sugar.Warnf("Block %d failed: %v", next.BlockNum, next.Error)
				}
				p.nextToCommit++
			}
			p.pendingMu.Unlock()
		}
	})

	nextBlock := startBlock

	var minInterval time.Duration
	if p.blocksPerMinute > 0 {
		minInterval = time.Minute / time.Duration(p.blocksPerMinute)
		logger.Sugar.Infof("Rate limiting enabled: %d blocks/minute (interval: %v)", p.blocksPerMinute, minInterval)
	}

	lastDispatch := time.Now().Add(-minInterval)

	for {
		if minInterval > 0 {
			elapsed := time.Since(lastDispatch)
			if elapsed < minInterval {
				select {
				case <-ctx.Done():
					close(workChan)
					wg.Wait()
					close(p.resultChan)
					collectWg.Wait()
					return ctx.Err()
				case <-time.After(minInterval - elapsed):
				}
			}
		}

		select {
		case <-ctx.Done():
			close(workChan)
			wg.Wait()
			close(p.resultChan)
			collectWg.Wait()
			return ctx.Err()
		case workChan <- nextBlock:
			lastDispatch = time.Now()
			nextBlock++
		}
	}
}

// fetchAndProcessBlock fetches a block with receipts and processes it
func (p *ConcurrentBlockProcessor) fetchAndProcessBlock(ctx context.Context, blockNum int64) *BlockResult {
	result := &BlockResult{BlockNum: blockNum}

	var block *types.Block
	var err error
	for attempt := range 3 {
		if ctx.Err() != nil {
			result.Error = ctx.Err()
			return result
		}

		block, err = p.ethClient.GetBlockByNumber(ctx, big.NewInt(blockNum))
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			logger.Sugar.Infof("Block %d not available yet, waiting...", blockNum)
			select {
			case <-ctx.Done():
				result.Error = ctx.Err()
				return result
			case <-time.After(3 * time.Second):
			}
			continue
		}
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	if err != nil {
		result.Error = fmt.Errorf("failed to fetch block: %w", err)
		return result
	}

	transactions := make([]*types.Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}

	var validReceipts []*types.TransactionReceipt
	batchReceipts, batchErr := p.ethClient.GetBlockReceipts(ctx, big.NewInt(blockNum))
	if batchErr == nil {
		validReceipts = batchReceipts
	} else {
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

		validReceipts = make([]*types.TransactionReceipt, 0, len(receipts))
		for _, r := range receipts {
			if r != nil {
				validReceipts = append(validReceipts, r)
			}
		}
	}

	const maxRetries = 3
	for attempt := range maxRetries {
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

		if strings.Contains(err.Error(), "already exists") {
			result.Success = true
			result.BlockID = "existing"
			return result
		}

		if strings.Contains(err.Error(), "transaction conflict") {
			if attempt < maxRetries-1 {
				logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, maxRetries)
				select {
				case <-ctx.Done():
					result.Error = ctx.Err()
					return result
				case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
				}
				continue
			}
		}

		result.Error = fmt.Errorf("failed to create block batch: %w", err)
		return result
	}

	return result
}
