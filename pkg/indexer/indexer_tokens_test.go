package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In tokens mode the indexer fetches no block above the finalized height and asks for that height
// every chain.finalized_poll_interval_ms, or every FinalizedPollInterval when it is unset. Each run
// is timed from its first poll to its last, so start-up time does not count.
func TestRunConcurrentIndexing_TokensModeDispatchesFinalizedOnly(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)
	cases := []struct {
		name     string
		pollMs   int64
		interval time.Duration // polls come at least this far apart
		polls    int           // the run stops at this poll
	}{
		{name: "default interval", interval: FinalizedPollInterval, polls: 2},
		{name: "configured interval", pollMs: 10, interval: 10 * time.Millisecond, polls: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var polled []time.Time
			fetcher := &testutils.MockFetcher{
				FetchFinalizedBlockNumberFn: func(context.Context) (int64, error) {
					polled = append(polled, time.Now())
					if len(polled) == tc.polls {
						cancel()
					}
					return 5000, nil
				},
			}
			td := testutils.SetupTestDefraDB(t)
			converter := chainConverter()
			handler, err := defra.NewBlockHandler(td.Node, 1000)
			require.NoError(t, err)
			indexer := &ChainIndexer{
				cfg: &config.Config{
					Chain:   config.ChainConfig{Mode: config.ChainModeTokens, FinalizedPollIntervalMs: tc.pollMs},
					Indexer: config.IndexerConfig{ConcurrentBlocks: 2},
				},
				fetcher:      fetcher,
				converter:    converter,
				blockHandler: handler,
				defraNode:    td.Node,
			}

			err = indexer.runConcurrentIndexing(ctx, 5001, indexer.cfg)
			assert.Empty(t, fetcher.FetchBlockCalls)
			require.ErrorIs(t, err, context.Canceled)
			require.Len(t, polled, tc.polls)
			for i := 1; i < len(polled); i++ {
				assert.GreaterOrEqual(t, polled[i].Sub(polled[i-1]), tc.interval)
			}
			if tc.pollMs != 0 {
				// At FinalizedPollInterval the last poll could come no sooner than this after the first.
				assert.Less(t, polled[len(polled)-1].Sub(polled[0]), time.Duration(tc.polls-1)*FinalizedPollInterval)
			}
		})
	}
}
