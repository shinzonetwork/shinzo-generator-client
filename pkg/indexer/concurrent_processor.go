package indexer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

const (
	// BlockNotFoundRetryDelay is the delay before retrying when a block is not yet available on chains.
	BlockNotFoundRetryDelay = 3 * time.Second

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

// ConcurrentBlockProcessor processes multiple blocks concurrently.
type ConcurrentBlockProcessor struct {
	chain           chains.Chain
	workers         int
	blocksPerMinute int
	resultChan      chan *BlockResult
	pendingMu       sync.Mutex
	pending         map[int64]*BlockResult
	nextToCommit    int64
}

// NewConcurrentBlockProcessor creates a new concurrent processor.
func NewConcurrentBlockProcessor(
	chain chains.Chain,
	workers int,
	blocksPerMinute int,
) *ConcurrentBlockProcessor {
	return &ConcurrentBlockProcessor{
		chain:           chain,
		workers:         workers,
		blocksPerMinute: blocksPerMinute,
		resultChan:      make(chan *BlockResult, workers*DefaultWorkersAhead),
		pending:         make(map[int64]*BlockResult),
	}
}

// ProcessBlocks dispatches blocks to workers and commits results in order.
func (p *ConcurrentBlockProcessor) ProcessBlocks(
	ctx context.Context,
	startBlock int64,
	onBlockProcessed func(blockNum int64),
) error {
	p.nextToCommit = startBlock

	workChan, wg, collectWg := p.startWorkers(ctx, onBlockProcessed)

	shutdown := func() {
		close(workChan)
		wg.Wait()
		close(p.resultChan)
		collectWg.Wait()
	}

	return p.dispatchLoop(ctx, startBlock, workChan, shutdown)
}

// startWorkers launches processing and result-collection goroutines.
func (p *ConcurrentBlockProcessor) startWorkers(ctx context.Context, onBlockProcessed func(blockNum int64)) (chan int64, *sync.WaitGroup, *sync.WaitGroup) {
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

	return workChan, &wg, &collectWg
}

// collectResults reads from resultChan and commits blocks in order.
func (p *ConcurrentBlockProcessor) collectResults(onBlockProcessed func(blockNum int64)) {
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
				if next.BlockID != "" {
					logger.Sugar.Infof("Committed block %d (ID: %s)", next.BlockNum, next.BlockID)
				} else {
					logger.Sugar.Infof("Committed block %d", next.BlockNum)
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

		p.pendingMu.Lock()
		tooFarAhead := nextBlock-p.nextToCommit >= int64(p.workers*DefaultWorkersAhead)
		p.pendingMu.Unlock()

		if tooFarAhead {
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
			lastDispatch = time.Now()
			nextBlock++
		}
	}
}

// fetchAndProcessBlock calls chain.FetchAndStoreBlock. The adapter retries
// non-not-found RPC errors internally (up to maxRPCRetries with linear
// backoff); the processor only handles not-found with infinite retry
// (block may not be mined yet). This avoids double-retry (previously
// 3×3=9 attempts).
func (p *ConcurrentBlockProcessor) fetchAndProcessBlock(ctx context.Context, blockNum int64) *BlockResult {
	result := &BlockResult{BlockNum: blockNum}

	for {
		if ctx.Err() != nil {
			result.Error = ctx.Err()
			return result
		}

		blockID, err := p.chain.FetchAndStoreBlock(ctx, blockNum)
		if err == nil {
			result.BlockID = blockID
			result.Success = true
			return result
		}

		if errors.IsErrNotFound(err) {
			logger.Sugar.Infof("Block %d not available yet, waiting...", blockNum)
			select {
			case <-ctx.Done():
				result.Error = ctx.Err()
				return result
			case <-time.After(BlockNotFoundRetryDelay):
			}
			continue
		}

		result.Error = fmt.Errorf("failed to fetch block %d: %w", blockNum, err)
		return result
	}
}
