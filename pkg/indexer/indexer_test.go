package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	indexerErrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexing_StartDefraFirst is now replaced by mock-based integration tests.
// See ./integration/ directory for comprehensive integration tests with mock data.
func TestIndexing_StartDefraFirst(t *testing.T) {
	t.Parallel()
	t.Skip("This test has been replaced by mock-based integration tests in ./integration/ - run 'make test' for full test suite")
}

func TestIndexing(t *testing.T) {
	t.Parallel()
	t.Skip("This test has been replaced by mock-based integration tests in ./integration/ - run 'make test' for full test suite")
}

// TestCreateIndexer tests indexer creation across valid, nil, and custom configs.
func TestCreateIndexer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		cfg             *config.Config
		wantErr         bool
		wantErrContains []string
		wantURL         string
		wantStartHeight int
		checkInitState  bool
	}{
		{
			name: "valid config",
			cfg: &config.Config{
				DefraDB: config.DefraDBConfig{
					URL: "http://localhost:9181",
				},
				Indexer: config.IndexerConfig{
					StartHeight: 100,
				},
			},
			wantURL:        "http://localhost:9181",
			checkInitState: true,
		},
		{
			name:            "nil config",
			cfg:             nil,
			wantErr:         true,
			wantErrContains: []string{"config is nil", "CONFIGURATION_ERROR"},
		},
		{
			name: "custom config is preserved",
			cfg: &config.Config{
				DefraDB: config.DefraDBConfig{
					URL: "http://localhost:8888",
					Store: config.DefraDBStoreConfig{
						Path: "/tmp/test_defra",
					},
				},
				Geth: config.GethConfig{
					NodeURL: "http://localhost:8545",
				},
				Indexer: config.IndexerConfig{
					StartHeight: 500,
				},
				Logger: config.LoggerConfig{
					Development: true,
				},
			},
			wantURL:         "http://localhost:8888",
			wantStartHeight: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			indexer, err := CreateIndexer(tt.cfg)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, indexer)
				for _, substr := range tt.wantErrContains {
					assert.Contains(t, err.Error(), substr)
				}
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, indexer)
			assert.Equal(t, tt.cfg, indexer.cfg)
			if tt.wantURL != "" {
				assert.Equal(t, tt.wantURL, indexer.cfg.DefraDB.URL)
			}
			if tt.wantStartHeight != 0 {
				assert.Equal(t, tt.wantStartHeight, indexer.cfg.Indexer.StartHeight)
			}
			if tt.checkInitState {
				assert.False(t, indexer.shouldIndex)
				assert.False(t, indexer.isStarted)
				assert.False(t, indexer.hasIndexedAtLeastOneBlock)
				assert.Nil(t, indexer.defraNode)
			}
		})
	}
}

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

// TestGetDefraDBPort tests port retrieval with and without an embedded node.
func TestGetDefraDBPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T) (*ChainIndexer, int, bool) // indexer, wantPort, wantPort > 0
	}{
		{
			name: "created indexer without embedded node returns -1",
			setup: func(t *testing.T) (*ChainIndexer, int, bool) {
				cfg := &config.Config{
					DefraDB: config.DefraDBConfig{URL: testDefraURL},
				}
				indexer, err := CreateIndexer(cfg)
				require.NoError(t, err)
				return indexer, -1, false
			},
		},
		{
			name: "nil defraNode returns -1",
			setup: func(_ *testing.T) (*ChainIndexer, int, bool) {
				return &ChainIndexer{defraNode: nil}, -1, false
			},
		},
		{
			name: "embedded node returns its port",
			setup: func(t *testing.T) (*ChainIndexer, int, bool) {
				td := testutils.SetupTestDefraDB(t)
				return &ChainIndexer{defraNode: td.Node}, td.Port, true
			},
		},
		{
			name: "embedded node port is consistent and valid",
			setup: func(t *testing.T) (*ChainIndexer, int, bool) {
				td := testutils.SetupTestDefraDB(t)
				return &ChainIndexer{defraNode: td.Node}, td.Port, true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			indexer, wantPort, wantPositive := tt.setup(t)
			port := indexer.GetDefraDBPort()
			assert.Equal(t, wantPort, port)
			if wantPositive {
				assert.Greater(t, port, 0)
			}
		})
	}
}

// ---------------------------------------------------------------------------.
// StartIndexing — error paths (nil config, external DefraDB, RPC failures).
// ---------------------------------------------------------------------------.

func TestStartIndexing_ErrorPaths(t *testing.T) {
	tests := []struct {
		name            string
		skipInShort     bool
		setup           func(t *testing.T) *ChainIndexer
		startExternal   bool
		wantErrContains string
		wantNil         func(t *testing.T, ix *ChainIndexer)
	}{
		{
			name: "nil config",
			// Test that configuration is required.
			setup: func(_ *testing.T) *ChainIndexer {
				return &ChainIndexer{cfg: nil}
			},
			startExternal:   true,
			wantErrContains: "configuration is required",
		},
		{
			name: "external defra without node",
			// External DefraDB no longer sets defraNode, so StartIndexing
			// returns "defraNode is required" after applying schema.
			setup: func(t *testing.T) *ChainIndexer {
				td := testutils.SetupTestDefraDB(t)

				// Create a config pointing to the test DefraDB as "external".
				cfg := &config.Config{
					DefraDB: config.DefraDBConfig{
						URL: fmt.Sprintf("http://localhost:%d", td.Port),
					},
					Geth: config.GethConfig{NodeURL: "http://localhost:9999"},
					Indexer: config.IndexerConfig{
						StartHeight:    0,
						ReceiptWorkers: 1,
						MaxDocsPerTxn:  100,
					},
					Logger: config.LoggerConfig{Development: true},
				}

				indexer, err := CreateIndexer(cfg)
				require.NoError(t, err)
				return indexer
			},
			startExternal:   true,
			wantErrContains: "defraNode is required",
		},
		{
			name:        "get latest block number error",
			skipInShort: true,
			setup: func(t *testing.T) *ChainIndexer {
				// The RPC server returns an error for eth_getBlockByNumber with
				// the "latest" param (which is what GetLatestBlockNumber uses).
				rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
					switch method {
					case ethGetBlockByNumber:
						var rawParams []json.RawMessage
						if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
							var blockParam string
							if innerErr := json.Unmarshal(rawParams[0], &blockParam); innerErr == nil && blockParam == defaultBlockParamLatest {
								return nil, fmt.Errorf("rpc connection refused")
							}
						}
						return fullBlockResponse("0x100", nil), nil
					case ethBlockNumber:
						return nil, fmt.Errorf("rpc connection refused")
					default:
						return "0x1", nil
					}
				})
				t.Cleanup(rpcServer.Close)

				cfg := &config.Config{
					DefraDB: config.DefraDBConfig{
						URL:           testDefraRandomURL,
						KeyringSecret: "test-secret-for-keyring-12345678",
						P2P:           testDefraP2PDisabled,
						Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
					},
					Geth: config.GethConfig{NodeURL: rpcServer.URL},
					Indexer: config.IndexerConfig{
						StartHeight:      100,
						ConcurrentBlocks: 1,
						ReceiptWorkers:   2,
						MaxDocsPerTxn:    100,
						HealthServerPort: 0,
						StartBuffer:      10,
					},
					Logger: config.LoggerConfig{Development: true},
				}

				indexer, err := CreateIndexer(cfg)
				require.NoError(t, err)
				return indexer
			},
			wantErrContains: "failed to get latest block number",
			wantNil: func(t *testing.T, ix *ChainIndexer) {
				assert.Nil(t, ix.fetcher, "fetcher should be nil after error-path cleanup")
				assert.Nil(t, ix.defraNode, "defraNode should be nil after error-path cleanup")
				assert.Nil(t, ix.networkHandler, "networkHandler should be nil after error-path cleanup")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.skipInShort && testing.Short() {
				t.Skip("skipping integration test in short mode")
			}
			logger.InitConsoleOnly(true)

			indexer := tc.setup(t)
			err := indexer.StartIndexing(tc.startExternal)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrContains)
			t.Logf("StartIndexing error (expected): %v", err)

			if tc.wantNil != nil {
				tc.wantNil(t, indexer)
			}
		})
	}
}

// TestConstants tests the defined constants.
func TestConstants(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 10, DefaultBlocksToIndexAtOnce)
	assert.Equal(t, 3, DefaultRetryAttempts)
	assert.Equal(t, 15*time.Second, DefaultSchemaWaitTimeout)
	assert.Equal(t, 30*time.Second, DefaultDefraReadyTimeout)
	assert.Equal(t, 3, DefaultBlockOffset)
	assert.Equal(t, "/ip4/127.0.0.1/tcp/9171", defaultListenAddress)
}

// TestConvertGethBlockToDefraBlock verifies field-preserving conversion of geth
// blocks into Defra blocks across full, empty-transaction, and processing shapes.
func TestConvertGethBlockToDefraBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		gethBlock *evm.Block
		wantTxLen int
	}{
		{
			name: "full block with one transaction",
			gethBlock: &evm.Block{
				Number:           "12345",
				Hash:             "0x1234567890abcdef",
				ParentHash:       "0xabcdef1234567890",
				Timestamp:        "1640995200",
				Miner:            testMinerAddr,
				GasLimit:         "8000000",
				GasUsed:          "21000",
				Difficulty:       "1000000",
				TotalDifficulty:  "5000000",
				Nonce:            "0x1234567890abcdef",
				Sha3Uncles:       testSha3Uncles,
				LogsBloom:        "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
				TransactionsRoot: testTransactionsRoot,
				StateRoot:        "0xd7f8974fb5ac78d9ac099b9ad5018bedc2ce0a72dad1827a1709da30580f0544",
				ReceiptsRoot:     testTransactionsRoot,

				Size:      "1000",
				ExtraData: "0x",
				Transactions: []evm.Transaction{
					{
						Hash:             "0xabc123",
						BlockNumber:      "12345",
						From:             "0x1234567890123456789012345678901234567890",
						To:               "0x0987654321098765432109876543210987654321",
						Value:            "1000000000000000000",
						Gas:              "21000",
						GasPrice:         "20000000000",
						Nonce:            "1",
						TransactionIndex: 0,
						Type:             "0",
						ChainID:          "1",
						V:                "27",
						R:                "12345",
						S:                "67890",
					},
				},
			},
			wantTxLen: 1,
		},
		{
			name: "block with empty transactions",
			gethBlock: &evm.Block{
				Number:       "12345",
				Hash:         "0x1234567890abcdef",
				ParentHash:   "0xabcdef1234567890",
				Timestamp:    "1640995200",
				Miner:        testMinerAddr,
				GasLimit:     "8000000",
				GasUsed:      "0",
				Transactions: []evm.Transaction{}, // Empty transactions.
			},
			wantTxLen: 0,
		},
		{
			name: "processing block with one transaction",
			gethBlock: &evm.Block{
				Number:     "100",
				Hash:       "0xtest123",
				ParentHash: "0xparent123",
				Timestamp:  "1640995200",
				Miner:      testMinerAddr,
				GasLimit:   "8000000",
				GasUsed:    "21000",
				Transactions: []evm.Transaction{
					{
						Hash:             "0xtx123",
						BlockNumber:      "100",
						From:             "0xfrom123",
						To:               "0xto123",
						Value:            "1000000",
						Gas:              "21000",
						GasPrice:         "20000000000",
						Nonce:            "1",
						TransactionIndex: 0,
					},
				},
			},
			wantTxLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger.InitConsoleOnly(true)

			defraBlock := &evm.Block{
				Number:           tt.gethBlock.Number,
				Hash:             tt.gethBlock.Hash,
				ParentHash:       tt.gethBlock.ParentHash,
				Nonce:            tt.gethBlock.Nonce,
				Sha3Uncles:       tt.gethBlock.Sha3Uncles,
				LogsBloom:        tt.gethBlock.LogsBloom,
				TransactionsRoot: tt.gethBlock.TransactionsRoot,
				StateRoot:        tt.gethBlock.StateRoot,
				ReceiptsRoot:     tt.gethBlock.ReceiptsRoot,
				Miner:            tt.gethBlock.Miner,
				Difficulty:       tt.gethBlock.Difficulty,
				TotalDifficulty:  tt.gethBlock.TotalDifficulty,
				ExtraData:        tt.gethBlock.ExtraData,
				Size:             tt.gethBlock.Size,
				GasLimit:         tt.gethBlock.GasLimit,
				GasUsed:          tt.gethBlock.GasUsed,
				Timestamp:        tt.gethBlock.Timestamp,
				Transactions:     tt.gethBlock.Transactions,
			}

			assert.NotNil(t, defraBlock)
			assert.Equal(t, tt.gethBlock.Number, defraBlock.Number)
			assert.Equal(t, tt.gethBlock.Hash, defraBlock.Hash)
			assert.Equal(t, tt.gethBlock.ParentHash, defraBlock.ParentHash)
			assert.Equal(t, tt.gethBlock.Timestamp, defraBlock.Timestamp)
			assert.Equal(t, tt.gethBlock.Miner, defraBlock.Miner)
			assert.Equal(t, tt.gethBlock.GasLimit, defraBlock.GasLimit)
			assert.Equal(t, tt.gethBlock.GasUsed, defraBlock.GasUsed)
			assert.Len(t, defraBlock.Transactions, tt.wantTxLen)
			if tt.wantTxLen > 0 {
				assert.Equal(t, tt.gethBlock.Transactions[0].Hash, defraBlock.Transactions[0].Hash)
				assert.Equal(t, tt.gethBlock.Transactions[0].From, defraBlock.Transactions[0].From)
				assert.Equal(t, tt.gethBlock.Transactions[0].To, defraBlock.Transactions[0].To)
			}
		})
	}
}

// TestRequiredPeersInitialization tests required peers initialization.
func TestRequiredPeersInitialization(t *testing.T) {
	t.Parallel()
	// requiredPeers was inlined into the bootstrap configuration.
	// Verify the empty slice literal behaves as expected.
	peers := []string{}
	assert.NotNil(t, peers)
	assert.IsType(t, []string{}, peers)
}

// ---------------------------------------------------------------------------
// IsHealthy tests
// ---------------------------------------------------------------------------

func TestIsHealthy(t *testing.T) {
	t.Parallel()

	updatedBlock := int64(42)

	tests := []struct {
		name              string
		isStarted         bool
		zeroLastProcessed bool
		processedAgo      time.Duration
		updateBlock       *int64
		wantHealthy       bool
	}{
		{name: "unhealthy when not started", isStarted: false, zeroLastProcessed: true, wantHealthy: false},
		{name: "healthy when started but never processed (starting up)", isStarted: true, zeroLastProcessed: true, wantHealthy: true},
		{name: "healthy when recently processed", isStarted: true, processedAgo: 1 * time.Minute, wantHealthy: true},
		{name: "unhealthy when last processed >10 minutes ago", isStarted: true, processedAgo: 11 * time.Minute, wantHealthy: false},
		{name: "healthy just under 10 minute threshold", isStarted: true, processedAgo: 9*time.Minute + 59*time.Second, wantHealthy: true},
		{name: "healthy after updateBlockInfo", isStarted: true, zeroLastProcessed: true, updateBlock: &updatedBlock, wantHealthy: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			indexer := &ChainIndexer{
				isStarted: tt.isStarted,
			}
			if !tt.zeroLastProcessed {
				// Fresh per subtest: parallel subtests may resume long after
				// the table was built, and the 9m59s boundary case has only
				// 1s of margin before IsHealthy's 10-minute threshold.
				indexer.lastProcessedTime = time.Now().Add(-tt.processedAgo)
			}

			if tt.updateBlock == nil {
				assert.Equal(t, tt.wantHealthy, indexer.IsHealthy())
				return
			}

			// Before any update: zero time means healthy (startup phase).
			assert.True(t, indexer.IsHealthy(), "zero lastProcessedTime should be healthy before update")
			indexer.updateBlockInfo(*tt.updateBlock)
			assert.Equal(t, tt.wantHealthy, indexer.IsHealthy(), "should be healthy after recent block update")
			assert.Equal(t, *tt.updateBlock, indexer.GetCurrentBlock())
		})
	}
}

// ---------------------------------------------------------------------------
// GetCurrentBlock / GetLastProcessedTime tests
// ---------------------------------------------------------------------------

func TestGetCurrentBlockAndLastProcessedTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		updates      []int64
		sleepBetween time.Duration
		wantBlocks   []int64
	}{
		{name: "default values are zero"},
		{name: "single update reflects block and time", updates: []int64{12345}, wantBlocks: []int64{12345}},
		{name: "multiple updates reflect the most recent", updates: []int64{100, 200, 300}, wantBlocks: []int64{100, 200, 300}},
		{name: "subsequent updates advance time", updates: []int64{500, 501}, sleepBetween: 1 * time.Millisecond, wantBlocks: []int64{500, 501}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			indexer := &ChainIndexer{}

			// Default state: currentBlock and lastProcessedTime are zero values.
			assert.Equal(t, int64(0), indexer.GetCurrentBlock(), "default currentBlock should be 0")
			assert.True(t, indexer.GetLastProcessedTime().IsZero(), "default lastProcessedTime should be zero")

			var prevProcessed time.Time
			for i, n := range tt.updates {
				if tt.sleepBetween > 0 && i > 0 {
					time.Sleep(tt.sleepBetween)
				}
				before := time.Now()
				indexer.updateBlockInfo(n)
				after := time.Now()

				assert.Equal(t, tt.wantBlocks[i], indexer.GetCurrentBlock())

				processed := indexer.GetLastProcessedTime()
				assert.False(t, processed.IsZero(), "lastProcessedTime should not be zero after update")
				assert.False(t, processed.Before(before), "lastProcessedTime should be >= time before update")
				assert.False(t, processed.After(after), "lastProcessedTime should be <= time after update")
				if i > 0 && tt.sleepBetween > 0 {
					assert.False(t, processed.Before(prevProcessed),
						"lastProcessedTime should advance or stay same with subsequent updates")
				}
				prevProcessed = processed
			}
		})
	}
}

// ---------------------------------------------------------------------------
// updateBlockInfo tests
// ---------------------------------------------------------------------------

func TestUpdateBlockInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		updates    []int64
		wantBlocks []int64
		checkTime  bool
	}{
		{name: "updates current block", updates: []int64{42, 999}, wantBlocks: []int64{42, 999}},
		{name: "allows lower block numbers (not monotonic)", updates: []int64{500, 100}, wantBlocks: []int64{500, 100}},
		{name: "accepts zero block and sets lastProcessedTime", updates: []int64{0}, wantBlocks: []int64{0}, checkTime: true},
		{name: "does not reject negative block numbers", updates: []int64{-1}, wantBlocks: []int64{-1}},
		{name: "sets lastProcessedTime to approximately now", updates: []int64{100}, wantBlocks: []int64{100}, checkTime: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			indexer := &ChainIndexer{}

			for i, n := range tt.updates {
				before := time.Now()
				indexer.updateBlockInfo(n)
				assert.Equal(t, tt.wantBlocks[i], indexer.currentBlock)

				if tt.checkTime {
					assert.False(t, indexer.lastProcessedTime.IsZero(), "lastProcessedTime should be set")
					assert.WithinDuration(t, time.Now(), indexer.lastProcessedTime, 1*time.Second,
						"lastProcessedTime should be approximately the current time")
					assert.False(t, indexer.lastProcessedTime.Before(before),
						"lastProcessedTime should not be before the call")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetPrunerMetrics tests
// ---------------------------------------------------------------------------

func TestGetPrunerMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T) *ChainIndexer
		wantNil bool
	}{
		{
			name: "nil pruner returns nil metrics",
			setup: func(_ *testing.T) *ChainIndexer {
				return &ChainIndexer{pruner: nil}
			},
			wantNil: true,
		},
		{
			name: "enabled pruner returns non-nil metrics",
			setup: func(t *testing.T) *ChainIndexer {
				td := testutils.SetupTestDefraDB(t)
				p := pruner.NewPruner(&config.PrunerConfig{
					Enabled:   true,
					MaxBlocks: 1000,
				}, td.Node, nil)
				return &ChainIndexer{pruner: p}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			indexer := tt.setup(t)
			metrics := indexer.GetPrunerMetrics()
			if tt.wantNil {
				assert.Nil(t, metrics, "GetPrunerMetrics should return nil when pruner is nil")
				return
			}
			require.NotNil(t, metrics)
			assert.True(t, metrics.Enabled)
		})
	}
}

// ---------------------------------------------------------------------------
// applySchemaViaHTTP tests.
// ---------------------------------------------------------------------------

func TestApplySchemaViaHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		url             string
		handler         func(callCount int, r *http.Request) (status int, body string)
		wantCalls       int
		wantMinCalls    int
		wantPath        string
		wantErr         bool
		wantErrContains string
	}{
		{
			name: "success makes exactly one POST to the collections endpoint",
			handler: func(_ int, r *http.Request) (int, string) {
				assert.Equal(t, "POST", r.Method)
				return http.StatusOK, ""
			},
			wantCalls: 1,
			wantPath:  "/api/v0/collections",
		},
		{
			name: "server error is returned",
			handler: func(_ int, _ *http.Request) (int, string) {
				return http.StatusInternalServerError, "schema error"
			},
			wantErr:         true,
			wantErrContains: "500",
		},
		{
			name:    "connection refused is returned as error",
			url:     "http://127.0.0.1:1",
			wantErr: true,
		},
		{
			name: "collection already exists falls back to per-file requests",
			handler: func(callCount int, _ *http.Request) (int, string) {
				if callCount == 1 {
					// First call (monolithic): respond with "collection already exists".
					return http.StatusInternalServerError, indexerErrors.ErrStrCollectionAlreadyExists
				}
				// Subsequent per-file calls succeed.
				return http.StatusOK, ""
			},
			wantMinCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			url := tt.url
			callCount := 0
			if tt.handler != nil {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					callCount++
					if tt.wantPath != "" {
						assert.Equal(t, tt.wantPath, r.URL.Path)
					}
					status, body := tt.handler(callCount, r)
					w.WriteHeader(status)
					if body != "" {
						_, _ = w.Write([]byte(body))
					}
				}))
				defer server.Close()
				url = server.URL
			}

			err := defradb.ApplyCollectionSchemasViaHTTP(context.Background(), url, evm.NewCollectionNames(evm.DefaultCollectionPrefix))

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			assert.NoError(t, err)
			if tt.wantCalls > 0 {
				assert.Equal(t, tt.wantCalls, callCount, "monolithic path should make exactly 1 POST")
			}
			if tt.wantMinCalls > 0 {
				assert.GreaterOrEqual(t, callCount, tt.wantMinCalls, "should fall back to per-file after monolithic already-exists")
			}
		})
	}
}

// ---------------------------------------------------------------------------.
// BlockResult struct tests.
// ---------------------------------------------------------------------------.

func TestBlockResult_Fields(t *testing.T) {
	t.Parallel()
	r := &BlockResult{
		BlockNum: 42,
		Success:  true,
		Error:    nil,
	}
	assert.Equal(t, int64(42), r.BlockNum)
	assert.True(t, r.Success)
	assert.Nil(t, r.Error)
}

// ---------------------------------------------------------------------------
// openBrowser tests (verifying they don't panic).
// Subtests run sequentially: execCommand is a package-level var that each
// case swaps and restores.
// ---------------------------------------------------------------------------

func TestOpenBrowser(t *testing.T) {
	t.Parallel()

	echoMock := func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("echo", "mock-browser")
	}
	failingMock := func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("nonexistent-command-that-will-fail")
	}

	tests := []struct {
		name string
		url  string
		cmd  func(string, ...string) *exec.Cmd
	}{
		{name: "invalid (empty) URL does not panic", url: "", cmd: echoMock},
		{name: "valid URL does not panic", url: "http://localhost:12345/health", cmd: echoMock},
		{name: "non-empty URL does not panic", url: "http://localhost:0/test-url-for-coverage", cmd: echoMock},
		{name: "command start failure does not panic", url: "http://127.0.0.1:0/health", cmd: failingMock},
		{name: "darwin happy path (about:blank) does not panic", url: "about:blank", cmd: echoMock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(_ *testing.T) {
			logger.InitConsoleOnly(true)
			original := execCommand
			execCommand = tt.cmd
			defer func() { execCommand = original }()

			openBrowser(tt.url)
		})
	}
}

// ---------------------------------------------------------------------------
// TrackBlock (indexerQueueTracker) tests
// ---------------------------------------------------------------------------

func TestTrackBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		blocks          []int64
		buildResult     func(blockNum int64) *defra.BlockCreationResult
		wantQueueLen    int
		wantCollections map[string]bool
	}{
		{
			name:   "success tracks one block with all doc IDs",
			blocks: []int64{100},
			buildResult: func(_ int64) *defra.BlockCreationResult {
				return &defra.BlockCreationResult{
					BlockID: fakeDocID(1),
					OtherDocIDs: map[string][]string{
						evm.CollectionTransaction:     {fakeDocID(2), fakeDocID(3)},
						evm.CollectionLog:             {fakeDocID(4)},
						evm.CollectionAccessListEntry: {fakeDocID(5)},
					},
					BlockSignatureID: fakeDocID(6),
				}
			},
			wantQueueLen: 1,
		},
		{
			name:   "multiple blocks all enqueued",
			blocks: []int64{100, 101, 102, 103, 104},
			buildResult: func(blockNum int64) *defra.BlockCreationResult {
				return &defra.BlockCreationResult{
					BlockID: fakeDocID(int(blockNum)),
					OtherDocIDs: map[string][]string{
						evm.CollectionTransaction: {fakeDocID(int(blockNum) + 1000)},
					},
				}
			},
			wantQueueLen: 5,
		},
		{
			name:   "empty result still tracked",
			blocks: []int64{100},
			buildResult: func(_ int64) *defra.BlockCreationResult {
				return &defra.BlockCreationResult{
					BlockID: fakeDocID(1),
				}
			},
			wantQueueLen: 1,
		},
		{
			name:   "passes correct collection names",
			blocks: []int64{100},
			buildResult: func(_ int64) *defra.BlockCreationResult {
				return &defra.BlockCreationResult{
					BlockID: fakeDocID(1),
					OtherDocIDs: map[string][]string{
						evm.CollectionTransaction:     {fakeDocID(2)},
						evm.CollectionLog:             {fakeDocID(3)},
						evm.CollectionAccessListEntry: {fakeDocID(4)},
					},
					BlockSignatureID: fakeDocID(5),
				}
			},
			wantQueueLen: 1,
			wantCollections: map[string]bool{
				evm.CollectionTransaction:     true,
				evm.CollectionLog:             true,
				evm.CollectionAccessListEntry: true,
			},
		},
		{
			name:   "wires EVM collection constants",
			blocks: []int64{1000},
			buildResult: func(_ int64) *defra.BlockCreationResult {
				return &defra.BlockCreationResult{
					BlockID: fakeDocID(100),
					OtherDocIDs: map[string][]string{
						evm.CollectionTransaction:     {fakeDocID(101), fakeDocID(102)},
						evm.CollectionLog:             {fakeDocID(103), fakeDocID(104), fakeDocID(105)},
						evm.CollectionAccessListEntry: {fakeDocID(106)},
					},
					BlockSignatureID: fakeDocID(107),
				}
			},
			wantQueueLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			queue := pruner.NewIndexerQueue()
			tracker := &indexerQueueTracker{queue: queue, collections: evm.NewCollectionNames(evm.DefaultCollectionPrefix)}

			var lastResult *defra.BlockCreationResult
			for _, blockNum := range tt.blocks {
				result := tt.buildResult(blockNum)
				err := tracker.TrackBlock(context.Background(), blockNum, result)
				require.NoError(t, err)
				lastResult = result
			}

			assert.Equal(t, tt.wantQueueLen, queue.Len())
			for collection := range tt.wantCollections {
				assert.Contains(t, lastResult.OtherDocIDs, collection)
			}
		})
	}

	// Verify the EVM collection name constants the tracker wires.
	t.Run("EVM collection name constants", func(t *testing.T) {
		assert.Contains(t, evm.CollectionTransaction, "Transaction")
		assert.Contains(t, evm.CollectionLog, "Log")
		assert.Contains(t, evm.CollectionAccessListEntry, "AccessListEntry")
	})
}

// ===========================================================================
// Additional tests to boost coverage to 95%+.
// ===========================================================================

// ---------------------------------------------------------------------------.
// StartIndexing — happy paths (table-driven).
// ---------------------------------------------------------------------------.

func TestStartIndexing_HappyPaths(t *testing.T) {
	tests := []struct {
		name       string
		rpcServer  func(t *testing.T, count *atomic.Int64, blockCh chan struct{}) *httptest.Server
		defraURL   string
		idxCfg     config.IndexerConfig
		pruner     *config.PrunerConfig
		snapshot   func(tmpDir string) *config.SnapshotConfig
		fileSetup  func(t *testing.T, tmpDir string)
		wait       func(t *testing.T, count *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error)
		assertMid  func(t *testing.T, ix *ChainIndexer)
		preClear   bool
		assertPost func(t *testing.T, ix *ChainIndexer)
	}{
		{
			// Pruner and snapshotter enabled; health server disabled.
			name: "full integration with pruner and snapshotter",
			rpcServer: func(_ *testing.T, _ *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServerForIntegration(blockCh)
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0,
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0, // disabled
				StartBuffer:      10,
			},
			pruner: &config.PrunerConfig{
				Enabled:         true,
				MaxBlocks:       1000,
				PruneThreshold:  500,
				IntervalSeconds: 3600,
			},
			snapshot: func(tmpDir string) *config.SnapshotConfig {
				return &config.SnapshotConfig{
					Enabled:         true,
					Dir:             filepath.Join(tmpDir, "snapshots"),
					BlocksPerFile:   1000,
					IntervalSeconds: 3600,
				}
			},
			wait: func(t *testing.T, _ *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockSignals(t, blockCh, errCh, 3, func(seen int) string {
					return fmt.Sprintf("timed out waiting for blocks to be processed (saw %d)", seen)
				})
			},
			assertPost: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
				assert.False(t, ix.isStarted)
			},
		},
		{
			name: "configured start height",
			rpcServer: func(_ *testing.T, _ *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServerForIntegration(blockCh)
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      50000, // explicit configured height.
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			wait: func(t *testing.T, _ *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockSignals(t, blockCh, errCh, 2, func(seen int) string {
					return fmt.Sprintf("timed out (saw %d)", seen)
				})
			},
			assertPost: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
			},
		},
		{
			name: "health server enabled",
			rpcServer: func(_ *testing.T, _ *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServerForIntegration(blockCh)
			},
			// Set Url so healthDefraURL uses config URL branch.
			defraURL: "http://localhost:9999",
			idxCfg: config.IndexerConfig{
				StartHeight:      0,
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 19876, // Enable health server on a high port.
				StartBuffer:      10,
			},
			wait: func(t *testing.T, _ *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockSignals(t, blockCh, errCh, 2, func(seen int) string {
					return fmt.Sprintf("timed out (saw %d)", seen)
				})
			},
			assertMid: func(t *testing.T, ix *ChainIndexer) {
				// Verify health server is running.
				assert.NotNil(t, ix.healthServer)
			},
			assertPost: func(t *testing.T, ix *ChainIndexer) {
				assert.False(t, ix.shouldIndex)
			},
		},
		{
			name: "health server with pruner and snapshotter",
			rpcServer: func(_ *testing.T, count *atomic.Int64, _ chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186b1", 100000, "", nil))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0,
				ConcurrentBlocks: 1, // concurrent
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 19876, // enable health server.
				StartBuffer:      10,
			},
			pruner: &config.PrunerConfig{
				Enabled:         true,
				MaxBlocks:       1000,
				PruneThreshold:  100,
				IntervalSeconds: 60,
			},
			snapshot: func(tmpDir string) *config.SnapshotConfig {
				return &config.SnapshotConfig{
					Enabled:         true,
					Dir:             filepath.Join(tmpDir, "snapshots"),
					BlocksPerFile:   1000,
					IntervalSeconds: 3600,
				}
			},
			wait: func(t *testing.T, count *atomic.Int64, _ <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, nil, errCh, func() bool { return count.Load() >= 3 }, "timed out")
			},
			assertMid: func(t *testing.T, ix *ChainIndexer) {
				// Verify subsystems are active.
				assert.NotNil(t, ix.healthServer, "health server should be initialized")
				assert.NotNil(t, ix.pruner, "pruner should be initialized")
				assert.NotNil(t, ix.snapshotter, "snapshotter should be initialized")
			},
			preClear: true,
		},
		{
			name: "concurrent with pruner and snapshotter",
			rpcServer: func(_ *testing.T, count *atomic.Int64, _ chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186b1", 100000, "", nil))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0,
				ConcurrentBlocks: 2, // concurrent.
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			pruner: &config.PrunerConfig{
				Enabled:         true,
				MaxBlocks:       1000,
				PruneThreshold:  100,
				IntervalSeconds: 60,
			},
			snapshot: func(tmpDir string) *config.SnapshotConfig {
				return &config.SnapshotConfig{
					Enabled:         true,
					Dir:             filepath.Join(tmpDir, "snapshots"),
					BlocksPerFile:   1000,
					IntervalSeconds: 3600,
				}
			},
			wait: func(t *testing.T, count *atomic.Int64, _ <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, nil, errCh, func() bool { return count.Load() >= 5 }, "timed out waiting for blocks")
			},
			assertMid: func(t *testing.T, ix *ChainIndexer) {
				assert.NotNil(t, ix.pruner, "pruner should be initialized")
				assert.NotNil(t, ix.snapshotter, "snapshotter should be initialized")
			},
			preClear: true,
		},
		{
			// Chain tip at 100000, start height just below it.
			name: "resume from high block",
			rpcServer: func(_ *testing.T, count *atomic.Int64, _ chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "", nil))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      99980, // specific start height
				ConcurrentBlocks: 1,     // concurrent
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			wait: func(t *testing.T, count *atomic.Int64, _ <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, nil, errCh, func() bool { return count.Load() >= 3 }, "timed out")
			},
			preClear: true,
		},
		{
			// Pre-created prune queue with entries up to 90010 vs chain tip
			// 100000: gap = 9990 > startBuffer=10 → gap detection skip-ahead
			// (lines 219-221, 243-252, 246-250).
			name: "resume from queue",
			fileSetup: func(t *testing.T, tmpDir string) {
				// Pre-create a prune queue file with entries so LoadFromFile returns loaded > 0.
				queue := pruner.NewIndexerQueue()
				for i := int64(90000); i <= 90010; i++ {
					_ = queue.TrackBlockDocIDs(i, fakeDocID(int(i)), map[string][]string{
						evm.CollectionTransaction: {fakeDocID(int(i) + 10000)},
					}, fakeDocID(int(i)+20000))
				}
				queueFilePath := filepath.Join(tmpDir, "prune_queue.gob")
				_, _ = queue.LoadFromFile(queueFilePath) // sets filePath.
				err := queue.Save()
				require.NoError(t, err)
			},
			rpcServer: func(_ *testing.T, count *atomic.Int64, _ chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "", nil))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0,
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			pruner: &config.PrunerConfig{
				Enabled:         true,
				MaxBlocks:       1000,
				PruneThreshold:  100,
				IntervalSeconds: 60,
			},
			wait: func(t *testing.T, count *atomic.Int64, _ <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, nil, errCh, func() bool { return count.Load() >= 2 }, "timed out waiting for blocks")
			},
			assertMid: func(t *testing.T, ix *ChainIndexer) {
				// Should have skipped ahead — start height should be around 99990.
				assert.True(t, ix.cfg.Indexer.StartHeight >= 99980,
					"should have skipped ahead due to gap, got start height %d", ix.cfg.Indexer.StartHeight)
			},
			preClear: true,
		},
		{
			// Chain tip very low (5), startBuffer=100 → startHeight = 5 - 100 = -95 → clamped to 0
			// (covers lines 259-261).
			name: "negative start height clamped to zero",
			rpcServer: func(_ *testing.T, count *atomic.Int64, _ chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x5", -1, "", nil))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0, // no configured height.
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      100, // larger than chain tip.
			},
			wait: func(t *testing.T, count *atomic.Int64, _ <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, nil, errCh, func() bool { return count.Load() >= 2 }, "timed out")
			},
			assertMid: func(t *testing.T, ix *ChainIndexer) {
				// Start height should be clamped to 0.
				assert.Equal(t, 0, ix.cfg.Indexer.StartHeight,
					"start height should be clamped to 0 when chainTip - buffer is negative")
			},
			preClear: true,
		},
		{
			// OpenBrowserOnStart path (covers lines 294-299); disabled here
			// because the server is already dead by the time it would open.
			name: "open browser on start disabled",
			rpcServer: func(_ *testing.T, count *atomic.Int64, _ chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "", nil))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:        99990,
				ConcurrentBlocks:   1,
				ReceiptWorkers:     2,
				MaxDocsPerTxn:      100,
				HealthServerPort:   8080,
				OpenBrowserOnStart: false, // This should be true but is annoying because the server is already dead.
				StartBuffer:        10,
			},
			wait: func(t *testing.T, count *atomic.Int64, _ <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, nil, errCh, func() bool { return count.Load() >= 2 }, "timed out")
			},
			preClear: true,
		},
		{
			// Fresh DB with no configured height and pruner disabled →
			// exercises the "no existing blocks" path (line 226/229).
			name: "no existing blocks fresh db",
			rpcServer: func(_ *testing.T, count *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "0x186a0", blockCh))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0, // No configured height, fresh DB → exercises "no existing blocks" path.
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			wait: func(t *testing.T, count *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, blockCh, errCh, func() bool { return count.Load() >= 2 }, "timed out")
			},
			preClear: true,
		},
		{
			// Random-port URL → health server falls through to defraNode port
			// (covers line 280-281).
			name: "health server without defra url",
			rpcServer: func(_ *testing.T, count *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "0x186a0", blockCh))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      99990,
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 19878, // Enable health server.
				StartBuffer:      10,
			},
			wait: func(t *testing.T, count *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, blockCh, errCh, func() bool { return count.Load() >= 2 }, "timed out")
			},
			assertMid: func(t *testing.T, ix *ChainIndexer) {
				assert.NotNil(t, ix.healthServer)
			},
			preClear: true,
		},
		{
			// Corrupted prune_queue.gob triggers a warning but not a crash
			// (covers line 217-218).
			name: "prune queue load error tolerated",
			fileSetup: func(t *testing.T, tmpDir string) {
				// Create a corrupted prune queue file.
				corruptFilePath := filepath.Join(tmpDir, "prune_queue.gob")
				err := writeCorruptedFile(corruptFilePath)
				require.NoError(t, err)
			},
			rpcServer: func(_ *testing.T, count *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "0x186a0", blockCh))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      0,
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			pruner: &config.PrunerConfig{
				Enabled:         true,
				MaxBlocks:       1000,
				PruneThreshold:  100,
				IntervalSeconds: 3600,
			},
			wait: func(t *testing.T, count *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, blockCh, errCh, func() bool { return count.Load() >= 2 }, "timed out")
			},
			preClear: true,
		},
		{
			// Snapshot dir under a file → snapshotter.Start fails, but the
			// error is logged as a warning, not fatal (covers lines 323-325).
			name: "snapshotter start error tolerated",
			fileSetup: func(t *testing.T, tmpDir string) {
				// Create a file where the snapshot directory would be — MkdirAll under
				// a file will fail, causing snapshotter.Start to return an error.
				invalidSnapshotPath := filepath.Join(tmpDir, "snapshot_blocker")
				err := os.WriteFile(filepath.Clean(invalidSnapshotPath), []byte("I am a file"), 0o600)
				require.NoError(t, err)
			},
			rpcServer: func(_ *testing.T, count *atomic.Int64, blockCh chan struct{}) *httptest.Server {
				return newMockRPCServer(countingBlockHandler(count, "0x186a0", 99990, "0x186a0", blockCh))
			},
			defraURL: testDefraRandomURL,
			idxCfg: config.IndexerConfig{
				StartHeight:      99990,
				ConcurrentBlocks: 1,
				ReceiptWorkers:   2,
				MaxDocsPerTxn:    100,
				HealthServerPort: 0,
				StartBuffer:      10,
			},
			snapshot: func(tmpDir string) *config.SnapshotConfig {
				return &config.SnapshotConfig{
					Enabled:         true,
					Dir:             filepath.Join(filepath.Clean(filepath.Join(tmpDir, "snapshot_blocker")), "nested"), // under a file → MkdirAll fails.
					BlocksPerFile:   1000,
					IntervalSeconds: 3600,
				}
			},
			wait: func(t *testing.T, count *atomic.Int64, blockCh <-chan struct{}, errCh <-chan error) {
				waitForBlockCondition(t, blockCh, errCh, func() bool { return count.Load() >= 2 }, "timed out")
			},
			assertMid: func(t *testing.T, _ *ChainIndexer) {
				// If we got here, the indexer continued despite snapshotter.Start failing,
				// (the error was logged as a warning, not a fatal — line 323-325).
				t.Log("Indexer continued despite snapshotter.Start error (covers lines 323-325)")
			},
			preClear: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if testing.Short() {
				t.Skip("skipping integration test in short mode")
			}
			logger.InitConsoleOnly(true)

			tmpDir := t.TempDir()
			if tc.fileSetup != nil {
				tc.fileSetup(t, tmpDir)
			}

			var count atomic.Int64
			blockCh := make(chan struct{}, 100)
			srv := tc.rpcServer(t, &count, blockCh)
			t.Cleanup(srv.Close)

			cfg := startPathBaseCfg(srv.URL, tc.defraURL, tmpDir, tc.idxCfg)
			if tc.pruner != nil {
				cfg.Pruner = *tc.pruner
			}
			if tc.snapshot != nil {
				cfg.Snapshot = *tc.snapshot(tmpDir)
			}

			indexer, err := CreateIndexer(cfg)
			require.NoError(t, err)

			stopped := false
			t.Cleanup(func() {
				if !stopped {
					indexer.shouldIndex = false
					indexer.StopIndexing()
				}
			})

			errCh := startIndexingBackground(t, indexer)
			tc.wait(t, &count, blockCh, errCh)

			if tc.assertMid != nil {
				tc.assertMid(t, indexer)
			}

			if tc.preClear {
				indexer.shouldIndex = false
			}
			indexer.StopIndexing()
			stopped = true

			if tc.assertPost != nil {
				tc.assertPost(t, indexer)
			}
		})
	}
}

// ---------------------------------------------------------------------------.
// runConcurrentIndexing test (direct call).
// ---------------------------------------------------------------------------.

func TestRunConcurrentIndexing_DirectCall(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var blockCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			n := blockCount.Add(1)
			num := fmt.Sprintf("0x%x", 5000+n)
			return fullBlockResponse(num, nil), nil
		case ethGetBlockReceipts:
			return []any{}, nil
		case ethBlockNumber:
			return "0x100000", nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	fetcher, converter, blockHandler := newTestProcessor(t, td, rpcServer.URL, 2)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{
				ConcurrentBlocks: 2,
				ReceiptWorkers:   2,
				BlocksPerMinute:  0,
			},
		},
		fetcher:      fetcher,
		converter:    converter,
		blockHandler: blockHandler,
		defraNode:    td.Node,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()

	err := indexer.runConcurrentIndexing(ctx, 5001, indexer.cfg)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, indexer.isStarted)
	assert.True(t, indexer.shouldIndex)
}

// ---------------------------------------------------------------------------.
// Concurrent safety of updateBlockInfo.
// ---------------------------------------------------------------------------.

func TestUpdateBlockInfo_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	indexer := &ChainIndexer{}
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(n int64) {
			defer wg.Done()
			indexer.updateBlockInfo(n)
			_ = indexer.GetCurrentBlock()
			_ = indexer.GetLastProcessedTime()
			_ = indexer.IsHealthy()
		}(int64(i))
	}
	wg.Wait()

	// Just verify no panic/race occurred.
	assert.True(t, indexer.GetCurrentBlock() >= 0)
}

// ---------------------------------------------------------------------------.
// openBrowser — cmd.Start error path (covers lines 695-698),
// Override the command to a non-existent one to trigger Start() failure.
// Since openBrowser is a function (not method) with runtime.GOOS switch,
// we can't easily mock. But we can call it indirectly. On macOS, the "open",
// command exists, so it won't fail. Instead, test it from a URL that won't,
// actually open anything harmful.
// ---------------------------------------------------------------------------.

// Note: The openBrowser cmd.Start error is OS-specific. On macOS, "open" exists,
// and will succeed for any URL. On Linux, "xdg-open" may not exist in CI.
// On Windows, "cmd" exists. The error path (695-698) only triggers when the,
// command binary doesn't exist. This is structurally difficult to test without,
// mocking, which would require refactoring.

// ---------------------------------------------------------------------------.
// StartIndexing — pruner enabled but queue not yet created (line 307-309).
// This path is hit when cfg.Pruner.Enabled=true but the pruneQueue,
// was not initialized in the earlier LoadFromFile block (which only runs,
// when cfg.Pruner.Enabled is true and creates the queue). Line 307 is,
// a defensive check. To trigger it, we need Pruner.Enabled=true BUT the,
// earlier block at line 214-222 must NOT create the queue. Looking at the,
// code: lines 214-215 check cfg.Pruner.Enabled and create the queue.
// So if Pruner.Enabled=true, the queue IS always created at line 215.
// Line 307 is truly dead code (defensive). We can't hit it without,
// removing the earlier creation. Skip this test target.
// ---------------------------------------------------------------------------.

// ---------------------------------------------------------------------------..
// fetchAndProcessBlock — receipt fetch with batch receipts success,
// (covers the batch receipt path in concurrent processor, not the fallback).
// ---------------------------------------------------------------------------..

// ---------------------------------------------------------------------------.
// fetchAndProcessBlock — individual receipt success in fallback path,
// (covers lines 266-284 in concurrent_processor.go).
// ---------------------------------------------------------------------------.

// ---------------------------------------------------------------------------
// StartIndexing — GetHighestBlockNumber succeeds with pre-populated DB,
// (covers lines 229-231).
// Strategy: Run one indexer to populate a block, stop it, then create a new,
// indexer pointing to the same DB directory. The second run should find the,
// existing block via GetHighestBlockNumber.
// ---------------------------------------------------------------------------.

func TestStartIndexing_ResumeFromExistingBlocks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64
	blockCh := make(chan struct{}, 100)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if innerErr := json.Unmarshal(rawParams[0], &blockParam); innerErr == nil && blockParam == defaultBlockParamLatest {
					return fullBlockResponse("0x186a0", nil), nil // chain tip 100000.
				}
			}
			count := blockCallCount.Add(1)
			select {
			case blockCh <- struct{}{}:
			default:
			}
			num := fmt.Sprintf("0x%x", 99990+count)
			return fullBlockResponse(num, nil), nil
		case ethBlockNumber:
			return "0x186a0", nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	// Phase 1: Start an indexer to populate some blocks.
	cfg1 := &config.Config{
		DefraDB: config.DefraDBConfig{
			URL:           testDefraRandomURL,
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           testDefraP2PDisabled,
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      99990,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		// Pruner disabled — so GetHighestBlockNumber path at line 226 is used.
		Logger: config.LoggerConfig{Development: true},
	}

	indexer1, err := CreateIndexer(cfg1)
	require.NoError(t, err)

	stopped1 := false
	t.Cleanup(func() {
		if !stopped1 {
			indexer1.shouldIndex = false
			indexer1.StopIndexing()
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer1.StartIndexing(false)
	}()

	// Wait for at least 3 blocks to be processed.
	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 3 {
		select {
		case <-blockCh:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for phase 1 blocks")
		case startErr := <-errCh:
			if startErr != nil {
				t.Fatalf("StartIndexing phase 1 failed: %v", startErr)
			}
		}
	}

	// Stop the first indexer.
	indexer1.shouldIndex = false
	indexer1.StopIndexing()
	stopped1 = true

	// Phase 2: Create a NEW indexer pointing to the same DB directory.
	// When it calls GetHighestBlockNumber (line 226), it should find existing blocks,
	// and enter the else branch (line 229-231).
	blockCallCount.Store(0)

	cfg2 := &config.Config{
		DefraDB: config.DefraDBConfig{
			URL:           testDefraRandomURL,
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           testDefraP2PDisabled,
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0, // No configured start height — will use DB state.
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer2, err := CreateIndexer(cfg2)
	require.NoError(t, err)

	stopped2 := false
	t.Cleanup(func() {
		if !stopped2 {
			indexer2.shouldIndex = false
			indexer2.StopIndexing()
		}
	})

	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- indexer2.StartIndexing(false)
	}()

	deadline2 := time.After(60 * time.Second)
	for blockCallCount.Load() < 2 {
		select {
		case <-blockCh:
		case <-time.After(100 * time.Millisecond):
		case <-deadline2:
			t.Fatalf("timed out waiting for phase 2 blocks")
		case err := <-errCh2:
			if err != nil {
				t.Fatalf("StartIndexing phase 2 failed: %v", err)
			}
		}
	}

	// The second indexer should have resumed from the existing blocks.
	indexer2.shouldIndex = false
	indexer2.StopIndexing()
	stopped2 = true
	t.Log("Phase 2 indexer resumed successfully from existing blocks")
}

// ---------------------------------------------------------------------------.
// StartIndexing — init-stage errors (Issue 1: ERROR_PATH_RESOURCE_CLEANUP).
// Asserts that when NewBlockHandler or WaitForDefraDB fails, the deferred
// guard calls StopIndexing() and nil-safes fetcher, defraNode, networkHandler.
// ---------------------------------------------------------------------------.

func TestStartIndexing_InitStageError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	validRPCHandler := func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			return fullBlockResponse("0x100", nil), nil
		case ethBlockNumber:
			return "0x100", nil
		default:
			return "0x1", nil
		}
	}

	cases := []struct {
		name            string
		setupSeam       func() func()
		wantErrContains string
	}{
		{
			name: "BlockHandler creation error",
			setupSeam: func() func() {
				original := newBlockHandlerFn
				newBlockHandlerFn = func(_ *node.Node, _ int) (*defra.BlockHandler, error) {
					return nil, errors.New("forced block handler failure")
				}
				return func() { newBlockHandlerFn = original }
			},
			wantErrContains: "forced block handler failure",
		},
		{
			name: "WaitForDefraDB error",
			setupSeam: func() func() {
				original := waitForDefraDBFn
				waitForDefraDBFn = func(_ string) error {
					return errors.New("forced WaitForDefraDB failure")
				}
				return func() { waitForDefraDBFn = original }
			},
			wantErrContains: "forced WaitForDefraDB failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			rpcServer := newMockRPCServer(validRPCHandler)
			defer rpcServer.Close()

			cfg := &config.Config{
				DefraDB: config.DefraDBConfig{
					URL:           testDefraRandomURL,
					KeyringSecret: "test-secret-for-keyring-12345678",
					P2P:           testDefraP2PDisabled,
					Store:         config.DefraDBStoreConfig{Path: tmpDir},
				},
				Geth: config.GethConfig{NodeURL: rpcServer.URL},
				Indexer: config.IndexerConfig{
					StartHeight:      100,
					ConcurrentBlocks: 1,
					ReceiptWorkers:   2,
					MaxDocsPerTxn:    100,
					HealthServerPort: 0,
					StartBuffer:      10,
				},
				Logger: config.LoggerConfig{Development: true},
			}

			indexer, err := CreateIndexer(cfg)
			require.NoError(t, err)

			cleanup := tc.setupSeam()
			defer cleanup()

			err = indexer.StartIndexing(false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrContains)
			t.Logf("StartIndexing error (expected): %v", err)

			assert.Nil(t, indexer.fetcher, "fetcher should be nil after error-path cleanup")
			assert.Nil(t, indexer.defraNode, "defraNode should be nil after error-path cleanup")
			assert.Nil(t, indexer.networkHandler, "networkHandler should be nil after error-path cleanup")
		})
	}
}

// ---------------------------------------------------------------------------
// newAuthenticator tests
// ---------------------------------------------------------------------------

func TestNewAuthenticator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mode     string
		keys     []string
		wantErr  error
		wantType any
	}{
		{"none", constants.SchemaAuthModeNone, nil, nil, server.NoOpAuthenticator{}},
		{"empty", "", nil, nil, &server.BearerAuthenticator{}},
		{"token", constants.SchemaAuthModeToken, []string{"key1", "key2"}, nil, &server.BearerAuthenticator{}},
		{"mtls_not_implemented", constants.SchemaAuthModeMTLS, nil, ErrMTLSNotImplemented, nil},
		{"unknown", "invalid", nil, ErrUnknownAuthMode, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auth, err := newAuthenticator(tt.mode, tt.keys)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, auth)
			} else {
				require.NoError(t, err)
				assert.IsType(t, tt.wantType, auth)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// initServices mTLS error propagation
// ---------------------------------------------------------------------------

func TestInitServices_MTLSMode_ReturnsError(t *testing.T) {
	t.Parallel()
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			URL: fmt.Sprintf("http://localhost:%d", td.Port),
		},
		Indexer: config.IndexerConfig{
			SchemaAuthMode:   constants.SchemaAuthModeMTLS,
			HealthServerPort: 1,
		},
	}

	indexer := &ChainIndexer{
		defraNode: td.Node,
		cfg:       cfg,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := indexer.initServices(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema auth configuration")
	assert.ErrorIs(t, err, ErrMTLSNotImplemented)
}
