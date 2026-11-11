package indexer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTokensModeIndexer returns a tokens-mode indexer over fetcher, with a test DefraDB.
func newTokensModeIndexer(t *testing.T, fetcher *testutils.MockFetcher) *ChainIndexer {
	t.Helper()
	td := testutils.SetupTestDefraDB(t)
	converter := chainConverter()
	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	return &ChainIndexer{
		cfg: &config.Config{
			Chain:   config.ChainConfig{Mode: config.ChainModeTokens},
			Indexer: config.IndexerConfig{ConcurrentBlocks: 2},
		},
		fetcher:      fetcher,
		converter:    converter,
		blockHandler: handler,
		defraNode:    td.Node,
	}
}

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
			indexer := newTokensModeIndexer(t, fetcher)
			indexer.cfg.Chain.FinalizedPollIntervalMs = tc.pollMs

			err := indexer.runConcurrentIndexing(ctx, 5001, indexer.cfg)
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

// In tokens mode the indexer keeps fetching a height whose fetch fails, instead of passing it.
func TestRunConcurrentIndexing_TokensModeRetriesFailedHeights(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)
	fetcher := &testutils.MockFetcher{
		FetchBlockFn:                func(context.Context, int64) (any, error) { return nil, errors.New("fetch failed") },
		FetchFinalizedBlockNumberFn: func(context.Context) (int64, error) { return 5001, nil },
	}
	indexer := newTokensModeIndexer(t, fetcher)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	require.ErrorIs(t, indexer.runConcurrentIndexing(ctx, 5001, indexer.cfg), context.DeadlineExceeded)
	assert.Greater(t, len(fetcher.FetchBlockCalls), MaxRPCRetries)
}

// In tokens mode a reorg stops indexing instead of rolling back.
func TestRunConcurrentIndexing_TokensModeStopsOnReorg(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)
	var orphanFetched atomic.Bool
	fetcher := &testutils.MockFetcher{
		FetchBlockFn: func(_ context.Context, height int64) (any, error) {
			if height == 100 && orphanFetched.CompareAndSwap(false, true) {
				return &servedBlock{height: 100, hash: orphanHash(100), parentHash: canonicalHash(99)}, nil
			}
			return &servedBlock{height: height, hash: canonicalHash(height), parentHash: canonicalHash(height - 1)}, nil
		},
		FetchBlockHeaderFn:          headerLookup(canonicalHashIndex(95, 115)),
		FetchFinalizedBlockNumberFn: func(context.Context) (int64, error) { return 110, nil },
	}
	indexer := newTokensModeIndexer(t, fetcher)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.ErrorIs(t, indexer.runConcurrentIndexing(ctx, 100, indexer.cfg), ErrReorgStopped)
}

// A tokens-mode restart resumes at the lowest unstored height the pruner still keeps and never
// skips ahead; full mode skips ahead when the gap to the tip exceeds the start buffer. listFrom is
// the lowest height the converter is asked to list (0: none).
func TestResolveStartHeight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mode        string
		startHeight int
		maxBlocks   int64
		stored      []int64
		want        int64
		listFrom    int64
		wantErr     error
	}{
		{name: "tokens mode, gap below the highest", mode: config.ChainModeTokens, startHeight: 100, maxBlocks: 1000, stored: []int64{100, 101, 103, 104}, want: 102, listFrom: 100},
		{name: "tokens mode, no gap", mode: config.ChainModeTokens, startHeight: 100, maxBlocks: 1000, stored: []int64{100, 101, 102}, want: 103, listFrom: 100},
		{name: "tokens mode, start height never stored", mode: config.ChainModeTokens, startHeight: 100, maxBlocks: 1000, stored: []int64{101, 102}, want: 100, listFrom: 100},
		{name: "tokens mode, gap below the retained heights", mode: config.ChainModeTokens, startHeight: 100, maxBlocks: 4, stored: []int64{100, 102, 103, 104, 105}, want: 106, listFrom: 102},
		{name: "tokens mode, gap at the lowest retained height", mode: config.ChainModeTokens, startHeight: 100, maxBlocks: 4, stored: []int64{100, 101, 103, 104, 105}, want: 102, listFrom: 102},
		{name: "tokens mode without a start height", mode: config.ChainModeTokens, maxBlocks: 1000, stored: []int64{100}, wantErr: ErrStartHeightUnset},
		{name: "tokens mode, max_blocks below the dispatch window", mode: config.ChainModeTokens, startHeight: 100, maxBlocks: 3, stored: []int64{100}, wantErr: ErrRetentionBelowDispatch},
		{name: "full mode", mode: config.ChainModeFull, stored: []int64{100, 101, 102}, want: 990},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var listedFrom int64
			indexer := &ChainIndexer{
				converter: &testutils.MockConverter{
					GetHighestStoredBlockNumberFn: func(context.Context, *node.Node) (int64, error) {
						return tc.stored[len(tc.stored)-1], nil
					},
					GetStoredBlockNumbersFn: func(_ context.Context, _ *node.Node, from int64) ([]int64, error) {
						listedFrom = from
						return slices.DeleteFunc(slices.Clone(tc.stored), func(h int64) bool { return h < from }), nil
					},
				},
				fetcher: &testutils.MockFetcher{
					FetchHighestBlockNumberFn: func(context.Context) (int64, error) { return 1000, nil },
				},
			}
			cfg := &config.Config{
				Chain:   config.ChainConfig{Mode: tc.mode},
				Indexer: config.IndexerConfig{StartBuffer: 10, StartHeight: tc.startHeight, ConcurrentBlocks: 2},
				Pruner:  config.PrunerConfig{MaxBlocks: tc.maxBlocks},
			}

			got, err := indexer.resolveStartHeight(context.Background(), cfg)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.listFrom, listedFrom)
		})
	}
}

// A tokens-mode start on an empty store begins at start_height. A store whose highest height cannot
// be read fails the start with the query's error: here it holds only a Header without a number,
// which GetHighestStoredBlockNumber reports as chains.ErrBlockNumberCorrupt.
func TestResolveStartHeight_TokensModeHighestStored(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		numberless bool
		want       int64
		wantErr    error
	}{
		{name: "empty store", want: 100},
		{name: "highest height unreadable", numberless: true, wantErr: chains.ErrBlockNumberCorrupt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Chain: config.ChainConfig{
					Name: "Ethereum", Network: "Mainnet", ChainID: 1, Mode: config.ChainModeTokens,
					Tokens:               []config.Token{{Address: "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d", SupplySlot: "0x35"}},
					StateCaptureInterval: 1,
				},
				Indexer: config.IndexerConfig{StartHeight: 100, ConcurrentBlocks: 2},
				Pruner:  config.PrunerConfig{MaxBlocks: 1000},
			}
			converter, err := evm.NewConverter(cfg)
			require.NoError(t, err)
			sdl, err := converter.GetSchema()
			require.NoError(t, err)
			td := testutils.SetupTestDefraDBWithSchema(t, sdl)
			if tc.numberless {
				headers := converter.Collections().BlockCollection()
				res := td.Node.DB.ExecRequest(context.Background(), fmt.Sprintf(`mutation { add_%s(input: {hash: "0x05"}) { _docID } }`, headers))
				require.Empty(t, res.GQL.Errors)
			}
			indexer := &ChainIndexer{
				converter: converter,
				defraNode: td.Node,
				fetcher: &testutils.MockFetcher{
					FetchHighestBlockNumberFn: func(context.Context) (int64, error) { return 1000, nil },
				},
			}

			got, err := indexer.resolveStartHeight(context.Background(), cfg)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.want, got)
		})
	}
}
