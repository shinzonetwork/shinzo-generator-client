package indexer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

const (
	// BlockNotFoundRetryDelay is the delay before retrying when a block is not yet available on chains.
	BlockNotFoundRetryDelay = 3 * time.Second

	// DispatchThrottleDelay is the delay when the processor is too far ahead of committed blocks.
	DispatchThrottleDelay = 100 * time.Millisecond

	// transactionConflictRetryBaseDelay is the base delay for retrying
	// transaction conflicts on store.
	transactionConflictRetryBaseDelay = 50 * time.Millisecond

	// MaxRPCRetries is the maximum number of retries for non-"not found" RPC
	// errors.
	MaxRPCRetries = 3

	// RPCErrorRetryBaseDelay is the base delay for retrying RPC errors.
	RPCErrorRetryBaseDelay = 500 * time.Millisecond
)

// BlockResult holds the result of processing a block.
type BlockResult struct {
	BlockNum int64
	BlockID  string
	Success  bool
	Error    error
}

// BlockStorer is the store-side interface used by the processor. The concrete
// *defra.BlockHandler satisfies it; the interface enables pure unit tests with
// a mock.
type BlockStorer interface {
	Store(ctx context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error)
	SignExisting(ctx context.Context, result chains.ConversionResult, blockHash string, blockNumber int64) (string, error)
}

// ConcurrentBlockProcessor processes multiple blocks concurrently.
type ConcurrentBlockProcessor struct {
	fetcher         chains.Fetcher
	converter       chains.Converter
	blockHandler    BlockStorer
	workers         int
	blocksPerMinute int
	resultChan      chan *BlockResult
	pendingMu       sync.Mutex
	pending         map[int64]*BlockResult
	nextToCommit    int64
}

// NewConcurrentBlockProcessor creates a new concurrent processor.
func NewConcurrentBlockProcessor(
	fetcher chains.Fetcher,
	converter chains.Converter,
	blockHandler BlockStorer,
	workers int,
	blocksPerMinute int,
) *ConcurrentBlockProcessor {
	return &ConcurrentBlockProcessor{
		fetcher:         fetcher,
		converter:       converter,
		blockHandler:    blockHandler,
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

// fetchAndProcessBlock fetches, converts, and stores a block with retry
// classification:
//   - fetch not-found: infinite retry with BlockNotFoundRetryDelay
//   - fetch other errors: up to MaxRPCRetries with linear backoff
//   - convert: no retry (pure computation)
//   - store: up to MaxRPCRetries on transaction conflicts; ErrAlreadyExists
//     triggers a fire-and-forget SignExisting goroutine
func (p *ConcurrentBlockProcessor) fetchAndProcessBlock(ctx context.Context, blockNum int64) *BlockResult {
	raw, err := p.fetchBlockWithRetry(ctx, blockNum)
	if err != nil {
		return &BlockResult{BlockNum: blockNum, Error: err}
	}

	result, err := p.converter.Convert(ctx, raw)
	if err != nil {
		return &BlockResult{BlockNum: blockNum, Error: fmt.Errorf("convert block: %w", err)}
	}

	return p.storeWithRetry(ctx, blockNum, result)
}

// fetchBlockWithRetry fetches a block from the fetcher with retry
// classification:
//   - not-found: infinite retry with BlockNotFoundRetryDelay (block may not be mined yet)
//   - other errors: up to MaxRPCRetries with linear backoff (RPCErrorRetryBaseDelay * attempt)
func (p *ConcurrentBlockProcessor) fetchBlockWithRetry(ctx context.Context, blockNum int64) (any, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		raw, err := p.fetcher.FetchBlock(ctx, blockNum)
		if err == nil {
			return raw, nil
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
			return nil, fmt.Errorf("failed to fetch block %d: %w", blockNum, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(otherErrors) * RPCErrorRetryBaseDelay):
		}
	}
}

// storeWithRetry persists a ConversionResult via the block handler. On
// ErrAlreadyExists it spawns a fire-and-forget SignExisting goroutine and
// returns success. Transaction conflicts are retried up to MaxRPCRetries
// times with transactionConflictRetryBaseDelay backoff.
func (p *ConcurrentBlockProcessor) storeWithRetry(ctx context.Context, blockNum int64, result chains.ConversionResult) *BlockResult {
	blockHash := extractBlockHash(result.Groups)

	for attempt := range MaxRPCRetries {
		if ctx.Err() != nil {
			return &BlockResult{BlockNum: blockNum, Error: ctx.Err()}
		}

		res, err := p.blockHandler.Store(ctx, result)
		if err == nil {
			return &BlockResult{BlockNum: blockNum, BlockID: res.BlockID, Success: true}
		}

		if errors.IsErrAlreadyExists(err) {
			go func() {
				if _, sErr := p.blockHandler.SignExisting(ctx, result, blockHash, blockNum); sErr != nil {
					logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", blockNum, sErr)
				}
			}()
			return &BlockResult{BlockNum: blockNum, Success: true}
		}

		if errors.IsErrTransactionConflict(err) && attempt < MaxRPCRetries-1 {
			logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, MaxRPCRetries)
			select {
			case <-ctx.Done():
				return &BlockResult{BlockNum: blockNum, Error: ctx.Err()}
			case <-time.After(time.Duration(attempt+1) * transactionConflictRetryBaseDelay):
			}
			continue
		}

		return &BlockResult{BlockNum: blockNum, Error: fmt.Errorf("failed to store block: %w", err)}
	}
	return &BlockResult{BlockNum: blockNum, Error: fmt.Errorf("failed to store block %d: exhausted retries", blockNum)}
}

// extractBlockHash finds the block group (the one with BlockHashField != "")
// and returns its block hash value. Returns "" if no block group is found.
func extractBlockHash(groups []chains.DocumentGroup) string {
	for _, g := range groups {
		if g.BlockHashField != "" && len(g.Docs) > 0 {
			if hash, ok := g.Docs[0][g.BlockHashField].(string); ok {
				return hash
			}
		}
	}
	return ""
}
