package indexer

// indexer_stop_indexing_test.go covers StopIndexing teardown variants and the
// indexer state/lifecycle transitions exercised through them (table-driven).

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------.
// StopIndexing variants + state/lifecycle (table-driven).
// ---------------------------------------------------------------------------.

func TestStopIndexingVariants(t *testing.T) {
	tests := []struct {
		name     string
		skipStop bool
		setup    func(t *testing.T) *ChainIndexer
		assert   func(t *testing.T, ix *ChainIndexer)
	}{
		{
			name:     "state accessors reflect flags",
			skipStop: true,
			setup: func(t *testing.T) *ChainIndexer {
				cfg := &config.Config{
					DefraDB: config.DefraDBConfig{URL: testDefraURL},
				}
				indexer, err := CreateIndexer(cfg)
				assert.NoError(t, err)
				return indexer
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				// Test initial state.
				assert.False(t, ix.IsStarted())
				assert.False(t, ix.HasIndexedAtLeastOneBlock())

				// Test state changes.
				ix.shouldIndex = true
				ix.isStarted = true
				ix.hasIndexedAtLeastOneBlock = true

				assert.True(t, ix.IsStarted())
				assert.True(t, ix.HasIndexedAtLeastOneBlock())
			},
		},
		{
			name: "stop resets state but keeps indexed history",
			setup: func(t *testing.T) *ChainIndexer {
				cfg := &config.Config{
					DefraDB: config.DefraDBConfig{URL: testDefraURL},
				}
				indexer, err := CreateIndexer(cfg)
				assert.NoError(t, err)

				// Set some state.
				indexer.shouldIndex = true
				indexer.isStarted = true
				indexer.hasIndexedAtLeastOneBlock = true
				return indexer
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				// Verify state is reset.
				assert.False(t, ix.shouldIndex)
				assert.False(t, ix.isStarted)
				// hasIndexedAtLeastOneBlock should remain true (historical fact).
				assert.True(t, ix.hasIndexedAtLeastOneBlock)
			},
		},
		{
			name: "lifecycle starts stopped and stays stopped",
			setup: func(t *testing.T) *ChainIndexer {
				cfg := &config.Config{
					DefraDB: config.DefraDBConfig{
						URL: testDefraURL,
						Store: config.DefraDBStoreConfig{
							Path: "/tmp/test_indexer",
						},
					},
					Indexer: config.IndexerConfig{
						StartHeight: 1,
					},
					Logger: config.LoggerConfig{
						Development: true,
					},
				}

				indexer, err := CreateIndexer(cfg)

				assert.NoError(t, err)
				return indexer
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				// Test initial state.
				assert.False(t, ix.IsStarted())
				assert.False(t, ix.HasIndexedAtLeastOneBlock())
				assert.Equal(t, -1, ix.GetDefraDBPort())

				// Test state after stopping (should remain stopped).
				assert.False(t, ix.IsStarted())
				assert.False(t, ix.HasIndexedAtLeastOneBlock())
			},
		},
		{
			name: "embedded node is closed and nilled",
			setup: func(t *testing.T) *ChainIndexer {
				td := testutils.SetupTestDefraDB(t)

				return &ChainIndexer{
					defraNode:   td.Node,
					shouldIndex: true,
					isStarted:   true,
					cfg:         &config.Config{},
				}
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
				assert.False(t, ix.isStarted)
				assert.Nil(t, ix.defraNode)
			},
		},
		{
			name: "started snapshotter is stopped and nilled",
			setup: func(t *testing.T) *ChainIndexer {
				s := snapshot.New(&config.SnapshotConfig{
					Enabled:         true,
					Dir:             t.TempDir(),
					BlocksPerFile:   1000,
					IntervalSeconds: 3600,
				}, nil, nil)

				err := s.Start(t.Context())
				require.NoError(t, err)

				return &ChainIndexer{
					shouldIndex: true,
					isStarted:   true,
					cfg:         &config.Config{},
					snapshotter: s,
				}
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
				assert.Nil(t, ix.snapshotter)
			},
		},
		{
			name: "pruner is stopped and nilled",
			setup: func(t *testing.T) *ChainIndexer {
				td := testutils.SetupTestDefraDB(t)
				p := pruner.NewPruner(&config.PrunerConfig{
					Enabled:   true,
					MaxBlocks: 1000,
				}, td.Node, nil)

				return &ChainIndexer{
					shouldIndex: true,
					isStarted:   true,
					cfg:         &config.Config{},
					pruner:      p,
				}
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
				assert.Nil(t, ix.pruner)
			},
		},
		{
			name: "health server is stopped",
			setup: func(t *testing.T) *ChainIndexer {
				hs := newHealthServerForTest(t)

				return &ChainIndexer{
					shouldIndex:  true,
					isStarted:    true,
					cfg:          &config.Config{},
					healthServer: hs,
				}
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
			},
		},
		{
			name: "all components are stopped and nilled",
			setup: func(t *testing.T) *ChainIndexer {
				td := testutils.SetupTestDefraDB(t)

				// Create fetcher, converter, and block handler wired to a mock RPC server.
				rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
					switch method {
					case ethGetBlockByNumber:
						return fullBlockResponse("0x1", nil), nil
					case ethGetBlockReceipts:
						return []any{}, nil
					default:
						return "0x1", nil
					}
				})
				t.Cleanup(rpcServer.Close)
				fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)
				require.NotNil(t, fetcher)

				// Create pruner.
				p := pruner.NewPruner(&config.PrunerConfig{
					Enabled:   true,
					MaxBlocks: 1000,
				}, td.Node, converter)

				// Create snapshotter.
				s := snapshot.New(&config.SnapshotConfig{
					Enabled:         true,
					Dir:             t.TempDir(),
					BlocksPerFile:   1000,
					IntervalSeconds: 3600,
				}, nil, nil)
				err := s.Start(t.Context())
				require.NoError(t, err)

				// Create health server.
				hs := server.NewHealthServer(0, nil, "")

				indexer := &ChainIndexer{
					shouldIndex:    true,
					isStarted:      true,
					fetcher:        fetcher,
					converter:      converter,
					blockHandler:   blockHandler,
					defraNode:      td.Node,
					pruner:         p,
					snapshotter:    s,
					healthServer:   hs,
					networkHandler: nil, // test nil network handler branch.
					cfg:            &config.Config{},
				}

				require.NotNil(t, indexer.fetcher, "fetcher should be set before StopIndexing")
				return indexer
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
				assert.False(t, ix.isStarted)
				assert.Nil(t, ix.fetcher, "StopIndexing should close and nil the fetcher")
				assert.Nil(t, ix.defraNode)
				assert.Nil(t, ix.pruner)
				assert.Nil(t, ix.snapshotter)
			},
		},
		{
			// Don't call p.Start()/s.Start() — they require the app-sdk logger
			// to be initialized. StopIndexing should handle calling Stop() on
			// unstarted components (isRunning=false → early return).
			name: "unstarted pruner and snapshotter tolerated",
			setup: func(t *testing.T) *ChainIndexer {
				td := testutils.SetupTestDefraDB(t)

				p := pruner.NewPruner(&config.PrunerConfig{
					Enabled:        true,
					MaxBlocks:      100,
					PruneThreshold: 10,
				}, td.Node, nil)
				p.SetQueue(pruner.NewIndexerQueue())

				s := snapshot.New(&config.SnapshotConfig{
					Enabled:         true,
					Dir:             t.TempDir(),
					BlocksPerFile:   100,
					IntervalSeconds: 3600,
				}, td.Node, nil)

				return &ChainIndexer{
					defraNode:   td.Node,
					isStarted:   true,
					shouldIndex: true,
					pruner:      p,
					snapshotter: s,
				}
			},
			assert: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.isStarted)
				assert.False(t, ix.shouldIndex)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			indexer := tc.setup(t)

			if !tc.skipStop {
				indexer.StopIndexing()
			}
			tc.assert(t, indexer)
		})
	}
}

// ---------------------------------------------------------------------------.
// StopIndexing vs in-flight StartIndexing.
//  Deterministic park: the mock RPC parks eth_getBlockByNumber("latest")
// (the first RPC call StartIndexing makes, from FetchHighestBlockNumber in
// resolveStartHeight — what HeaderByNumber(nil) actually sends), so the start
// is pinned mid-init with fetcher/defraNode already assigned while
// StopIndexing races it.
//
// StopIndexing can only tear down after the start settles, and a stop also
// cancels the start's init context, so the parked init aborts promptly
// instead of the stop waiting on the parked RPC.
//
// Mutation detection:
//   - Reverting the init-cancel in StopIndexing: the naked waitStartSettled
//     stalls the full IndexingStartStopTimeout (30s) on the parked start →
//     the bounded stop wait below fails.
//   - Removing the errIndexingStopped→nil mapping in finishStart: the aborted
//     start surfaces its abort artifact (the cancellation cause "indexing
//     stopped" propagated through the transport, or a wrapped
//     context.Canceled) → the NoError assertion fails.
//   - Removing stopMu: TestStopIndexing_ConcurrentStopsAfterInitError trips
//     -race on i.fetcher/i.defraNode.
// ---------------------------------------------------------------------------.

func TestStopIndexing_DuringStart_WaitsForStart(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	// Park the start mid-init (resolveStartHeight) until releaseRPC closes.
	// FetchHighestBlockNumber → HeaderByNumber(nil) → eth_getBlockByNumber
	// with the "latest" param — the FIRST RPC call StartIndexing makes, and
	// it happens before the runConcurrentIndexing handoff. This park is
	// context-aware client-side: cancelling the init context aborts it even
	// though the server handler stays parked until the cleanup release.
	releaseRPC := make(chan struct{})
	t.Cleanup(func() { close(releaseRPC) })
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if innerErr := json.Unmarshal(rawParams[0], &blockParam); innerErr == nil && blockParam == defaultBlockParamLatest {
					<-releaseRPC
					return fullBlockResponse("0x100", nil), nil
				}
			}
			return fullBlockResponse("0x100", nil), nil
		case ethBlockNumber:
			return "0x100", nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	t.Cleanup(rpcServer.Close)

	cfg := startPathBaseCfg(rpcServer.URL, testDefraRandomURL, tmpDir, config.IndexerConfig{
		StartHeight:      100,
		ConcurrentBlocks: 1,
		ReceiptWorkers:   2,
		MaxDocsPerTxn:    100,
		HealthServerPort: 0,
		StartBuffer:      10,
	})

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := startIndexingBackground(t, indexer)

	// Wait until StartIndexing is deterministically in its init phase.
	require.Eventually(t, func() bool {
		indexer.mutex.RLock()
		defer indexer.mutex.RUnlock()
		return indexer.startInProgress
	}, 10*time.Second, 10*time.Millisecond, "StartIndexing never entered its init phase")

	// Race StopIndexing against the parked start: the stop must cancel the
	// init context, wait for the start to settle, and only then tear down.
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		indexer.StopIndexing()
	}()

	// The stop must not ride out the full IndexingStartStopTimeout: the init
	// cancellation aborts the parked RPC in milliseconds (bounded generously
	// for slow machines — the 30s timeout path would exceed this).
	select {
	case <-stopDone:
	case <-time.After(10 * time.Second):
		t.Fatal("StopIndexing did not settle against the parked start (init context not cancelled?)")
	}

	select {
	case startErr := <-errCh:
		// An aborted start maps its context-canceled init failure to nil via
		// finishStart (a stopped start is not an init failure); no panic is
		// the core assertion.
		assert.NoError(t, startErr)
	case <-time.After(30 * time.Second):
		t.Fatal("StartIndexing did not return after the stop aborted it")
	}

	assert.False(t, indexer.isStarted, "indexer should be stopped")
	assert.False(t, indexer.shouldIndex, "indexer should not be indexing")
	assert.Nil(t, indexer.fetcher, "fetcher should be torn down")
	assert.Nil(t, indexer.defraNode, "defraNode should be torn down")
}

// TestStopIndexing_ConcurrentStopsAfterInitError pins the teardown
// serialization: when an init fails while an external StopIndexing is
// already waiting, the guard's StopIndexing and the external one wake at the
// same moment (markStartSettled closes startDone before the guard runs). They
// must serialize instead of racing on the teardown fields.
//
// The park happens inside the newBlockHandlerFn seam, which ignores context —
// so the stop's init cancellation cannot preempt it, and the release
// deterministically produces a GENUINE init failure. A genuine failure makes
// the error guard call StopIndexing concurrently with the parked external
// Stop: exactly the pairing stopMu must serialize.
func TestStopIndexing_ConcurrentStopsAfterInitError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	// Not t.Parallel: swaps the global newBlockHandlerFn seam.
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fullBlockResponse("0x100", nil), nil
		case ethBlockNumber:
			return "0x100", nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	t.Cleanup(rpcServer.Close)

	releaseSeam := make(chan struct{})
	released := false
	release := func() {
		if !released {
			released = true
			close(releaseSeam)
		}
	}
	original := newBlockHandlerFn
	newBlockHandlerFn = func(_ *node.Node, _ int) (*defra.BlockHandler, error) {
		<-releaseSeam
		return nil, errors.New("forced block handler failure")
	}
	t.Cleanup(func() {
		newBlockHandlerFn = original
		release()
	})

	cfg := startPathBaseCfg(rpcServer.URL, testDefraRandomURL, tmpDir, config.IndexerConfig{
		StartHeight:      100,
		ConcurrentBlocks: 1,
		ReceiptWorkers:   2,
		MaxDocsPerTxn:    100,
		HealthServerPort: 0,
		StartBuffer:      10,
	})

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := startIndexingBackground(t, indexer)

	// Park: fetcher, defraNode and networkHandler are all assigned before the
	// block handler stage.
	require.Eventually(t, func() bool {
		indexer.mutex.RLock()
		defer indexer.mutex.RUnlock()
		return indexer.startInProgress
	}, 10*time.Second, 10*time.Millisecond, "StartIndexing never entered its init phase")

	// Start the external stop while init is parked: it cancels the init
	// context (no effect on the context-blind seam) and waits for settle.
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		indexer.StopIndexing()
	}()

	// Release the seam with a genuine init failure. markStartSettled closes
	// startDone: the external stop's wait unblocks while the guard's own
	// StopIndexing (via the error guard) is starting — both must serialize.
	release()

	select {
	case <-stopDone:
	case <-time.After(30 * time.Second):
		t.Fatal("external StopIndexing did not finish after the start failed")
	}

	select {
	case startErr := <-errCh:
		// A genuine init failure is NOT mapped to nil: it must surface, and
		// the guard's StopIndexing must still have torn down cleanly.
		assert.ErrorContains(t, startErr, "forced block handler failure")
	case <-time.After(30 * time.Second):
		t.Fatal("StartIndexing did not return after the forced init failure")
	}

	assert.False(t, indexer.isStarted, "indexer should be stopped")
	assert.False(t, indexer.shouldIndex, "indexer should not be indexing")
	assert.Nil(t, indexer.fetcher, "fetcher should be torn down exactly once, without a data race")
	assert.Nil(t, indexer.defraNode, "defraNode should be torn down exactly once, without a data race")
}
