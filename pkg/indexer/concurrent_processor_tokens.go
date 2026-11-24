package indexer

import (
	"context"
	"time"

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
