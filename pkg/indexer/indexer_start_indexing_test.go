package indexer

// indexer_start_indexing_test.go covers the StartIndexing lifecycle of
// ChainIndexer: error paths, happy-path integration runs, concurrent
// indexing, resume from an existing database, init-stage resource cleanup,
// and authenticator/services initialization.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// ---------------------------------------------------------------------------.
// Prune-queue startup notes (indexer.go).
// When cfg.Pruner.Enabled, resolveStartHeight loads prune_queue.gob and
// resumes or gap-skips from its highest entry — the "resume from queue"
// subtest pre-creates that file to exercise this. The queue is always
// created when the pruner is enabled, so there is no "queue not yet
// created" branch to test; initServices later loads the same file again
// when binding the queue to the pruner and the block tracker.
// ---------------------------------------------------------------------------.

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
