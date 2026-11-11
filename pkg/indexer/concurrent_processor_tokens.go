package indexer

import (
	"context"
	"fmt"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"

	stderrors "errors"
)

// DispatchFinalizedOnly bounds dispatch at the height the fetcher reports as finalized, so no
// block a reorg can still replace is indexed. Dispatch asks for that height every poll, or every
// FinalizedPollInterval when poll is 0. Call it before ProcessBlocks.
func (p *ConcurrentBlockProcessor) DispatchFinalizedOnly(poll time.Duration) {
	p.finalizedOnly = true
	p.finalizedPoll = poll
	if poll == 0 {
		p.finalizedPoll = FinalizedPollInterval
	}
}

// RetryFailedHeights makes a height whose fetch, convert or store failed retry until it succeeds,
// and stores nothing DefaultWorkersAhead*workers or more above it before it. A store refusal that
// retrying cannot clear still stops the run. Call it before ProcessBlocks.
func (p *ConcurrentBlockProcessor) RetryFailedHeights() {
	p.retryFailedHeights = true
}

// StopOnReorg makes a detected reorg stop the run with ErrReorgStopped instead of rolling the
// orphaned heights back, so nothing stored is deleted. Call it before ProcessBlocks.
func (p *ConcurrentBlockProcessor) StopOnReorg() {
	p.stopOnReorg = true
}

// stoppedForReorg stops the run on signal when the processor stops on reorgs, and reports whether
// it did.
func (p *ConcurrentBlockProcessor) stoppedForReorg(signal reorgSignal) bool {
	if !p.stopOnReorg {
		return false
	}
	p.failRun(fmt.Errorf("%w: stored block %d has hash %s, the chain has %s", ErrReorgStopped,
		signal.rollbackFrom, signal.orphanHash, signal.chainHash))
	return true
}

// retryInPlace calls attempt and, when failed heights are retried in place, again every
// FailedHeightRetryDelay until it succeeds, ctx is done, or a store is refused in a way retrying
// cannot clear. It returns the last result.
func (p *ConcurrentBlockProcessor) retryInPlace(ctx context.Context, attempt func() *BlockResult) *BlockResult {
	result := attempt()
	height, failures := result.BlockNum, int64(0)
	defer func() {
		if failures > 0 {
			p.recordRetry(ctx, height, 0)
		}
	}()
	for p.retryFailedHeights && result.Error != nil && !errors.IsErrDocumentDeleted(result.Error) &&
		!stderrors.Is(result.Error, defra.ErrStoredHeaderDiffers) && ctx.Err() == nil {
		failures++
		p.recordRetry(ctx, height, failures)
		logger.Sugar.Warnf("Block %d failed, retrying in %v: %v", result.BlockNum, FailedHeightRetryDelay, result.Error)
		select {
		case <-ctx.Done():
			return result
		case <-time.After(FailedHeightRetryDelay):
		}
		result = attempt()
	}
	return result
}

// recordRetry notes that height has failed failures times and is being retried in place, or, with
// failures 0, that it no longer is, and sets the retry gauges to the lowest such height and its
// failures, or to 0 when no height is being retried.
func (p *ConcurrentBlockProcessor) recordRetry(ctx context.Context, height, failures int64) {
	p.retryMu.Lock()
	defer p.retryMu.Unlock()
	if failures == 0 {
		delete(p.retrying, height)
	} else {
		p.retrying[height] = failures
	}
	var lowest, lowestFailures int64
	for h, n := range p.retrying {
		if lowest == 0 || h < lowest {
			lowest, lowestFailures = h, n
		}
	}
	p.metrics.retryHeight.Record(ctx, lowest)
	p.metrics.retryFailures.Record(ctx, lowestFailures)
}

// waitForFinalized returns once the fetcher reports height as finalized, asking every
// finalizedPoll, or with ctx's error once ctx is done.
func (p *ConcurrentBlockProcessor) waitForFinalized(ctx context.Context, height int64) error {
	for height > p.finalized {
		finalized, err := p.fetcher.FetchFinalizedBlockNumber(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			logger.Sugar.Warnf("Block %d waits for dispatch: finalized height unknown: %v", height, err)
		} else {
			p.finalized = finalized
			if height <= finalized {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.finalizedPoll):
		}
	}
	return nil
}
