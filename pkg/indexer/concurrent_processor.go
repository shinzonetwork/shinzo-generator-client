package indexer

import (
	"context"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chain"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

// signingJob holds the data needed to sign an existing block in the background
type signingJob struct {
	blockNum     int64
	blockHash    string
	block        *types.Block
	transactions []*types.Transaction
	receipts     []*types.TransactionReceipt
}

// ConcurrentBlockProcessor processes multiple blocks concurrently
type ConcurrentBlockProcessor struct {
	chain           chain.Chain
	workers         int
	receiptWorkers  int
	blocksPerMinute int
	resultChan      chan *chain.BlockResult
	pendingMu       sync.Mutex
	pending         map[int64]*chain.BlockResult
	nextToCommit    int64
	signingChan     chan signingJob
}

// NewConcurrentBlockProcessor creates a new concurrent processor
func NewConcurrentBlockProcessor(
	chainAdapter chain.Chain,
	workers int,
	receiptWorkers int,
	blocksPerMinute int,
) *ConcurrentBlockProcessor {
	return &ConcurrentBlockProcessor{
		chain:           chainAdapter,
		workers:         workers,
		receiptWorkers:  receiptWorkers,
		blocksPerMinute: blocksPerMinute,
		resultChan:      make(chan *chain.BlockResult, workers*2),
		pending:         make(map[int64]*chain.BlockResult),
		signingChan:     make(chan signingJob, 64),
	}
}

// ProcessBlocks processes blocks concurrently while maintaining commit order.
func (p *ConcurrentBlockProcessor) ProcessBlocks(
	ctx context.Context,
	startBlock int64,
	onBlockProcessed func(blockNum int64),
) error {
	p.nextToCommit = startBlock

	// Start background signing worker for existing blocks
	/*
		var signingWg sync.WaitGroup
		signingWg.Go(func() {
			for job := range p.signingChan {
				if ctx.Err() != nil {
					continue // drain channel
				}
				// TODO(tzdybal): signing
				//if _, err := p.blockHandler.CreateBlockSignatureForExistingBlock(
				//	ctx, job.blockNum, job.blockHash, job.block, job.transactions, job.receipts,
				//); err != nil {
				//	logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", job.blockNum, err)
				//}
			}
		})
	*/

	var wg sync.WaitGroup
	workChan := make(chan int64, p.workers*2)

	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for blockNum := range workChan {
				result := p.chain.FetchAndStoreBlock(ctx, blockNum)
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

	shutdown := func() {
		close(workChan)
		wg.Wait()
		close(p.resultChan)
		collectWg.Wait()
		close(p.signingChan)
		// TODO(tzdybal): signing
		//signingWg.Wait()
	}

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
					shutdown()
					return ctx.Err()
				case <-time.After(minInterval - elapsed):
				}
			}
		}

		// Don't dispatch too far ahead of what's been committed
		p.pendingMu.Lock()
		maxAhead := int64(p.workers * 2)
		tooFarAhead := nextBlock-p.nextToCommit >= maxAhead
		p.pendingMu.Unlock()

		if tooFarAhead {
			select {
			case <-ctx.Done():
				shutdown()
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
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
