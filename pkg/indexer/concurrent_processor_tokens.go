package indexer

import (
	"context"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
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

// RetryFailedHeights makes a height whose fetch, convert or store failed retry until it succeeds.
// A store refusal that retrying cannot clear still stops the run. Call it before ProcessBlocks.
func (p *ConcurrentBlockProcessor) RetryFailedHeights() {
	p.retryFailedHeights = true
}

// retryInPlace calls attempt and, when failed heights are retried in place, again every
// FailedHeightRetryDelay until it succeeds, ctx is done, or a store is refused in a way retrying
// cannot clear. It returns the last result.
func (p *ConcurrentBlockProcessor) retryInPlace(ctx context.Context, attempt func() *BlockResult) *BlockResult {
	result := attempt()
	for p.retryFailedHeights && result.Error != nil && !errors.IsErrDocumentDeleted(result.Error) &&
		ctx.Err() == nil {
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
