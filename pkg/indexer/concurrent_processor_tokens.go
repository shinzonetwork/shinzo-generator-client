package indexer

import (
	"context"
	"fmt"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
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

// RetryFailedHeights makes a height whose fetch, convert or store failed retry in place, and stores
// nothing DefaultWorkersAhead*workers or more above it before it. A height that keeps failing for
// StuckHeightLimit stops the run with ErrHeightStuck, unless the node is unavailable. A store
// refusal that retrying cannot clear stops the run at once. Call it before ProcessBlocks.
func (p *ConcurrentBlockProcessor) RetryFailedHeights() {
	p.retryFailedHeights = true
	p.stuckHeightLimit = StuckHeightLimit
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

// retryInPlace calls attempt and, when failed heights are retried in place, again until it
// succeeds, ctx is done, or a store is refused in a way retrying cannot clear. The wait between
// attempts starts at FailedHeightRetryDelay and doubles up to MaxFailedHeightRetryDelay. Once the
// height has failed for stuckHeightLimit, a failure other than chains.ErrNodeUnavailable stops the
// run with ErrHeightStuck. It returns the last result.
func (p *ConcurrentBlockProcessor) retryInPlace(ctx context.Context, attempt func() *BlockResult) *BlockResult {
	result := attempt()
	failingSince := time.Now()
	delay := FailedHeightRetryDelay
	for p.retryFailedHeights && result.Error != nil && !errors.IsErrDocumentDeleted(result.Error) &&
		ctx.Err() == nil {
		failing := time.Since(failingSince)
		if failing >= p.stuckHeightLimit && !stderrors.Is(result.Error, chains.ErrNodeUnavailable) {
			p.failRun(fmt.Errorf("%w: block %d has failed for %v: %w", ErrHeightStuck, result.BlockNum,
				failing.Round(time.Second), result.Error))
			return result
		}
		logger.Sugar.Warnf("Block %d failed, retrying in %v: %v", result.BlockNum, delay, result.Error)
		select {
		case <-ctx.Done():
			return result
		case <-time.After(delay):
		}
		delay = min(2*delay, MaxFailedHeightRetryDelay) //nolint:mnd
		result = attempt()
	}
	return result
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
