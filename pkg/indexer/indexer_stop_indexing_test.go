package indexer

// indexer_stop_indexing_test.go covers StopIndexing teardown variants and the
// indexer state/lifecycle transitions exercised through them (table-driven).

import (
	"encoding/json"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
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
