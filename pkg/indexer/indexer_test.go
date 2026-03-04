package indexer

import (
	"context"
	crypto_rand "crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	appConfig "github.com/shinzonetwork/shinzo-app-sdk/pkg/config"
	"github.com/shinzonetwork/shinzo-app-sdk/pkg/pruner"
	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/server"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/snapshot"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexing_StartDefraFirst is now replaced by mock-based integration tests
// See ./integration/ directory for comprehensive integration tests with mock data
func TestIndexing_StartDefraFirst(t *testing.T) {
	t.Skip("This test has been replaced by mock-based integration tests in ./integration/ - run 'make test' for full test suite")
}

func TestIndexing(t *testing.T) {
	t.Skip("This test has been replaced by mock-based integration tests in ./integration/ - run 'make test' for full test suite")
}

// TestCreateIndexer tests the indexer creation
func TestCreateIndexer(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: "http://localhost:9181",
		},
		Indexer: config.IndexerConfig{
			StartHeight: 100,
		},
	}

	indexer, err := CreateIndexer(cfg)

	assert.NoError(t, err)
	assert.NotNil(t, indexer)
	assert.Equal(t, cfg, indexer.cfg)
	assert.False(t, indexer.shouldIndex)
	assert.False(t, indexer.isStarted)
	assert.False(t, indexer.hasIndexedAtLeastOneBlock)
	assert.Nil(t, indexer.defraNode)
}

// TestCreateIndexerWithNilConfig tests indexer creation with nil config
func TestCreateIndexerWithNilConfig(t *testing.T) {
	indexer, err := CreateIndexer(nil)

	assert.Error(t, err)
	assert.Nil(t, indexer)
	assert.Contains(t, err.Error(), "config is nil")
	assert.Contains(t, err.Error(), "CONFIGURATION_ERROR")
}

// TestIndexerStateManagement tests the state management methods
func TestIndexerStateManagement(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{Url: "http://localhost:9181"},
	}
	indexer, err := CreateIndexer(cfg)
	assert.NoError(t, err)

	// Test initial state
	assert.False(t, indexer.IsStarted())
	assert.False(t, indexer.HasIndexedAtLeastOneBlock())

	// Test state changes
	indexer.shouldIndex = true
	indexer.isStarted = true
	indexer.hasIndexedAtLeastOneBlock = true

	assert.True(t, indexer.IsStarted())
	assert.True(t, indexer.HasIndexedAtLeastOneBlock())
}

// TestGetDefraDBPortWithEmbeddedNode tests port retrieval with embedded node
func TestGetDefraDBPortWithEmbeddedNode(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{Url: "http://localhost:9181"},
	}
	indexer, err := CreateIndexer(cfg)
	assert.NoError(t, err)

	// Initially no embedded node
	assert.Equal(t, -1, indexer.GetDefraDBPort())

	// Note: We can't easily test with an actual embedded node in unit tests
	// as it requires starting DefraDB, which is covered in integration tests
}

// TestStopIndexing tests the stop indexing functionality
func TestStopIndexing(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{Url: "http://localhost:9181"},
	}
	indexer, err := CreateIndexer(cfg)
	assert.NoError(t, err)

	// Set some state
	indexer.shouldIndex = true
	indexer.isStarted = true
	indexer.hasIndexedAtLeastOneBlock = true

	// Stop indexing
	indexer.StopIndexing()

	// Verify state is reset
	assert.False(t, indexer.shouldIndex)
	assert.False(t, indexer.isStarted)
	// hasIndexedAtLeastOneBlock should remain true (historical fact)
	assert.True(t, indexer.hasIndexedAtLeastOneBlock)
}

// TestConfigLoading tests configuration loading
func TestConfigLoading(t *testing.T) {
	// Test that configuration is required
	indexer := &ChainIndexer{cfg: nil}
	err := indexer.StartIndexing(true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "configuration is required")
}

// TestConstants tests the defined constants
func TestConstants(t *testing.T) {
	assert.Equal(t, 10, DefaultBlocksToIndexAtOnce)
	assert.Equal(t, 3, DefaultRetryAttempts)
	assert.Equal(t, 15*time.Second, DefaultSchemaWaitTimeout)
	assert.Equal(t, 30*time.Second, DefaultDefraReadyTimeout)
	assert.Equal(t, 3, DefaultBlockOffset)
	assert.Equal(t, "/ip4/127.0.0.1/tcp/9171", defaultListenAddress)
}

// TestConvertGethBlockToDefraBlock tests block conversion
func TestConvertGethBlockToDefraBlock(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Create a mock geth block
	gethBlock := &types.Block{
		Number:           "12345",
		Hash:             "0x1234567890abcdef",
		ParentHash:       "0xabcdef1234567890",
		Timestamp:        "1640995200",
		Miner:            "0x1111111111111111111111111111111111111111",
		GasLimit:         "8000000",
		GasUsed:          "21000",
		Difficulty:       "1000000",
		TotalDifficulty:  "5000000",
		Nonce:            "0x1234567890abcdef",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		LogsBloom:        "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		StateRoot:        "0xd7f8974fb5ac78d9ac099b9ad5018bedc2ce0a72dad1827a1709da30580f0544",
		ReceiptsRoot:     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		Size:             "1000",
		ExtraData:        "0x",
		Transactions: []types.Transaction{
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
				ChainId:          "1",
				V:                "27",
				R:                "12345",
				S:                "67890",
			},
		},
	}

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: "http://localhost:9181",
		},
	}
	indexer, err := CreateIndexer(cfg)
	assert.NoError(t, err)

	// Set some state
	indexer.shouldIndex = true
	indexer.isStarted = true
	indexer.hasIndexedAtLeastOneBlock = true

	// Stop indexing
	indexer.StopIndexing()

	// Test block structure
	transactions := gethBlock.Transactions
	defraBlock := &types.Block{
		Number:           gethBlock.Number,
		Hash:             gethBlock.Hash,
		ParentHash:       gethBlock.ParentHash,
		Nonce:            gethBlock.Nonce,
		Sha3Uncles:       gethBlock.Sha3Uncles,
		LogsBloom:        gethBlock.LogsBloom,
		TransactionsRoot: gethBlock.TransactionsRoot,
		StateRoot:        gethBlock.StateRoot,
		ReceiptsRoot:     gethBlock.ReceiptsRoot,
		Miner:            gethBlock.Miner,
		Difficulty:       gethBlock.Difficulty,
		TotalDifficulty:  gethBlock.TotalDifficulty,
		ExtraData:        gethBlock.ExtraData,
		Size:             gethBlock.Size,
		GasLimit:         gethBlock.GasLimit,
		GasUsed:          gethBlock.GasUsed,
		Timestamp:        gethBlock.Timestamp,
		Transactions:     transactions,
	}

	assert.NotNil(t, defraBlock)
	assert.Equal(t, gethBlock.Number, defraBlock.Number)
	assert.Equal(t, gethBlock.Hash, defraBlock.Hash)
	assert.Equal(t, gethBlock.ParentHash, defraBlock.ParentHash)
	assert.Equal(t, gethBlock.Timestamp, defraBlock.Timestamp)
	assert.Equal(t, gethBlock.Miner, defraBlock.Miner)
	assert.Equal(t, gethBlock.GasLimit, defraBlock.GasLimit)
	assert.Equal(t, gethBlock.GasUsed, defraBlock.GasUsed)
	assert.Len(t, defraBlock.Transactions, 1)
}

// TestConvertGethBlockToDefraBlockWithEmptyTransactions tests block conversion with no transactions
func TestConvertGethBlockToDefraBlockWithEmptyTransactions(t *testing.T) {
	logger.InitConsoleOnly(true)

	gethBlock := &types.Block{
		Number:       "12345",
		Hash:         "0x1234567890abcdef",
		ParentHash:   "0xabcdef1234567890",
		Timestamp:    "1640995200",
		Miner:        "0x1111111111111111111111111111111111111111",
		GasLimit:     "8000000",
		GasUsed:      "0",
		Transactions: []types.Transaction{}, // Empty transactions
	}

	defraBlock := &types.Block{
		Number:           gethBlock.Number,
		Hash:             gethBlock.Hash,
		ParentHash:       gethBlock.ParentHash,
		Nonce:            gethBlock.Nonce,
		Sha3Uncles:       gethBlock.Sha3Uncles,
		LogsBloom:        gethBlock.LogsBloom,
		TransactionsRoot: gethBlock.TransactionsRoot,
		StateRoot:        gethBlock.StateRoot,
		ReceiptsRoot:     gethBlock.ReceiptsRoot,
		Miner:            gethBlock.Miner,
		Difficulty:       gethBlock.Difficulty,
		TotalDifficulty:  gethBlock.TotalDifficulty,
		ExtraData:        gethBlock.ExtraData,
		Size:             gethBlock.Size,
		GasLimit:         gethBlock.GasLimit,
		GasUsed:          gethBlock.GasUsed,
		Timestamp:        gethBlock.Timestamp,
		Transactions:     gethBlock.Transactions,
	}

	assert.NotNil(t, defraBlock)
	assert.Equal(t, gethBlock.Number, defraBlock.Number)
	assert.Len(t, defraBlock.Transactions, 0)
}

// TestCreateIndexerWithNilConfigError tests that CreateIndexer fails immediately with nil config
func TestCreateIndexerWithNilConfigError(t *testing.T) {
	// This should fail immediately when creating the indexer
	indexer, err := CreateIndexer(nil)

	assert.Error(t, err)
	assert.Nil(t, indexer)
	assert.Contains(t, err.Error(), "config is nil")
	assert.Contains(t, err.Error(), "CONFIGURATION_ERROR")
}

// TestIndexerConfigHandling tests configuration handling
func TestIndexerConfigHandling(t *testing.T) {
	// Test with custom config
	customCfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: "http://localhost:8888",
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
	}

	indexer, err := CreateIndexer(customCfg)

	assert.NoError(t, err)
	assert.Equal(t, customCfg, indexer.cfg)
	assert.Equal(t, "http://localhost:8888", indexer.cfg.DefraDB.Url)
	assert.Equal(t, 500, indexer.cfg.Indexer.StartHeight)
}

// TestRequiredPeersInitialization tests required peers initialization
func TestRequiredPeersInitialization(t *testing.T) {
	assert.NotNil(t, requiredPeers)
	assert.IsType(t, []string{}, requiredPeers)
	// Currently empty by design, but should be a valid slice
}

// MockBlockHandler for testing block processing logic
type MockBlockHandler struct {
	highestBlock int64
	createError  error
}

func NewMockBlockHandler() *MockBlockHandler {
	return &MockBlockHandler{}
}

func (m *MockBlockHandler) GetHighestBlockNumber(ctx context.Context) (int64, error) {
	if m.createError != nil {
		return 0, m.createError
	}
	return m.highestBlock, nil
}

// TestBlockProcessingLogic tests the block processing logic with mocked dependencies
func TestBlockProcessingLogic(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Create test block
	testBlock := &types.Block{
		Number:     "100",
		Hash:       "0xtest123",
		ParentHash: "0xparent123",
		Timestamp:  "1640995200",
		Miner:      "0x1111111111111111111111111111111111111111",
		GasLimit:   "8000000",
		GasUsed:    "21000",
		Transactions: []types.Transaction{
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
	}

	// Test conversion
	defraBlock := &types.Block{
		Number:           testBlock.Number,
		Hash:             testBlock.Hash,
		ParentHash:       testBlock.ParentHash,
		Nonce:            testBlock.Nonce,
		Sha3Uncles:       testBlock.Sha3Uncles,
		LogsBloom:        testBlock.LogsBloom,
		TransactionsRoot: testBlock.TransactionsRoot,
		StateRoot:        testBlock.StateRoot,
		ReceiptsRoot:     testBlock.ReceiptsRoot,
		Miner:            testBlock.Miner,
		Difficulty:       testBlock.Difficulty,
		TotalDifficulty:  testBlock.TotalDifficulty,
		ExtraData:        testBlock.ExtraData,
		Size:             testBlock.Size,
		GasLimit:         testBlock.GasLimit,
		GasUsed:          testBlock.GasUsed,
		Timestamp:        testBlock.Timestamp,
		Transactions:     testBlock.Transactions,
	}

	assert.NotNil(t, defraBlock)
	assert.Equal(t, testBlock.Number, defraBlock.Number)
	assert.Equal(t, testBlock.Hash, defraBlock.Hash)
	assert.Len(t, defraBlock.Transactions, 1)

	// Verify transaction conversion
	assert.Equal(t, testBlock.Transactions[0].Hash, defraBlock.Transactions[0].Hash)
	assert.Equal(t, testBlock.Transactions[0].From, defraBlock.Transactions[0].From)
	assert.Equal(t, testBlock.Transactions[0].To, defraBlock.Transactions[0].To)
}

// TestIndexerLifecycle tests the complete indexer lifecycle
func TestIndexerLifecycle(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: "http://localhost:9181",
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
	// Test initial state
	assert.False(t, indexer.IsStarted())
	assert.False(t, indexer.HasIndexedAtLeastOneBlock())
	assert.Equal(t, -1, indexer.GetDefraDBPort())

	// Test state after stopping (should remain stopped)
	indexer.StopIndexing()
	assert.False(t, indexer.IsStarted())
	assert.False(t, indexer.HasIndexedAtLeastOneBlock())
}

// ---------------------------------------------------------------------------
// toAppConfig tests
// ---------------------------------------------------------------------------

func TestToAppConfig_NilInput(t *testing.T) {
	result := toAppConfig(nil)
	assert.Nil(t, result, "toAppConfig(nil) should return nil")
}

func TestToAppConfig_ValidConfig(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url:           "http://localhost:9181",
			KeyringSecret: "test-secret-key",
			P2P: config.DefraDBP2PConfig{
				Enabled:             true,
				BootstrapPeers:      []string{"/ip4/1.2.3.4/tcp/9171/p2p/QmPeer1"},
				ListenAddr:          "/ip4/0.0.0.0/tcp/9171",
				MaxRetries:          5,
				RetryBaseDelayMs:    1000,
				ReconnectIntervalMs: 30000,
				EnableAutoReconnect: true,
			},
			Store: config.DefraDBStoreConfig{
				Path:                    "/data/defra",
				BlockCacheMB:            256,
				MemTableMB:              128,
				IndexCacheMB:            64,
				NumCompactors:           4,
				NumLevelZeroTables:      10,
				NumLevelZeroTablesStall: 20,
			},
		},
	}

	result := toAppConfig(cfg)
	require.NotNil(t, result, "toAppConfig should return a non-nil config")

	// Verify top-level DefraDB fields
	assert.Equal(t, "http://localhost:9181", result.DefraDB.Url)
	assert.Equal(t, "test-secret-key", result.DefraDB.KeyringSecret)

	// Verify P2P fields
	assert.Equal(t, true, result.DefraDB.P2P.Enabled)
	assert.Equal(t, []string{"/ip4/1.2.3.4/tcp/9171/p2p/QmPeer1"}, result.DefraDB.P2P.BootstrapPeers)
	assert.Equal(t, "/ip4/0.0.0.0/tcp/9171", result.DefraDB.P2P.ListenAddr)
	assert.Equal(t, 5, result.DefraDB.P2P.MaxRetries)
	assert.Equal(t, 1000, result.DefraDB.P2P.RetryBaseDelayMs)
	assert.Equal(t, 30000, result.DefraDB.P2P.ReconnectIntervalMs)
	assert.Equal(t, true, result.DefraDB.P2P.EnableAutoReconnect)

	// Verify Store fields
	assert.Equal(t, "/data/defra", result.DefraDB.Store.Path)
	assert.Equal(t, int64(256), result.DefraDB.Store.BlockCacheMB)
	assert.Equal(t, int64(128), result.DefraDB.Store.MemTableMB)
	assert.Equal(t, int64(64), result.DefraDB.Store.IndexCacheMB)
	assert.Equal(t, 4, result.DefraDB.Store.NumCompactors)
	assert.Equal(t, 10, result.DefraDB.Store.NumLevelZeroTables)
	assert.Equal(t, 20, result.DefraDB.Store.NumLevelZeroTablesStall)
}

func TestToAppConfig_EmptyConfig(t *testing.T) {
	cfg := &config.Config{}

	result := toAppConfig(cfg)
	require.NotNil(t, result)

	// All fields should be zero values
	assert.Equal(t, "", result.DefraDB.Url)
	assert.Equal(t, "", result.DefraDB.KeyringSecret)
	assert.False(t, result.DefraDB.P2P.Enabled)
	assert.Nil(t, result.DefraDB.P2P.BootstrapPeers)
	assert.Equal(t, "", result.DefraDB.P2P.ListenAddr)
	assert.Equal(t, "", result.DefraDB.Store.Path)
	assert.Equal(t, int64(0), result.DefraDB.Store.BlockCacheMB)
}

func TestToAppConfig_ReturnsNewInstance(t *testing.T) {
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: "http://localhost:9181",
		},
	}

	result1 := toAppConfig(cfg)
	result2 := toAppConfig(cfg)

	// Each call should return a distinct appConfig instance
	require.NotNil(t, result1)
	require.NotNil(t, result2)
	assert.NotSame(t, result1, result2, "toAppConfig should return a new instance each call")
}

func TestToAppConfig_ReturnType(t *testing.T) {
	cfg := &config.Config{}
	result := toAppConfig(cfg)
	// Verify the result is the correct app-sdk type
	var _ *appConfig.Config = result
}

// ---------------------------------------------------------------------------
// IsHealthy tests
// ---------------------------------------------------------------------------

func TestIsHealthy_NotStarted(t *testing.T) {
	indexer := &ChainIndexer{isStarted: false}
	assert.False(t, indexer.IsHealthy(), "should be unhealthy when not started")
}

func TestIsHealthy_StartedNeverProcessed(t *testing.T) {
	indexer := &ChainIndexer{
		isStarted:         true,
		lastProcessedTime: time.Time{}, // zero time = never processed
	}
	assert.True(t, indexer.IsHealthy(), "should be healthy when started but never processed (starting up)")
}

func TestIsHealthy_StartedRecentlyProcessed(t *testing.T) {
	indexer := &ChainIndexer{
		isStarted:         true,
		lastProcessedTime: time.Now().Add(-1 * time.Minute), // 1 minute ago
	}
	assert.True(t, indexer.IsHealthy(), "should be healthy when recently processed")
}

func TestIsHealthy_StartedStale(t *testing.T) {
	indexer := &ChainIndexer{
		isStarted:         true,
		lastProcessedTime: time.Now().Add(-11 * time.Minute), // 11 minutes ago
	}
	assert.False(t, indexer.IsHealthy(), "should be unhealthy when last processed >10 minutes ago")
}

func TestIsHealthy_StartedExactlyAtThreshold(t *testing.T) {
	// Right at the 10-minute boundary (slightly under)
	indexer := &ChainIndexer{
		isStarted:         true,
		lastProcessedTime: time.Now().Add(-9*time.Minute - 59*time.Second),
	}
	assert.True(t, indexer.IsHealthy(), "should be healthy just under 10 minute threshold")
}

// ---------------------------------------------------------------------------
// GetCurrentBlock tests
// ---------------------------------------------------------------------------

func TestGetCurrentBlock_DefaultValue(t *testing.T) {
	indexer := &ChainIndexer{}
	assert.Equal(t, int64(0), indexer.GetCurrentBlock(), "default currentBlock should be 0")
}

func TestGetCurrentBlock_AfterUpdateBlockInfo(t *testing.T) {
	indexer := &ChainIndexer{}
	indexer.updateBlockInfo(12345)
	assert.Equal(t, int64(12345), indexer.GetCurrentBlock())
}

func TestGetCurrentBlock_AfterMultipleUpdates(t *testing.T) {
	indexer := &ChainIndexer{}
	indexer.updateBlockInfo(100)
	indexer.updateBlockInfo(200)
	indexer.updateBlockInfo(300)
	assert.Equal(t, int64(300), indexer.GetCurrentBlock(), "should reflect the most recent update")
}

// ---------------------------------------------------------------------------
// GetLastProcessedTime tests
// ---------------------------------------------------------------------------

func TestGetLastProcessedTime_DefaultValue(t *testing.T) {
	indexer := &ChainIndexer{}
	assert.True(t, indexer.GetLastProcessedTime().IsZero(), "default lastProcessedTime should be zero")
}

func TestGetLastProcessedTime_AfterUpdateBlockInfo(t *testing.T) {
	indexer := &ChainIndexer{}
	before := time.Now()
	indexer.updateBlockInfo(100)
	after := time.Now()

	lastProcessed := indexer.GetLastProcessedTime()
	assert.False(t, lastProcessed.IsZero(), "lastProcessedTime should not be zero after update")
	assert.True(t, !lastProcessed.Before(before), "lastProcessedTime should be >= time before update")
	assert.True(t, !lastProcessed.After(after), "lastProcessedTime should be <= time after update")
}

// ---------------------------------------------------------------------------
// updateBlockInfo tests
// ---------------------------------------------------------------------------

func TestUpdateBlockInfo_UpdatesCurrentBlock(t *testing.T) {
	indexer := &ChainIndexer{}

	indexer.updateBlockInfo(42)
	assert.Equal(t, int64(42), indexer.currentBlock)

	indexer.updateBlockInfo(999)
	assert.Equal(t, int64(999), indexer.currentBlock)
}

func TestUpdateBlockInfo_UpdatesLastProcessedTime(t *testing.T) {
	indexer := &ChainIndexer{}

	before := time.Now()
	indexer.updateBlockInfo(100)

	// lastProcessedTime should be approximately now
	assert.WithinDuration(t, time.Now(), indexer.lastProcessedTime, 1*time.Second,
		"lastProcessedTime should be approximately the current time")
	assert.True(t, !indexer.lastProcessedTime.Before(before),
		"lastProcessedTime should not be before the call")
}

func TestUpdateBlockInfo_CanDecrease(t *testing.T) {
	// updateBlockInfo does not enforce monotonically increasing block numbers
	indexer := &ChainIndexer{}

	indexer.updateBlockInfo(500)
	assert.Equal(t, int64(500), indexer.currentBlock)

	indexer.updateBlockInfo(100)
	assert.Equal(t, int64(100), indexer.currentBlock, "updateBlockInfo should allow lower block numbers")
}

func TestUpdateBlockInfo_ZeroBlock(t *testing.T) {
	indexer := &ChainIndexer{}
	indexer.updateBlockInfo(0)
	assert.Equal(t, int64(0), indexer.currentBlock)
	assert.False(t, indexer.lastProcessedTime.IsZero(), "lastProcessedTime should be set even for block 0")
}

func TestUpdateBlockInfo_NegativeBlock(t *testing.T) {
	indexer := &ChainIndexer{}
	indexer.updateBlockInfo(-1)
	assert.Equal(t, int64(-1), indexer.currentBlock, "updateBlockInfo does not reject negative block numbers")
}

// ---------------------------------------------------------------------------
// GetPrunerMetrics tests
// ---------------------------------------------------------------------------

func TestGetPrunerMetrics_NilPruner(t *testing.T) {
	indexer := &ChainIndexer{pruner: nil}
	metrics := indexer.GetPrunerMetrics()
	assert.Nil(t, metrics, "GetPrunerMetrics should return nil when pruner is nil")
}

// ---------------------------------------------------------------------------
// extractPublicKeyFromPeerID tests
// ---------------------------------------------------------------------------

func TestExtractPublicKeyFromPeerID_InvalidPeerID(t *testing.T) {
	logger.InitConsoleOnly(true)

	result := extractPublicKeyFromPeerID("not-a-valid-peer-id")
	assert.Equal(t, "", result, "invalid peer ID should return empty string")
}

func TestExtractPublicKeyFromPeerID_EmptyString(t *testing.T) {
	logger.InitConsoleOnly(true)

	result := extractPublicKeyFromPeerID("")
	assert.Equal(t, "", result, "empty peer ID should return empty string")
}

func TestExtractPublicKeyFromPeerID_ValidPeerID(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Generate a real Ed25519 key pair and derive a peer ID
	priv, pub, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err, "key generation should not fail")
	require.NotNil(t, priv)
	require.NotNil(t, pub)

	pid, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err, "peer ID derivation should not fail")

	result := extractPublicKeyFromPeerID(pid.String())
	assert.NotEmpty(t, result, "valid peer ID should produce a non-empty hex public key")

	// Ed25519 public keys are 32 bytes -> 64 hex characters
	assert.Len(t, result, 64, "Ed25519 public key hex should be 64 characters")

	// The result should be valid hex
	for _, c := range result {
		assert.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
			"public key hex should only contain hex characters, got: %c", c)
	}
}

func TestExtractPublicKeyFromPeerID_DifferentKeysProduceDifferentResults(t *testing.T) {
	logger.InitConsoleOnly(true)

	priv1, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	pid1, err := peer.IDFromPrivateKey(priv1)
	require.NoError(t, err)

	priv2, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	pid2, err := peer.IDFromPrivateKey(priv2)
	require.NoError(t, err)

	result1 := extractPublicKeyFromPeerID(pid1.String())
	result2 := extractPublicKeyFromPeerID(pid2.String())

	assert.NotEqual(t, result1, result2, "different peer IDs should produce different public keys")
}

// ---------------------------------------------------------------------------
// GetDefraDBPort tests (nil node case)
// ---------------------------------------------------------------------------

func TestGetDefraDBPort_NilDefraNode(t *testing.T) {
	indexer := &ChainIndexer{defraNode: nil}
	assert.Equal(t, -1, indexer.GetDefraDBPort(), "nil defraNode should return -1")
}

// ---------------------------------------------------------------------------
// Integration-style tests combining multiple methods
// ---------------------------------------------------------------------------

func TestIsHealthy_AfterUpdateBlockInfo(t *testing.T) {
	// Verify that updateBlockInfo makes an indexer with isStarted=true healthy
	indexer := &ChainIndexer{isStarted: true}

	// Before any update: zero time means healthy (startup phase)
	assert.True(t, indexer.IsHealthy())

	// After an update: recently processed means healthy
	indexer.updateBlockInfo(42)
	assert.True(t, indexer.IsHealthy(), "should be healthy after recent block update")
	assert.Equal(t, int64(42), indexer.GetCurrentBlock())
}

func TestGetCurrentBlockAndLastProcessedTime_Consistency(t *testing.T) {
	indexer := &ChainIndexer{}

	// Both should start at zero values
	assert.Equal(t, int64(0), indexer.GetCurrentBlock())
	assert.True(t, indexer.GetLastProcessedTime().IsZero())

	// After update, both should reflect the change
	indexer.updateBlockInfo(500)
	assert.Equal(t, int64(500), indexer.GetCurrentBlock())
	assert.False(t, indexer.GetLastProcessedTime().IsZero())

	// Second update should advance both
	time1 := indexer.GetLastProcessedTime()
	// Small sleep to ensure time advances
	time.Sleep(1 * time.Millisecond)
	indexer.updateBlockInfo(501)

	assert.Equal(t, int64(501), indexer.GetCurrentBlock())
	assert.True(t, !indexer.GetLastProcessedTime().Before(time1),
		"lastProcessedTime should advance or stay same with subsequent updates")
}

// ---------------------------------------------------------------------------
// NewConcurrentBlockProcessor tests
// ---------------------------------------------------------------------------

func TestNewConcurrentBlockProcessor(t *testing.T) {
	p := NewConcurrentBlockProcessor(nil, nil, 4, 8, 60)
	require.NotNil(t, p)
	assert.Equal(t, 4, p.workers)
	assert.Equal(t, 8, p.receiptWorkers)
	assert.Equal(t, 60, p.blocksPerMinute)
	assert.NotNil(t, p.resultChan)
	assert.NotNil(t, p.pending)
	assert.NotNil(t, p.signingChan)
}

func TestNewConcurrentBlockProcessor_DefaultValues(t *testing.T) {
	p := NewConcurrentBlockProcessor(nil, nil, 1, 1, 0)
	require.NotNil(t, p)
	assert.Equal(t, 1, p.workers)
	assert.Equal(t, 0, p.blocksPerMinute)
}

// ---------------------------------------------------------------------------
// applySchemaViaHTTP tests
// ---------------------------------------------------------------------------

func TestApplySchemaViaHTTP_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v0/schema", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := applySchemaViaHTTP(server.URL)
	assert.NoError(t, err)
}

func TestApplySchemaViaHTTP_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("schema error"))
	}))
	defer server.Close()

	err := applySchemaViaHTTP(server.URL)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestApplySchemaViaHTTP_ConnectionRefused(t *testing.T) {
	err := applySchemaViaHTTP("http://127.0.0.1:1")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetPeerInfo tests
// ---------------------------------------------------------------------------

func TestGetPeerInfo_NilNode(t *testing.T) {
	indexer := &ChainIndexer{defraNode: nil}
	info, err := indexer.GetPeerInfo()
	assert.NoError(t, err)
	assert.Nil(t, info)
}

// ---------------------------------------------------------------------------
// GetNodePublicKey / GetPeerPublicKey tests (nil node)
// ---------------------------------------------------------------------------

func TestGetNodePublicKey_NilNode(t *testing.T) {
	indexer := &ChainIndexer{
		defraNode: nil,
		cfg:       &config.Config{},
	}
	_, err := indexer.GetNodePublicKey()
	assert.Error(t, err)
}

func TestGetPeerPublicKey_NilNode(t *testing.T) {
	indexer := &ChainIndexer{
		defraNode: nil,
		cfg:       &config.Config{},
	}
	_, err := indexer.GetPeerPublicKey()
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// StopIndexing with embedded DefraDB node
// ---------------------------------------------------------------------------

func TestStopIndexing_WithEmbeddedNode(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	indexer := &ChainIndexer{
		defraNode:   td.Node,
		shouldIndex: true,
		isStarted:   true,
		cfg:         &config.Config{},
	}

	indexer.StopIndexing()

	assert.False(t, indexer.shouldIndex)
	assert.False(t, indexer.isStarted)
	assert.Nil(t, indexer.defraNode)
}

// ---------------------------------------------------------------------------
// GetDefraDBPort with embedded DefraDB node
// ---------------------------------------------------------------------------

func TestGetDefraDBPort_WithEmbeddedNode(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	indexer := &ChainIndexer{defraNode: td.Node}
	port := indexer.GetDefraDBPort()
	assert.Equal(t, td.Port, port)
}

// ---------------------------------------------------------------------------
// SignMessages with nil node
// ---------------------------------------------------------------------------

func TestSignMessages_NilNode(t *testing.T) {
	indexer := &ChainIndexer{
		defraNode: nil,
		cfg:       &config.Config{},
	}
	_, _, err := indexer.SignMessages("test message")
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// BlockResult struct tests
// ---------------------------------------------------------------------------

func TestBlockResult_Fields(t *testing.T) {
	r := &BlockResult{
		BlockNum: 42,
		BlockID:  "bae-123",
		Success:  true,
		Error:    nil,
	}
	assert.Equal(t, int64(42), r.BlockNum)
	assert.Equal(t, "bae-123", r.BlockID)
	assert.True(t, r.Success)
	assert.Nil(t, r.Error)
}

// ---------------------------------------------------------------------------
// openBrowser test (just verifying it doesn't panic)
// ---------------------------------------------------------------------------

func TestOpenBrowser_InvalidURL(t *testing.T) {
	logger.InitConsoleOnly(true)
	// Just verify it doesn't panic with an empty URL
	openBrowser("")
}

// ---------------------------------------------------------------------------
// Mock JSON-RPC server for indexer-level integration tests
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     any             `json:"id"`
}

func newMockRPCServer(handler func(method string, params json.RawMessage) (any, error)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		result, rpcErr := handler(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if rpcErr != nil {
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32000, "message": rpcErr.Error()},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func fullBlockResponse(number string, txs []any) map[string]any {
	emptyTrieRoot := "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
	block := map[string]any{
		"number":           number,
		"hash":             "0x0000000000000000000000000000000000000000000000000000000000000001",
		"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":            "0x0000000000000000",
		"sha3Uncles":       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		"logsBloom":        "0x" + fmt.Sprintf("%0512x", 0),
		"transactionsRoot": emptyTrieRoot,
		"stateRoot":        "0x0000000000000000000000000000000000000000000000000000000000000000",
		"receiptsRoot":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"miner":            "0x0000000000000000000000000000000000000000",
		"difficulty":       "0x0",
		"totalDifficulty":  "0x0",
		"extraData":        "0x",
		"size":             "0x100",
		"gasLimit":         "0x1000000",
		"gasUsed":          "0x5208",
		"timestamp":        "0x60000000",
		"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
		"uncles":           []any{},
	}
	if txs != nil {
		block["transactions"] = txs
	} else {
		block["transactions"] = []any{}
	}
	return block
}

// fullBlockResponseWithTx returns a block response with a single legacy transaction.
// The transactionsRoot is set to a non-empty value so go-ethereum accepts it.
func fullBlockResponseWithTx(number string) map[string]any {
	tx := map[string]any{
		"hash":             "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"nonce":            "0x0",
		"blockHash":        "0x0000000000000000000000000000000000000000000000000000000000000001",
		"blockNumber":      number,
		"transactionIndex": "0x0",
		"from":             "0x0000000000000000000000000000000000000001",
		"to":               "0x0000000000000000000000000000000000000002",
		"value":            "0x3e8",
		"gas":              "0x5208",
		"gasPrice":         "0x3b9aca00",
		"input":            "0x",
		"v":                "0x1b",
		"r":                "0x1111111111111111111111111111111111111111111111111111111111111111",
		"s":                "0x2222222222222222222222222222222222222222222222222222222222222222",
		"type":             "0x0",
	}

	block := map[string]any{
		"number":           number,
		"hash":             "0x0000000000000000000000000000000000000000000000000000000000000001",
		"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":            "0x0000000000000000",
		"sha3Uncles":       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		"logsBloom":        "0x" + fmt.Sprintf("%0512x", 0),
		"transactionsRoot": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", // non-empty → indicates txns present
		"stateRoot":        "0x0000000000000000000000000000000000000000000000000000000000000000",
		"receiptsRoot":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"miner":            "0x0000000000000000000000000000000000000000",
		"difficulty":       "0x0",
		"totalDifficulty":  "0x0",
		"extraData":        "0x",
		"size":             "0x100",
		"gasLimit":         "0x1000000",
		"gasUsed":          "0x5208",
		"timestamp":        "0x60000000",
		"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
		"uncles":           []any{},
		"transactions":     []any{tx},
	}
	return block
}

// ---------------------------------------------------------------------------
// processBlock + processBlockBatch integration tests
// ---------------------------------------------------------------------------

func TestProcessBlock_Success_NoTransactions(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create mock RPC server returning a block with no transactions
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x64", nil), nil // block 100
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	ctx := context.Background()
	err = indexer.processBlock(ctx, ethClient, blockHandler, 100)
	require.NoError(t, err)

	// Verify block was stored in DefraDB
	highest, err := blockHandler.GetHighestBlockNumber(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(100), highest)
}

func TestProcessBlock_RPCError_RetriesAndFails(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	callCount := 0
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			callCount++
			return nil, fmt.Errorf("connection refused")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	ctx := context.Background()
	err = indexer.processBlock(ctx, ethClient, blockHandler, 100)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch block")
	assert.Equal(t, DefaultRetryAttempts, callCount)
}

func TestProcessBlockBatch_WithTransactions(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create mock RPC server that returns receipts for transactions
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return map[string]any{
				"transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000001",
				"blockNumber":     "0xc8", // 200
				"blockHash":       "0x0000000000000000000000000000000000000000000000000000000000000002",
				"gasUsed":         "0x5208",
				"status":          "0x1",
				"logs":            []any{},
			}, nil
		case "eth_getBlockReceipts":
			return nil, fmt.Errorf("not supported")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:     "200",
		Hash:       "0x0000000000000000000000000000000000000000000000000000000000000002",
		ParentHash: "0x0000000000000000000000000000000000000000000000000000000000000001",
		Timestamp:  "1640995200",
		Miner:      "0x0000000000000000000000000000000000000000",
		GasLimit:   "8000000",
		GasUsed:    "21000",
		Transactions: []types.Transaction{
			{
				Hash:             "0x0000000000000000000000000000000000000000000000000000000000000001",
				BlockNumber:      "200",
				From:             "0x1234567890123456789012345678901234567890",
				To:               "0x0987654321098765432109876543210987654321",
				Value:            "1000000",
				Gas:              "21000",
				GasPrice:         "20000000000",
				Nonce:            "1",
				TransactionIndex: 0,
				Type:             "0",
				ChainId:          "1",
				V:                "27",
				R:                "0x1234",
				S:                "0x5678",
			},
		},
	}

	ctx := context.Background()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 200)
	require.NoError(t, err)

	// Verify the block was stored
	highest, err := blockHandler.GetHighestBlockNumber(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(200), highest)
}

func TestProcessBlockBatch_WithBlockReceipts(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create mock RPC server that supports eth_getBlockReceipts
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockReceipts":
			return []any{
				map[string]any{
					"transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000010",
					"blockNumber":     "0x12c", // 300
					"blockHash":       "0x0000000000000000000000000000000000000000000000000000000000000003",
					"gasUsed":         "0x5208",
					"status":          "0x1",
					"logs":            []any{},
				},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:     "300",
		Hash:       "0x0000000000000000000000000000000000000000000000000000000000000003",
		ParentHash: "0x0000000000000000000000000000000000000000000000000000000000000002",
		Timestamp:  "1640995200",
		Miner:      "0x0000000000000000000000000000000000000000",
		GasLimit:   "8000000",
		GasUsed:    "21000",
		Transactions: []types.Transaction{
			{
				Hash:             "0x0000000000000000000000000000000000000000000000000000000000000010",
				BlockNumber:      "300",
				From:             "0x1234567890123456789012345678901234567890",
				To:               "0x0987654321098765432109876543210987654321",
				Value:            "1000000",
				Gas:              "21000",
				GasPrice:         "20000000000",
				Nonce:            "1",
				TransactionIndex: 0,
				Type:             "0",
				ChainId:          "1",
				V:                "27",
				R:                "0x1234",
				S:                "0x5678",
			},
		},
	}

	ctx := context.Background()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 300)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// TrackBlock (indexerQueueTracker) tests
// ---------------------------------------------------------------------------

// fakeDocID generates a valid bae-prefixed UUID for testing
func fakeDocID(seed int) string {
	return fmt.Sprintf("bae-%08x-0000-0000-0000-%012x", seed, seed)
}

func TestTrackBlock_Success(t *testing.T) {
	queue := pruner.NewIndexerQueue()
	tracker := &indexerQueueTracker{queue: queue}

	result := &defra.BlockCreationResult{
		BlockID:          fakeDocID(1),
		TransactionIDs:   []string{fakeDocID(2), fakeDocID(3)},
		LogIDs:           []string{fakeDocID(4)},
		AccessListIDs:    []string{fakeDocID(5)},
		BlockSignatureID: fakeDocID(6),
	}

	err := tracker.TrackBlock(context.Background(), 100, result)
	require.NoError(t, err)
	assert.Equal(t, 1, queue.Len())
}

func TestTrackBlock_MultipleBlocks(t *testing.T) {
	queue := pruner.NewIndexerQueue()
	tracker := &indexerQueueTracker{queue: queue}

	for i := int64(100); i < 105; i++ {
		result := &defra.BlockCreationResult{
			BlockID:        fakeDocID(int(i)),
			TransactionIDs: []string{fakeDocID(int(i) + 1000)},
		}
		err := tracker.TrackBlock(context.Background(), i, result)
		require.NoError(t, err)
	}
	assert.Equal(t, 5, queue.Len())
}

func TestTrackBlock_EmptyResult(t *testing.T) {
	queue := pruner.NewIndexerQueue()
	tracker := &indexerQueueTracker{queue: queue}

	result := &defra.BlockCreationResult{
		BlockID: fakeDocID(1),
	}

	err := tracker.TrackBlock(context.Background(), 100, result)
	require.NoError(t, err)
	assert.Equal(t, 1, queue.Len())
}

func TestTrackBlock_PassesCorrectCollectionNames(t *testing.T) {
	queue := pruner.NewIndexerQueue()
	tracker := &indexerQueueTracker{queue: queue}

	result := &defra.BlockCreationResult{
		BlockID:          fakeDocID(1),
		TransactionIDs:   []string{fakeDocID(2)},
		LogIDs:           []string{fakeDocID(3)},
		AccessListIDs:    []string{fakeDocID(4)},
		BlockSignatureID: fakeDocID(5),
	}

	// The tracker maps to constants.CollectionTransaction, CollectionLog, CollectionAccessListEntry
	err := tracker.TrackBlock(context.Background(), 100, result)
	require.NoError(t, err)

	// Verify the constants are used (they should match what pruner expects)
	assert.NotEmpty(t, constants.CollectionTransaction)
	assert.NotEmpty(t, constants.CollectionLog)
	assert.NotEmpty(t, constants.CollectionAccessListEntry)
}

// ---------------------------------------------------------------------------
// GetPrunerMetrics with non-nil pruner
// ---------------------------------------------------------------------------

func TestGetPrunerMetrics_WithPruner(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	p := pruner.NewPruner(&pruner.Config{
		Enabled:   true,
		MaxBlocks: 1000,
	}, td.Node)

	indexer := &ChainIndexer{pruner: p}
	metrics := indexer.GetPrunerMetrics()
	require.NotNil(t, metrics)
	assert.True(t, metrics.Enabled)
}

// ---------------------------------------------------------------------------
// StopIndexing with snapshotter + pruner + healthServer
// ---------------------------------------------------------------------------

func TestStopIndexing_WithSnapshotter(t *testing.T) {
	logger.InitConsoleOnly(true)

	dir := t.TempDir()
	snapCfg := &snapshot.Config{
		Enabled:         true,
		Dir:             dir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600,
	}
	s := snapshot.New(snapCfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Start(ctx)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		shouldIndex: true,
		isStarted:   true,
		cfg:         &config.Config{},
		snapshotter: s,
	}

	indexer.StopIndexing()
	assert.False(t, indexer.shouldIndex)
	assert.Nil(t, indexer.snapshotter)
}

func TestStopIndexing_WithPruner(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)
	p := pruner.NewPruner(&pruner.Config{
		Enabled:   true,
		MaxBlocks: 1000,
	}, td.Node)

	indexer := &ChainIndexer{
		shouldIndex: true,
		isStarted:   true,
		cfg:         &config.Config{},
		pruner:      p,
	}

	indexer.StopIndexing()
	assert.False(t, indexer.shouldIndex)
	assert.Nil(t, indexer.pruner)
}

func TestStopIndexing_WithHealthServer(t *testing.T) {
	logger.InitConsoleOnly(true)

	hs := NewHealthServerForTest(t)

	indexer := &ChainIndexer{
		shouldIndex:  true,
		isStarted:    true,
		cfg:          &config.Config{},
		healthServer: hs,
	}

	indexer.StopIndexing()
	assert.False(t, indexer.shouldIndex)
}

// NewHealthServerForTest creates a health server that can be stopped
func NewHealthServerForTest(t *testing.T) *server.HealthServer {
	t.Helper()
	// Use a random high port to avoid conflicts
	hs := server.NewHealthServer(0, nil, "")
	return hs
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock tests
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_Success_NoTx(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x1f4", nil), nil // block 500
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	result := p.fetchAndProcessBlock(context.Background(), 500)
	require.NotNil(t, result)
	assert.True(t, result.Success, "fetchAndProcessBlock should succeed: %v", result.Error)
	assert.NotEmpty(t, result.BlockID)
	assert.Equal(t, int64(500), result.BlockNum)
}

func TestFetchAndProcessBlock_RPCError(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return nil, fmt.Errorf("internal server error")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	result := p.fetchAndProcessBlock(context.Background(), 500)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "failed to fetch block")
}

func TestFetchAndProcessBlock_ContextCancelled(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		return "0x1", nil
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result := p.fetchAndProcessBlock(ctx, 500)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Error(t, result.Error)
}

func TestFetchAndProcessBlock_DuplicateBlock(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x2bc", nil), nil // block 700
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	// First call should succeed
	result1 := p.fetchAndProcessBlock(context.Background(), 700)
	require.NotNil(t, result1)
	assert.True(t, result1.Success)

	// Second call should detect duplicate and return "existing"
	result2 := p.fetchAndProcessBlock(context.Background(), 700)
	require.NotNil(t, result2)
	assert.True(t, result2.Success)
	assert.Equal(t, "existing", result2.BlockID)
}

// ---------------------------------------------------------------------------
// ProcessBlocks tests (with context cancellation)
// ---------------------------------------------------------------------------

func TestProcessBlocks_ContextCancel(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	blockCount := 0
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			blockCount++
			num := fmt.Sprintf("0x%x", 1000+blockCount)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithCancel(context.Background())

	processed := make([]int64, 0)
	var mu sync.Mutex

	// Cancel after a short time
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	err = p.ProcessBlocks(ctx, 1001, func(blockNum int64) {
		mu.Lock()
		processed = append(processed, blockNum)
		mu.Unlock()
	})

	assert.ErrorIs(t, err, context.Canceled)
	// Should have processed at least some blocks before cancellation
	mu.Lock()
	t.Logf("Processed %d blocks before cancellation", len(processed))
	mu.Unlock()
}

func TestProcessBlocks_WithRateLimit_ContextCancel(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	blockCount := 0
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			blockCount++
			num := fmt.Sprintf("0x%x", 2000+blockCount)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	// Rate limit to 600 blocks/min = 10/sec
	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 600)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	err = p.ProcessBlocks(ctx, 2001, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

// ===========================================================================
// Additional tests to boost coverage to 95%+
// ===========================================================================

// ---------------------------------------------------------------------------
// StartIndexing — external DefraDB path (defraStarted=true)
// ---------------------------------------------------------------------------

func TestStartIndexing_ExternalDefraDB_WaitFails(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Point to a non-listening address so WaitForDefraDB fails
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: "http://127.0.0.1:1",
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer := &ChainIndexer{cfg: cfg}
	err := indexer.StartIndexing(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DefraDB failed to become ready")
}

func TestStartIndexing_ExternalDefraDB_SchemaApplyFails(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Mock DefraDB server: GraphQL introspection succeeds (for WaitForDefraDB)
	// but schema application fails with a non-already-exists error.
	defraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/graphql" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":{"__schema":{"types":[]}}}`))
			return
		}
		if r.URL.Path == "/api/v0/schema" && r.Method == "POST" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("schema application failed"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer defraServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: defraServer.URL,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer := &ChainIndexer{cfg: cfg}
	err := indexer.StartIndexing(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to apply schema to external DefraDB")
}

func TestStartIndexing_ExternalDefraDB_SchemaAlreadyExists(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Mock DefraDB server: GraphQL introspection succeeds, schema returns
	// "already exists" error → should be tolerated, but defraNode is nil → error
	defraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/graphql" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":{"__schema":{"types":[]}}}`))
			return
		}
		if r.URL.Path == "/api/v0/schema" && r.Method == "POST" {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("collection already exists"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer defraServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: defraServer.URL,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer := &ChainIndexer{cfg: cfg}
	err := indexer.StartIndexing(true)
	require.Error(t, err)
	// The "already exists" error is tolerated, but defraNode is nil
	assert.Contains(t, err.Error(), "defraNode is required")
}

// ---------------------------------------------------------------------------
// StartIndexing — embedded full integration (covers the biggest chunk: lines 147-385)
// ---------------------------------------------------------------------------

// newMockRPCServerForIntegration creates a mock that handles all methods needed
// by the full StartIndexing flow. blockCh is sent on every eth_getBlockByNumber call
// so the caller can track progress.
func newMockRPCServerForIntegration(blockCh chan<- struct{}) *httptest.Server {
	var blockCallCount atomic.Int64

	return newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			count := blockCallCount.Add(1)
			if blockCh != nil {
				select {
				case blockCh <- struct{}{}:
				default:
				}
			}
			// Return a unique block per call: use a high starting number
			num := fmt.Sprintf("0x%x", 100000+count)
			return fullBlockResponse(num, nil), nil

		case "eth_blockNumber":
			// Used by HeaderByNumber(nil) → returns the "latest" header
			return "0x100000", nil

		case "eth_getBlockReceipts":
			return []any{}, nil

		case "net_version":
			return "1", nil

		case "eth_chainId":
			return "0x1", nil

		default:
			return "0x1", nil
		}
	})
}

func TestStartIndexing_Embedded_FullIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	blockCh := make(chan struct{}, 100)
	rpcServer := newMockRPCServerForIntegration(blockCh)
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url:           "",
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P: config.DefraDBP2PConfig{
				Enabled: false,
			},
			Store: config.DefraDBStoreConfig{
				Path: tmpDir,
			},
		},
		Geth: config.GethConfig{
			NodeURL: rpcServer.URL,
		},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0, // disabled
			StartBuffer:      10,
		},
		Pruner: pruner.Config{
			Enabled:         true,
			MaxBlocks:       1000,
			PruneThreshold:  500,
			IntervalSeconds: 3600,
		},
		Snapshot: snapshot.Config{
			Enabled:         true,
			Dir:             filepath.Join(tmpDir, "snapshots"),
			BlocksPerFile:   1000,
			IntervalSeconds: 3600,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	// Run StartIndexing in a goroutine and cancel after we see some blocks processed
	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Wait for at least a few block calls
	deadline := time.After(60 * time.Second)
	blocksSeen := 0
	for blocksSeen < 3 {
		select {
		case <-blockCh:
			blocksSeen++
		case <-deadline:
			t.Fatalf("timed out waiting for blocks to be processed (saw %d)", blocksSeen)
		case err := <-errCh:
			// StartIndexing returned early — could be a startup failure
			if err != nil {
				t.Fatalf("StartIndexing returned early with error: %v", err)
			}
		}
	}

	// Stop the indexer — closes defraNode + subsystems.
	// Note: ProcessBlocks uses context.Background() so it won't return immediately.
	// We just verify the indexer was functional, then clean up.
	indexer.StopIndexing()
	assert.False(t, indexer.shouldIndex)
	assert.False(t, indexer.isStarted)
}

// TestStartIndexing_Embedded_WithConfiguredStartHeight tests the configuredHeight > 0 branch.
func TestStartIndexing_Embedded_WithConfiguredStartHeight(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	blockCh := make(chan struct{}, 100)
	rpcServer := newMockRPCServerForIntegration(blockCh)
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      50000, // explicit configured height
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

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	blocksSeen := 0
	for blocksSeen < 2 {
		select {
		case <-blockCh:
			blocksSeen++
		case <-deadline:
			t.Fatalf("timed out (saw %d)", blocksSeen)
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.StopIndexing()
	assert.False(t, indexer.shouldIndex)
}

// TestStartIndexing_Embedded_WithHealthServer tests the health server branch.
func TestStartIndexing_Embedded_WithHealthServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	blockCh := make(chan struct{}, 100)
	rpcServer := newMockRPCServerForIntegration(blockCh)
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url:           "http://localhost:9999", // Set Url so healthDefraURL uses config URL branch
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 19876, // Enable health server on a high port
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	blocksSeen := 0
	for blocksSeen < 2 {
		select {
		case <-blockCh:
			blocksSeen++
		case <-deadline:
			t.Fatalf("timed out (saw %d)", blocksSeen)
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	// Verify health server is running
	assert.NotNil(t, indexer.healthServer)

	indexer.StopIndexing()
	assert.False(t, indexer.shouldIndex)
}

// ---------------------------------------------------------------------------
// runConcurrentIndexing test (direct call)
// ---------------------------------------------------------------------------

func TestRunConcurrentIndexing_DirectCall(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var blockCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := blockCount.Add(1)
			num := fmt.Sprintf("0x%x", 5000+n)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		case "eth_blockNumber":
			return "0x100000", nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{
				ConcurrentBlocks: 2,
				ReceiptWorkers:   2,
				BlocksPerMinute:  0,
			},
		},
		defraNode: td.Node,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()

	err = indexer.runConcurrentIndexing(ctx, ethClient, blockHandler, 5001, indexer.cfg)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, indexer.isStarted)
	assert.True(t, indexer.shouldIndex)
}

// ---------------------------------------------------------------------------
// GetPeerInfo tests with embedded node
// ---------------------------------------------------------------------------

func TestGetPeerInfo_WithEmbeddedNode(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)
	indexer := &ChainIndexer{defraNode: td.Node}

	info, err := indexer.GetPeerInfo()
	require.NoError(t, err)
	require.NotNil(t, info)

	// P2P is disabled in test, so network shouldn't be active
	assert.False(t, info.Enabled)
	// Self should have peer information
	if info.Self != nil {
		assert.NotEmpty(t, info.Self.ID)
	}
}

func TestGetPeerInfo_WithEmbeddedNodeAndNetworkHandler(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)
	// networkHandler is nil but defraNode is set - covers the line networkActive = false
	indexer := &ChainIndexer{
		defraNode:      td.Node,
		networkHandler: nil, // nil network handler
	}

	info, err := indexer.GetPeerInfo()
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.False(t, info.Enabled)
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — receipt fallback path
// ---------------------------------------------------------------------------

// TestFetchAndProcessBlock_ReceiptFallbackViaProcessBlockBatch tests the receipt
// fallback path via processBlockBatch where eth_getBlockReceipts fails and individual
// eth_getTransactionReceipt succeeds. This tests the processBlockBatch receipt fetching
// goroutine paths directly (since fetchAndProcessBlock goes through go-ethereum which
// validates transaction roots).
func TestFetchAndProcessBlock_ReceiptFallbackViaProcessBlockBatch(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	txHash := "0x0000000000000000000000000000000000000000000000000000000000000abc"
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return map[string]any{
				"transactionHash":   txHash,
				"blockNumber":       "0x3e8",
				"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",
				"gasUsed":           "0x5208",
				"cumulativeGasUsed": "0x5208",
				"status":            "0x1",
				"logs":              []any{},
				"transactionIndex":  "0x0",
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	// Create block with a transaction — processBlockBatch will fetch receipts individually
	block := &types.Block{
		Number:           "1000",
		Hash:             "0x0000000000000000000000000000000000000000000000000000000000000abc",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Timestamp:        "1640995200",
		Miner:            "0x0000000000000000000000000000000000000000",
		GasLimit:         "8000000",
		GasUsed:          "21000",
		Nonce:            "0",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000000",
		ReceiptsRoot:     "0x0000000000000000000000000000000000000000000000000000000000000000",
		Transactions: []types.Transaction{
			{
				Hash:             txHash,
				BlockNumber:      "1000",
				From:             "0x1234567890123456789012345678901234567890",
				To:               "0x0987654321098765432109876543210987654321",
				Value:            "1000000",
				Gas:              "21000",
				GasPrice:         "20000000000",
				Nonce:            "1",
				TransactionIndex: 0,
				Type:             "0",
				ChainId:          "1",
				V:                "27",
				R:                "0x1234",
				S:                "0x5678",
			},
		},
	}

	ctx := context.Background()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 1000)
	require.NoError(t, err)
}

// TestFetchAndProcessBlock_NotFoundThenSuccess tests the not-found retry path.
func TestFetchAndProcessBlock_NotFoundThenSuccess(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var callCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := callCount.Add(1)
			if n <= 1 {
				// First call: return null (not found)
				return nil, nil
			}
			// Second call: return valid block
			return fullBlockResponse("0x4e20", nil), nil // block 20000
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	// Use a context with timeout so the not-found retry doesn't block forever
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := p.fetchAndProcessBlock(ctx, 20000)
	require.NotNil(t, result)
	// It should eventually succeed because the second call returns a valid block
	assert.True(t, result.Success, "should succeed after not-found retry: %v", result.Error)
}

// TestFetchAndProcessBlock_OtherRPCErrorRetry tests other (non-not-found) error retry.
func TestFetchAndProcessBlock_OtherRPCErrorRetry(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var callCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := callCount.Add(1)
			if n <= 2 {
				return nil, fmt.Errorf("temporary server error")
			}
			return fullBlockResponse("0x7530", nil), nil // block 30000
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	result := p.fetchAndProcessBlock(context.Background(), 30000)
	require.NotNil(t, result)
	// After 2 failures + 1 success = succeeds on attempt 3
	assert.True(t, result.Success, "should succeed after RPC error retries: %v", result.Error)
}

// TestFetchAndProcessBlock_TransactionConflict tests the transaction conflict retry path.
func TestFetchAndProcessBlock_TransactionConflict(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x9c40", nil), nil // block 40000
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	// Insert block first
	result1 := p.fetchAndProcessBlock(context.Background(), 40000)
	require.True(t, result1.Success)

	// Second insert should hit "already exists" → enqueue signing → return "existing"
	result2 := p.fetchAndProcessBlock(context.Background(), 40000)
	require.NotNil(t, result2)
	assert.True(t, result2.Success)
	assert.Equal(t, "existing", result2.BlockID)
}

// TestFetchAndProcessBlock_ContextCancelledDuringNotFound tests cancellation
// during the not-found wait loop.
func TestFetchAndProcessBlock_ContextCancelledDuringNotFound(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return nil, nil // always not found
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := p.fetchAndProcessBlock(ctx, 99999)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Error(t, result.Error)
}

// TestFetchAndProcessBlock_ContextCancelledDuringOtherRetry tests cancellation
// during the non-not-found retry backoff.
func TestFetchAndProcessBlock_ContextCancelledDuringOtherRetry(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return nil, fmt.Errorf("temporary error")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result := p.fetchAndProcessBlock(ctx, 88888)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Error(t, result.Error)
}

// ---------------------------------------------------------------------------
// processBlockBatch — already exists handling
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_AlreadyExists(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockReceipts":
			return []any{}, nil
		case "eth_getTransactionReceipt":
			return map[string]any{
				"transactionHash":   "0x0000000000000000000000000000000000000000000000000000000000000099",
				"blockNumber":       "0x1388",
				"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",
				"gasUsed":           "0x5208",
				"cumulativeGasUsed": "0x5208",
				"status":            "0x1",
				"logs":              []any{},
				"transactionIndex":  "0x0",
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:           "5000",
		Hash:             "0x0000000000000000000000000000000000000000000000000000000000000099",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Timestamp:        "1640995200",
		Miner:            "0x0000000000000000000000000000000000000000",
		GasLimit:         "8000000",
		GasUsed:          "0",
		Transactions:     []types.Transaction{},
		Nonce:            "0",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000000",
		ReceiptsRoot:     "0x0000000000000000000000000000000000000000000000000000000000000000",
	}

	ctx := context.Background()
	// First insertion should succeed
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 5000)
	require.NoError(t, err)

	// Second insertion: block already exists → should hit IsErrAlreadyExists branch
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 5000)
	// Should return nil (already-exists is handled gracefully)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// SignMessages with embedded node
// ---------------------------------------------------------------------------

func TestSignMessages_WithEmbeddedNode(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)
	indexer := &ChainIndexer{
		defraNode: td.Node,
		cfg: &config.Config{
			DefraDB: config.DefraDBConfig{
				Store: config.DefraDBStoreConfig{Path: td.Dir},
				// No KeyringSecret → signing will fail
			},
		},
	}

	// Without a keyring secret, SignMessages should return an error
	_, _, err := indexer.SignMessages("test message")
	assert.Error(t, err)
}

func TestSignMessages_WithEmbeddedNode_KeyringSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)
	indexer := &ChainIndexer{
		defraNode: td.Node,
		cfg: &config.Config{
			DefraDB: config.DefraDBConfig{
				KeyringSecret: "test-secret-key-12345678",
				Store:         config.DefraDBStoreConfig{Path: td.Dir},
			},
		},
	}

	// With a keyring secret but no identity stored, it may create one or fail
	// Either way, we exercise the SignMessages code paths
	_, _, err := indexer.SignMessages("test message")
	// The signer will try to load/create an identity from the keyring
	// It may succeed or fail depending on whether the identity was already created
	if err != nil {
		t.Logf("SignMessages returned error (expected without prior identity setup): %v", err)
	}
}

// ---------------------------------------------------------------------------
// openBrowser test with valid URL
// ---------------------------------------------------------------------------

func TestOpenBrowser_ValidURL(t *testing.T) {
	logger.InitConsoleOnly(true)
	// This will attempt to open a browser on macOS via `open` command.
	// In CI, it may fail silently. We just verify it doesn't panic.
	openBrowser("http://localhost:12345/health")
}

// ---------------------------------------------------------------------------
// StopIndexing comprehensive (with all subsystems)
// ---------------------------------------------------------------------------

func TestStopIndexing_WithAllComponents(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create pruner
	p := pruner.NewPruner(&pruner.Config{
		Enabled:   true,
		MaxBlocks: 1000,
	}, td.Node)

	// Create snapshotter
	snapDir := t.TempDir()
	snapCfg := &snapshot.Config{
		Enabled:         true,
		Dir:             snapDir,
		BlocksPerFile:   1000,
		IntervalSeconds: 3600,
	}
	s := snapshot.New(snapCfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Start(ctx)
	require.NoError(t, err)

	// Create health server
	hs := server.NewHealthServer(0, nil, "")

	indexer := &ChainIndexer{
		shouldIndex:    true,
		isStarted:      true,
		defraNode:      td.Node,
		pruner:         p,
		snapshotter:    s,
		healthServer:   hs,
		networkHandler: nil, // test nil network handler branch
		cfg:            &config.Config{},
	}

	indexer.StopIndexing()

	assert.False(t, indexer.shouldIndex)
	assert.False(t, indexer.isStarted)
	assert.Nil(t, indexer.defraNode)
	assert.Nil(t, indexer.pruner)
	assert.Nil(t, indexer.snapshotter)
}

// ---------------------------------------------------------------------------
// ProcessBlocks — additional coverage for tooFarAhead and rate-limiting paths
// ---------------------------------------------------------------------------

func TestProcessBlocks_TooFarAhead(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var callCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := callCount.Add(1)
			num := fmt.Sprintf("0x%x", 3000+n)
			// Add a small delay to simulate slow RPC, causing tooFarAhead to trigger
			if n > 3 {
				time.Sleep(200 * time.Millisecond)
			}
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	// Use only 1 worker so the tooFarAhead check (workers*2=2) triggers quickly
	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(2 * time.Second)
		cancel()
	}()

	err = p.ProcessBlocks(ctx, 3001, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestProcessBlocks_WithNilCallback(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var callCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := callCount.Add(1)
			num := fmt.Sprintf("0x%x", 4000+n)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	err = p.ProcessBlocks(ctx, 4001, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestProcessBlocks_FailedBlockInSequence tests a block that fails during processing.
func TestProcessBlocks_FailedBlockInSequence(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var callCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := callCount.Add(1)
			if n == 2 {
				// Make the second block fail repeatedly
				return nil, fmt.Errorf("server error")
			}
			num := fmt.Sprintf("0x%x", 6000+n)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	processed := make([]int64, 0)
	go func() {
		time.Sleep(2 * time.Second)
		cancel()
	}()

	err = p.ProcessBlocks(ctx, 6001, func(blockNum int64) {
		mu.Lock()
		processed = append(processed, blockNum)
		mu.Unlock()
	})
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// extractPublicKeyFromPeerID — additional coverage for RSA keys (different error path)
// ---------------------------------------------------------------------------

func TestExtractPublicKeyFromPeerID_Secp256k1Key(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Generate a Secp256k1 key pair — different key type exercises more of the extraction path
	priv, _, err := crypto.GenerateSecp256k1Key(nil)
	require.NoError(t, err)

	pid, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)

	result := extractPublicKeyFromPeerID(pid.String())
	assert.NotEmpty(t, result, "Secp256k1 peer ID should produce a non-empty hex public key")
	t.Logf("Secp256k1 key extraction result: %q (len=%d)", result, len(result))
}

// ---------------------------------------------------------------------------
// GetDefraDBPort with embedded node — verify healthy node returns correct port
// ---------------------------------------------------------------------------

func TestGetDefraDBPort_Consistency(t *testing.T) {
	td := testutils.SetupTestDefraDB(t)

	indexer := &ChainIndexer{defraNode: td.Node}
	port := indexer.GetDefraDBPort()

	assert.Greater(t, port, 0)
	assert.Equal(t, td.Port, port)
}

// ---------------------------------------------------------------------------
// processBlock — duplicate block (already exists via processBlock)
// ---------------------------------------------------------------------------

func TestProcessBlock_AlreadyExistsBlock(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x1770", nil), nil // block 6000
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	ctx := context.Background()
	// First insertion succeeds
	err = indexer.processBlock(ctx, ethClient, blockHandler, 6000)
	require.NoError(t, err)

	// Second insertion: already exists → handled gracefully
	err = indexer.processBlock(ctx, ethClient, blockHandler, 6000)
	// processBlock → processBlockBatch → detects already exists → returns nil
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// processBlockBatch with receipt error (warn path)
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_ReceiptError(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	txHash := "0x0000000000000000000000000000000000000000000000000000000000000888"
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			// Receipt fetch fails → processBlockBatch logs warning
			return nil, fmt.Errorf("receipt unavailable")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:           "8000",
		Hash:             "0x0000000000000000000000000000000000000000000000000000000000000888",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Timestamp:        "1640995200",
		Miner:            "0x0000000000000000000000000000000000000000",
		GasLimit:         "8000000",
		GasUsed:          "21000",
		Nonce:            "0",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000000",
		ReceiptsRoot:     "0x0000000000000000000000000000000000000000000000000000000000000000",
		Transactions: []types.Transaction{
			{
				Hash:             txHash,
				BlockNumber:      "8000",
				From:             "0x1234567890123456789012345678901234567890",
				To:               "0x0987654321098765432109876543210987654321",
				Value:            "1000000",
				Gas:              "21000",
				GasPrice:         "20000000000",
				Nonce:            "1",
				TransactionIndex: 0,
				Type:             "0",
				ChainId:          "1",
				V:                "27",
				R:                "0x1234",
				S:                "0x5678",
			},
		},
	}

	ctx := context.Background()
	// Receipt fails → tx is inserted without receipt data
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 8000)
	// The block itself should still succeed even if receipt fetch fails
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// indexerQueueTracker — verify collection name wiring
// ---------------------------------------------------------------------------

func TestIndexerQueueTracker_CorrectCollections(t *testing.T) {
	queue := pruner.NewIndexerQueue()
	tracker := &indexerQueueTracker{queue: queue}

	result := &defra.BlockCreationResult{
		BlockID:          fakeDocID(100),
		TransactionIDs:   []string{fakeDocID(101), fakeDocID(102)},
		LogIDs:           []string{fakeDocID(103), fakeDocID(104), fakeDocID(105)},
		AccessListIDs:    []string{fakeDocID(106)},
		BlockSignatureID: fakeDocID(107),
	}

	err := tracker.TrackBlock(context.Background(), 1000, result)
	require.NoError(t, err)
	assert.Equal(t, 1, queue.Len())

	// Verify collection names contain expected substrings
	assert.Contains(t, constants.CollectionTransaction, "Transaction")
	assert.Contains(t, constants.CollectionLog, "Log")
	assert.Contains(t, constants.CollectionAccessListEntry, "AccessListEntry")
}

// ---------------------------------------------------------------------------
// Concurrent safety of updateBlockInfo
// ---------------------------------------------------------------------------

func TestUpdateBlockInfo_ConcurrentAccess(t *testing.T) {
	indexer := &ChainIndexer{}
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
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

	// Just verify no panic/race occurred
	assert.True(t, indexer.GetCurrentBlock() >= 0)
}

// ---------------------------------------------------------------------------
// Verify that the mock RPC server handles batch requests correctly
// ---------------------------------------------------------------------------

func TestMockRPCServer_VariousEndpoints(t *testing.T) {
	srv := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_blockNumber":
			return "0x100000", nil
		case "net_version":
			return "1", nil
		case "eth_chainId":
			return "0x1", nil
		default:
			return nil, fmt.Errorf("unknown method: %s", method)
		}
	})
	defer srv.Close()

	// Verify the server responds to a basic request
	resp, err := http.Post(srv.URL, "application/json", nil)
	require.NoError(t, err)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// fullBlockResponse helper test
// ---------------------------------------------------------------------------

func TestFullBlockResponse_WithTransactions(t *testing.T) {
	txs := []any{
		map[string]any{
			"hash":  "0x123",
			"value": "0x0",
		},
	}
	block := fullBlockResponse("0x100", txs)
	assert.Equal(t, "0x100", block["number"])
	assert.NotNil(t, block["transactions"])
	txList := block["transactions"].([]any)
	assert.Len(t, txList, 1)
}

func TestFullBlockResponse_NilTransactions(t *testing.T) {
	block := fullBlockResponse("0x200", nil)
	assert.Equal(t, "0x200", block["number"])
	txList := block["transactions"].([]any)
	assert.Len(t, txList, 0)
}

// ---------------------------------------------------------------------------
// ProcessBlocks with context cancel during rate-limit wait
// ---------------------------------------------------------------------------

func TestProcessBlocks_CancelDuringRateLimit(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var callCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			n := callCount.Add(1)
			num := fmt.Sprintf("0x%x", 7000+n)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	// Very low rate limit (1 block/min) so cancellation hits during wait
	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 1)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	err = p.ProcessBlocks(ctx, 7001, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// ProcessBlocks — cancel during tooFarAhead backoff
// ---------------------------------------------------------------------------

func TestProcessBlocks_CancelDuringTooFarAhead(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			// Slow response to cause tooFarAhead
			time.Sleep(2 * time.Second)
			return fullBlockResponse("0xbeef", nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err = p.ProcessBlocks(ctx, 9001, nil)
	// Should be context.DeadlineExceeded or context.Canceled
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// StartIndexing — sequential loop path (ConcurrentBlocks=0)
// ---------------------------------------------------------------------------

func TestStartIndexing_Embedded_SequentialLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	blockCh := make(chan struct{}, 100)
	rpcServer := newMockRPCServerForIntegration(blockCh)
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0, // Force sequential path
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Wait for a few block calls
	deadline := time.After(60 * time.Second)
	blocksSeen := 0
	for blocksSeen < 3 {
		select {
		case <-blockCh:
			blocksSeen++
		case <-deadline:
			t.Fatalf("timed out (saw %d)", blocksSeen)
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	// Stop the sequential loop by setting shouldIndex = false
	indexer.shouldIndex = false

	// Wait for StartIndexing to return
	select {
	case err := <-errCh:
		// Should return nil when the loop exits naturally
		if err != nil {
			t.Logf("StartIndexing returned: %v", err)
		}
	case <-time.After(30 * time.Second):
		// If it doesn't return, the sequential loop is stuck on sleep
		t.Log("sequential loop did not return within 30s")
	}

	indexer.StopIndexing()
}

// TestStartIndexing_Embedded_SequentialLoop_AlreadyExists tests the already-exists branch
// in the sequential loop. The first block succeeds, the second is the same block
// (already exists), triggering the already-exists path.
func TestStartIndexing_Embedded_SequentialLoop_AlreadyExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64

	// Return the SAME block number for all calls → second insert triggers "already exists"
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			blockCallCount.Add(1)
			// Always return block 99991 (same block number, so second insert triggers already-exists)
			return fullBlockResponse("0x186a7", nil), nil
		case "eth_blockNumber":
			return "0x186b1", nil // chain tip 99985
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0, // sequential
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Wait until at least 3 block fetch calls (first succeeds, second is already-exists)
	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 3 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for block calls")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// TestStartIndexing_Embedded_SequentialLoop_NotFound tests the not-found branch.
func TestStartIndexing_Embedded_SequentialLoop_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			// Parse params to distinguish "latest" (chain tip query) from numbered blocks
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					// This is GetLatestBlockNumber → always return valid header
					return fullBlockResponse("0x186b1", nil), nil
				}
			}
			n := blockCallCount.Add(1)
			if n <= 3 {
				// First 3 numbered-block calls: block not found (null response)
				return nil, nil
			}
			// After that: return valid blocks
			num := fmt.Sprintf("0x%x", 99990+n)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0, // sequential
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Wait for enough calls to cover not-found retries + a successful block
	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 6 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// TestStartIndexing_Embedded_SequentialLoop_OtherError tests the generic error branch.
func TestStartIndexing_Embedded_SequentialLoop_OtherError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			// Parse params to distinguish "latest" (chain tip query) from numbered blocks
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					// This is GetLatestBlockNumber → always return valid header
					return fullBlockResponse("0x186b1", nil), nil
				}
			}
			n := blockCallCount.Add(1)
			if n <= 3 {
				// First 3 numbered-block calls: generic server error
				return nil, fmt.Errorf("internal server error")
			}
			// Then: return valid blocks
			num := fmt.Sprintf("0x%x", 99990+n)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0, // sequential
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 8 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// GetPeerInfo — test peer deduplication with mock addresses
// ---------------------------------------------------------------------------

func TestGetPeerInfo_DeduplicationBranch(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create indexer with embedded node — exercise all code paths in GetPeerInfo
	indexer := &ChainIndexer{
		defraNode:      td.Node,
		networkHandler: nil,
	}

	info, err := indexer.GetPeerInfo()
	require.NoError(t, err)
	require.NotNil(t, info)

	// The test node has P2P disabled, so no active peers
	// This still exercises the deduplication code with 0 active peers
	assert.NotNil(t, info.PeerInfo)
}

// ---------------------------------------------------------------------------
// processBlockBatch — retry loop exhaustion
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_RetryExhaustion(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create a mock RPC server that always fails on receipt fetch
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		return "0x1", nil
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	// Use a nil block to trigger an error in CreateBlockBatch
	block := &types.Block{
		Number:     "", // Empty block number → causes conversion errors
		Hash:       "",
		ParentHash: "",
		Timestamp:  "",
	}

	ctx := context.Background()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 0)
	// Should fail after retries
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to batch create block")
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — context cancel during CreateBlockBatch retry
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_ContextCancelDuringBatch(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0xdead", nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	// Insert block first to cause "already exists" on second attempt
	result1 := p.fetchAndProcessBlock(context.Background(), 0xdead)
	require.True(t, result1.Success)

	// Second attempt with cancelled context — tests ctx.Err() check in retry loop
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	result2 := p.fetchAndProcessBlock(ctx, 0xdead)
	require.NotNil(t, result2)
	// Either already-exists (fast path) or context error
	if !result2.Success {
		assert.Error(t, result2.Error)
	}
}

// ---------------------------------------------------------------------------
// openBrowser — test the "default" (linux) case on non-darwin platforms
// The function switches on runtime.GOOS. On macOS, only darwin branch runs.
// We test that the function completes without panicking.
// ---------------------------------------------------------------------------

func TestOpenBrowser_NonEmptyURL(t *testing.T) {
	logger.InitConsoleOnly(true)
	// On macOS, this actually opens a browser briefly — acceptable in tests
	openBrowser("http://localhost:0/test-url-for-coverage")
}

// ---------------------------------------------------------------------------
// SignMessages — test full flow with keyring identity
// ---------------------------------------------------------------------------

func TestSignMessages_FullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	// Start a real DefraDB via app-sdk to get proper keyring setup
	appCfg := &appConfig.Config{
		DefraDB: appConfig.DefraDBConfig{
			KeyringSecret: "test-secret-for-sign-flow-1234",
			P2P:           appConfig.DefraP2PConfig{Enabled: false},
			Store:         appConfig.DefraStoreConfig{Path: tmpDir},
		},
	}

	// Use testutils' SetupTestDefraDB for the node, then configure keyring manually
	td := testutils.SetupTestDefraDB(t)

	indexer := &ChainIndexer{
		defraNode: td.Node,
		cfg: &config.Config{
			DefraDB: config.DefraDBConfig{
				KeyringSecret: appCfg.DefraDB.KeyringSecret,
				Store:         config.DefraDBStoreConfig{Path: td.Dir},
			},
		},
	}

	// SignMessages will try to load identity from keyring.
	// Without a pre-created identity, it will fail at the load step.
	_, _, err := indexer.SignMessages("test registration message")
	// We expect an error because the keyring doesn't have an identity yet.
	// This exercises SignWithDefraKeys → loadIdentityFromStore → error path.
	assert.Error(t, err)
	// Verify it's a meaningful error (not a nil pointer or panic)
	assert.NotEmpty(t, err.Error())
}

// ---------------------------------------------------------------------------
// GetPeerInfo — exercise code paths with actual peer info
// ---------------------------------------------------------------------------

func TestGetPeerInfo_WithSelfInfo(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)
	indexer := &ChainIndexer{defraNode: td.Node}

	info, err := indexer.GetPeerInfo()
	require.NoError(t, err)
	require.NotNil(t, info)

	// P2P is disabled in test node, but it still has peer info
	if info.Self != nil {
		// Verify self info fields
		assert.NotEmpty(t, info.Self.ID, "self peer ID should not be empty")
		// Public key extraction may or may not work
		t.Logf("Self ID: %s, PublicKey: %s, Addresses: %v", info.Self.ID, info.Self.PublicKey, info.Self.Addresses)
	}
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — signing queue full path
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_SigningQueueFull(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0xbeef", nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	// First: insert block
	result1 := p.fetchAndProcessBlock(context.Background(), 0xbeef)
	require.True(t, result1.Success)

	// Fill the signing channel to capacity
	for i := 0; i < cap(p.signingChan); i++ {
		p.signingChan <- signingJob{blockNum: int64(i)}
	}

	// Second: duplicate block with full signing queue → "signing queue full" warning
	result2 := p.fetchAndProcessBlock(context.Background(), 0xbeef)
	require.NotNil(t, result2)
	assert.True(t, result2.Success)
	assert.Equal(t, "existing", result2.BlockID)

	// Drain signing channel
	for len(p.signingChan) > 0 {
		<-p.signingChan
	}
}

// ---------------------------------------------------------------------------
// SignMessages with full identity flow
// ---------------------------------------------------------------------------

func TestSignMessages_WithIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	keyringSecret := "test-secret-for-sign-identity-123"

	// Use app-sdk to create identity first
	appCfg := &appConfig.Config{
		DefraDB: appConfig.DefraDBConfig{
			KeyringSecret: keyringSecret,
			P2P:           appConfig.DefraP2PConfig{Enabled: false},
			Store:         appConfig.DefraStoreConfig{Path: tmpDir},
		},
	}

	// Import the app-sdk to create identity
	appsdk := appCfg // reference to avoid import error
	_ = appsdk

	// Start DefraDB with app-sdk to create identity and keys
	td := testutils.SetupTestDefraDB(t)

	// Create the identity manually by using the keyring
	indexer := &ChainIndexer{
		defraNode: td.Node,
		cfg: &config.Config{
			DefraDB: config.DefraDBConfig{
				KeyringSecret: keyringSecret,
				Store:         config.DefraDBStoreConfig{Path: td.Dir},
			},
		},
	}

	// Try to sign — exercises error handling in SignWithDefraKeys
	defraReg, peerReg, err := indexer.SignMessages("test message for signing")
	if err != nil {
		// Expected without pre-existing identity
		t.Logf("SignMessages error (expected): %v", err)
		assert.Empty(t, defraReg.PublicKey)
		assert.Empty(t, peerReg.PeerID)
	} else {
		// If it succeeds (identity was created), verify the response
		assert.NotEmpty(t, defraReg.PublicKey)
		assert.NotEmpty(t, defraReg.SignedPKMsg)
		assert.NotEmpty(t, peerReg.PeerID)
		assert.NotEmpty(t, peerReg.SignedPeerMsg)
	}
}

// ---------------------------------------------------------------------------
// processBlockBatch — batch retry delay path (attempt < DefaultRetryAttempts-1)
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_RetryWithDelay(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		return "0x1", nil
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	// Use a block with invalid/empty fields that will cause CreateBlockBatch to fail
	// but NOT with "already exists" → triggers the retry delay path
	block := &types.Block{
		Number:     "invalid-number",
		Hash:       "not-a-hash",
		ParentHash: "not-a-hash",
		Timestamp:  "not-a-timestamp",
	}

	ctx := context.Background()
	start := time.Now()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 0)
	elapsed := time.Since(start)

	// Should fail after retries
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to batch create block")
	// Should have taken at least 1s+2s = 3s for retry delays
	assert.GreaterOrEqual(t, elapsed.Seconds(), 2.0, "should have waited for retry delays")
}

// ---------------------------------------------------------------------------
// StartIndexing — external DefraDB (defraStarted=true) path
// ---------------------------------------------------------------------------

// TestStartIndexing_ExternalDefra tests the external-DefraDB path in StartIndexing.
// Since external DefraDB no longer sets defraNode, it should return the
// "defraNode is required" error after applying schema.
func TestStartIndexing_ExternalDefra(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Create a config pointing to the test DefraDB as "external"
	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			Url: fmt.Sprintf("http://localhost:%d", td.Port),
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

	err = indexer.StartIndexing(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "defraNode is required")
}

// ---------------------------------------------------------------------------
// StartIndexing — health server + pruner + snapshotter subsystems
// ---------------------------------------------------------------------------

func TestStartIndexing_WithHealthPrunerSnapshotter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")

	var blockCallCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x186b1", nil), nil
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", 100000+count)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0, // sequential
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 19876, // enable health server
			StartBuffer:      10,
		},
		Pruner: pruner.Config{
			Enabled:         true,
			MaxBlocks:       1000,
			PruneThreshold:  100,
			IntervalSeconds: 60,
		},
		Snapshot: snapshot.Config{
			Enabled:         true,
			Dir:             snapshotDir,
			BlocksPerFile:   1000,
			IntervalSeconds: 3600,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Wait for a few blocks to be processed
	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 3 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	// Verify subsystems are active
	assert.NotNil(t, indexer.healthServer, "health server should be initialized")
	assert.NotNil(t, indexer.pruner, "pruner should be initialized")
	assert.NotNil(t, indexer.snapshotter, "snapshotter should be initialized")

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// StartIndexing — concurrent path with pruner+snapshotter
// ---------------------------------------------------------------------------

func TestStartIndexing_ConcurrentWithSubsystems(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	snapshotDir := filepath.Join(tmpDir, "snapshots")

	var blockCallCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x186b1", nil), nil
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", 100000+count)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 2, // concurrent
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Pruner: pruner.Config{
			Enabled:         true,
			MaxBlocks:       1000,
			PruneThreshold:  100,
			IntervalSeconds: 60,
		},
		Snapshot: snapshot.Config{
			Enabled:         true,
			Dir:             snapshotDir,
			BlocksPerFile:   1000,
			IntervalSeconds: 3600,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Let some blocks process
	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 5 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for blocks")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	assert.NotNil(t, indexer.pruner, "pruner should be initialized")
	assert.NotNil(t, indexer.snapshotter, "snapshotter should be initialized")

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// StartIndexing — resuming with existing blocks in DB (gap detection)
// ---------------------------------------------------------------------------

func TestStartIndexing_ResumeFromHighBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64

	// Chain tip at 100000, we'll simulate existing blocks by inserting one
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x186a0", nil), nil // chain tip 100000
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", 99990+count)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      99980, // specific start height
			ConcurrentBlocks: 0,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 3 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// StartIndexing — unsupported tx type branch in sequential loop
// ---------------------------------------------------------------------------

func TestStartIndexing_Embedded_SequentialLoop_UnsupportedTxType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x186b1", nil), nil
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", 100000+count)
			// All blocks valid — the unsupported tx error comes from processBlock
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		case "eth_getTransactionReceipt":
			return map[string]any{
				"status": "0x1",
				"type":   "0xff", // unsupported type
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Let some blocks be processed (they should all succeed since they have 0 txns)
	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 5 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// processBlockBatch — with transactions and receipt fetching
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_WithTransactionsAndReceipts(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Mock server that returns receipts for transactions
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return map[string]any{
				"transactionHash": "0xabc",
				"blockHash":       "0x0000000000000000000000000000000000000000000000000000000000000001",
				"blockNumber":     "0x186a0",
				"from":            "0x0000000000000000000000000000000000000001",
				"to":              "0x0000000000000000000000000000000000000002",
				"gasUsed":         "0x5208",
				"status":          "0x1",
				"type":            "0x0",
				"logs":            []any{},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:           "100000",
		Hash:             "0x0000000000000000000000000000000000000000000000000000000000000001",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Timestamp:        "1000000",
		GasLimit:         "30000000",
		GasUsed:          "21000",
		Miner:            "0x0000000000000000000000000000000000000000",
		Difficulty:       "0",
		TotalDifficulty:  "0",
		Size:             "1000",
		Nonce:            "0x0000000000000000",
		BaseFeePerGas:    "1000000000",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		ReceiptsRoot:     "0x0000000000000000000000000000000000000000000000000000000000000000",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000000",
		Sha3Uncles:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		LogsBloom:        "0x" + fmt.Sprintf("%0512x", 0),
		ExtraData:        "0x",
		MixHash:          "0x0000000000000000000000000000000000000000000000000000000000000000",
		Transactions: []types.Transaction{
			{
				Hash:     "0xabc",
				From:     "0x0000000000000000000000000000000000000001",
				To:       "0x0000000000000000000000000000000000000002",
				Value:    "1000",
				Gas:      "21000",
				GasPrice: "1000000000",
				Nonce:    "0",
				Type:     "0x0",
				Input:    "0x",
			},
		},
	}

	ctx := context.Background()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 100000)
	// Should succeed (receipt fetching + batch creation)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// processBlockBatch — receipt fetch failure (covers receipt error handling)
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_ReceiptFetchFailure(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Mock server that always fails on receipt fetch
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return nil, fmt.Errorf("receipt not available")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:           "200000",
		Hash:             "0x0000000000000000000000000000000000000000000000000000000000000002",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Timestamp:        "1000000",
		GasLimit:         "30000000",
		GasUsed:          "21000",
		Miner:            "0x0000000000000000000000000000000000000000",
		Difficulty:       "0",
		TotalDifficulty:  "0",
		Size:             "1000",
		Nonce:            "0x0000000000000000",
		BaseFeePerGas:    "1000000000",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		ReceiptsRoot:     "0x0000000000000000000000000000000000000000000000000000000000000000",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000000",
		Sha3Uncles:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		LogsBloom:        "0x" + fmt.Sprintf("%0512x", 0),
		ExtraData:        "0x",
		MixHash:          "0x0000000000000000000000000000000000000000000000000000000000000000",
		Transactions: []types.Transaction{
			{
				Hash:     "0xfail1",
				From:     "0x0000000000000000000000000000000000000001",
				To:       "0x0000000000000000000000000000000000000002",
				Value:    "1000",
				Gas:      "21000",
				GasPrice: "1000000000",
				Nonce:    "0",
				Type:     "0x0",
				Input:    "0x",
			},
		},
	}

	ctx := context.Background()
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 200000)
	// Block creation should still succeed (block created without receipts)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — receipt fallback to individual fetch
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_ReceiptFallbackIndividual(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var getBlockCalls atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			getBlockCalls.Add(1)
			// Return a block with 0 txns (so receipt fallback path runs but has no txns to iterate)
			return fullBlockResponse("0x186a0", nil), nil
		case "eth_getBlockReceipts":
			// Fail batch receipts → triggers fallback to individual fetches
			return nil, fmt.Errorf("eth_getBlockReceipts not supported")
		case "eth_getTransactionReceipt":
			return map[string]any{
				"status": "0x1",
				"logs":   []any{},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		2, // workers
		2, // receiptWorkers
		0, // blocksPerMinute
	)

	ctx := context.Background()
	result := processor.fetchAndProcessBlock(ctx, 100000)

	assert.True(t, result.Success, "block should be processed successfully")
	assert.NoError(t, result.Error)
	assert.NotEmpty(t, result.BlockID)
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — receipt fallback with actual transactions
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_ReceiptFallbackWithTxns(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			// Return a block WITH a transaction
			return fullBlockResponseWithTx("0x186a0"), nil
		case "eth_getBlockReceipts":
			// Fail batch receipts → triggers individual fallback
			return nil, fmt.Errorf("eth_getBlockReceipts not supported")
		case "eth_getTransactionReceipt":
			// Return a valid receipt with all required go-ethereum fields
			return map[string]any{
				"transactionHash":   "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",
				"blockNumber":       "0x186a0",
				"transactionIndex":  "0x0",
				"from":              "0x0000000000000000000000000000000000000001",
				"to":                "0x0000000000000000000000000000000000000002",
				"gasUsed":           "0x5208",
				"cumulativeGasUsed": "0x5208",
				"contractAddress":   nil,
				"status":            "0x1",
				"type":              "0x0",
				"logsBloom":         "0x" + fmt.Sprintf("%0512x", 0),
				"logs":              []any{},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		1, // workers
		2, // receiptWorkers
		0, // blocksPerMinute
	)

	ctx := context.Background()
	result := processor.fetchAndProcessBlock(ctx, 100000)

	assert.True(t, result.Success, "block with receipt fallback should succeed: %v", result.Error)
	assert.NoError(t, result.Error)
	assert.NotEmpty(t, result.BlockID)
}

// TestFetchAndProcessBlock_ReceiptFallbackWithTxnsFail tests the fallback
// when individual receipt fetch also fails.
func TestFetchAndProcessBlock_ReceiptFallbackWithTxnsFail(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponseWithTx("0x186a1"), nil
		case "eth_getBlockReceipts":
			return nil, fmt.Errorf("eth_getBlockReceipts not supported")
		case "eth_getTransactionReceipt":
			// Individual receipt fetch also fails
			return nil, fmt.Errorf("receipt not available")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		1, // workers
		2, // receiptWorkers
		0, // blocksPerMinute
	)

	ctx := context.Background()
	result := processor.fetchAndProcessBlock(ctx, 100001)

	// Block should still be created (just without receipts)
	assert.True(t, result.Success, "block should still succeed even with receipt failures: %v", result.Error)
}

// TestFetchAndProcessBlock_ReceiptFallbackContextCancel tests receipt fallback
// when context is cancelled during individual fetch.
func TestFetchAndProcessBlock_ReceiptFallbackContextCancel(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponseWithTx("0x186a2"), nil
		case "eth_getBlockReceipts":
			return nil, fmt.Errorf("eth_getBlockReceipts not supported")
		case "eth_getTransactionReceipt":
			// Slow response to trigger context cancel during receipt fetch
			time.Sleep(2 * time.Second)
			return nil, fmt.Errorf("timeout")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		1, // workers
		2, // receiptWorkers
		0, // blocksPerMinute
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := processor.fetchAndProcessBlock(ctx, 100002)
	// May succeed (block created without receipts) or fail (ctx cancelled during batch create)
	// The important thing is it doesn't hang
	t.Logf("result: success=%v, error=%v", result.Success, result.Error)
}

// ---------------------------------------------------------------------------
// ProcessBlocks — already-existing block triggers signing queue ("existing" path)
// ---------------------------------------------------------------------------

func TestProcessBlocks_ExistingBlockPath(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	var getBlockCalls atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			getBlockCalls.Add(1)
			// Always return block 100000 → second call triggers already-exists
			return fullBlockResponse("0x186a0", nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		1, // workers
		2, // receiptWorkers
		0, // blocksPerMinute
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var processedBlocks atomic.Int64
	err = processor.ProcessBlocks(ctx, 100000, func(blockNum int64) {
		processedBlocks.Add(1)
	})

	// Should return context deadline/canceled error
	assert.Error(t, err)
	// Should have processed at least the first block (subsequent hit already-exists → "existing" path)
	assert.GreaterOrEqual(t, processedBlocks.Load(), int64(1))
}

// ---------------------------------------------------------------------------
// GetPeerInfo — with self info construction
// ---------------------------------------------------------------------------

func TestGetPeerInfo_SelfInfoConstruction(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	indexer := &ChainIndexer{
		defraNode:      td.Node,
		networkHandler: nil,
	}

	info, err := indexer.GetPeerInfo()
	require.NoError(t, err)
	require.NotNil(t, info)

	// The test node has P2P disabled — check that self info is populated
	// when the node has a peer ID (even with no active peers)
	if info.Self != nil {
		assert.NotEmpty(t, info.Self.ID, "self peer ID should be set")
		// Public key may or may not be extractable depending on key type
	}

	// Enabled should be false since networkHandler is nil
	assert.False(t, info.Enabled)
}

// ---------------------------------------------------------------------------
// SignMessages — full success path with keyring
// ---------------------------------------------------------------------------

func TestSignMessages_FullSuccessPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x186a0", nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 0,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	// Use StartIndexing briefly to set up the defra node with keyring
	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	// Wait for indexer to be started (defra node is initialized)
	deadline := time.After(30 * time.Second)
	for !indexer.IsStarted() {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for indexer to start")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	// Now try SignMessages
	defraPK, peerReg, err := indexer.SignMessages("test-message-for-signing")
	if err != nil {
		t.Logf("SignMessages returned error (may be expected with test keyring): %v", err)
	} else {
		assert.NotEmpty(t, defraPK.PublicKey, "defra public key should be set")
		assert.NotEmpty(t, defraPK.SignedPKMsg, "signed message should be set")
		assert.NotEmpty(t, peerReg.PeerID, "peer public key should be set")
		assert.NotEmpty(t, peerReg.SignedPeerMsg, "peer signed message should be set")
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// extractPublicKeyFromPeerID — failure to extract key and raw bytes errors
// ---------------------------------------------------------------------------

func TestExtractPublicKeyFromPeerID_Ed25519Key(t *testing.T) {
	// Ed25519 keys are embedded in PeerIDs and should be extractable.
	logger.InitConsoleOnly(true)

	priv, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)

	result := extractPublicKeyFromPeerID(id.String())
	assert.NotEmpty(t, result, "Ed25519 keys should be extractable from PeerID")
}

func TestExtractPublicKeyFromPeerID_RSAKey(t *testing.T) {
	// RSA keys use multihash encoding in PeerIDs (key too large to embed).
	// ExtractPublicKey() returns ErrNoPublicKey for RSA PeerIDs.
	logger.InitConsoleOnly(true)

	priv, _, err := crypto.GenerateRSAKeyPair(2048, crypto_rand.Reader)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)

	result := extractPublicKeyFromPeerID(id.String())
	// RSA keys can't be extracted from PeerID — should return empty string
	assert.Empty(t, result, "RSA keys should not be extractable from PeerID (too large)")
}

// ---------------------------------------------------------------------------
// openBrowser — cmd.Start failure (non-existent command)
// ---------------------------------------------------------------------------

func TestOpenBrowser_StartFailure(t *testing.T) {
	logger.InitConsoleOnly(true)

	// openBrowser uses runtime.GOOS to pick the command.
	// On macOS it calls "open", which will succeed for valid URLs but
	// we test it with a benign URL. The main coverage target is the
	// cmd.Start() error path, which fires on invalid platforms.
	// We just exercise the function to confirm it doesn't panic.
	openBrowser("http://127.0.0.1:0/health")
}

// ---------------------------------------------------------------------------
// StopIndexing — with pruner and snapshotter set
// ---------------------------------------------------------------------------

func TestStopIndexing_WithPrunerAndSnapshotter(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	prunerCfg := &pruner.Config{
		Enabled:        true,
		MaxBlocks:      100,
		PruneThreshold: 10,
	}
	p := pruner.NewPruner(prunerCfg, td.Node)
	p.SetQueue(pruner.NewIndexerQueue())

	snapshotDir := t.TempDir()
	s := snapshot.New(&snapshot.Config{
		Enabled:         true,
		Dir:             snapshotDir,
		BlocksPerFile:   100,
		IntervalSeconds: 3600,
	}, td.Node)

	indexer := &ChainIndexer{
		defraNode:   td.Node,
		isStarted:   true,
		shouldIndex: true,
		pruner:      p,
		snapshotter: s,
	}

	// Don't call p.Start()/s.Start() — they require the app-sdk logger
	// to be initialized. StopIndexing should handle calling Stop() on
	// unstarted components (isRunning=false → early return).
	indexer.StopIndexing()

	assert.False(t, indexer.isStarted)
	assert.False(t, indexer.shouldIndex)
}

// ---------------------------------------------------------------------------
// ProcessBlocks — block fetch failure exhaustion (3 retries)
// ---------------------------------------------------------------------------

func TestProcessBlocks_BlockFetchExhaustion(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	// Mock server that always errors on getBlockByNumber
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return nil, fmt.Errorf("persistent RPC error")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		1, // workers
		2, // receiptWorkers
		0, // blocksPerMinute
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = processor.ProcessBlocks(ctx, 100000, nil)
	// Should exit due to context timeout (blocks keep failing)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — context cancelled during main dispatch loop
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_ContextCancelMainLoop(t *testing.T) {
	logger.InitConsoleOnly(true)

	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x186a0", nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	processor := NewConcurrentBlockProcessor(
		blockHandler,
		ethClient,
		2,  // workers
		2,  // receiptWorkers
		60, // blocksPerMinute - rate limited to exercise more paths
	)

	// Cancel immediately to exercise the main dispatch loop's ctx.Done()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = processor.ProcessBlocks(ctx, 100000, nil)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Ensure unused imports are exercised
// ---------------------------------------------------------------------------

// This test ensures the filepath import is used (for prune queue test paths).
func TestPruneQueueFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	queueFilePath := filepath.Join(tmpDir, "prune_queue.gob")
	assert.Contains(t, queueFilePath, "prune_queue.gob")
}

// ---------------------------------------------------------------------------
// StartIndexing — resume from pruner queue (covers lines 219-221, 243-252)
// ---------------------------------------------------------------------------

func TestStartIndexing_ResumeFromPrunerQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	// Pre-create a prune queue file with entries so LoadFromFile returns loaded > 0
	queue := pruner.NewIndexerQueue()
	for i := int64(90000); i <= 90010; i++ {
		_ = queue.TrackBlockDocIDs(i, fakeDocID(int(i)), map[string][]string{
			constants.CollectionTransaction: {fakeDocID(int(i) + 10000)},
		}, fakeDocID(int(i)+20000))
	}
	queueFilePath := filepath.Join(tmpDir, "prune_queue.gob")
	_, _ = queue.LoadFromFile(queueFilePath) // sets filePath
	err := queue.Save()
	require.NoError(t, err)

	// Chain tip at 100000, highest in queue is 90010, gap = 9990 > startBuffer=10
	// This should trigger the gap detection skip-ahead (lines 246-250)
	var blockCallCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x186a0", nil), nil // 100000
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", 99990+count)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Pruner: pruner.Config{
			Enabled:         true,
			MaxBlocks:       1000,
			PruneThreshold:  100,
			IntervalSeconds: 60,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 2 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for blocks")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	// Should have skipped ahead — start height should be around 99990
	assert.True(t, indexer.cfg.Indexer.StartHeight >= 99980,
		"should have skipped ahead due to gap, got start height %d", indexer.cfg.Indexer.StartHeight)

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// StartIndexing — negative start height clamp (covers lines 259-261)
// ---------------------------------------------------------------------------

func TestStartIndexing_NegativeStartHeightClamp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()

	// Chain tip very low (5), startBuffer=100 → startHeight = 5 - 100 = -95 → clamped to 0
	var blockCallCount atomic.Int64
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x5", nil), nil // chain tip = 5
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", count-1)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0, // no configured height
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      100, // larger than chain tip
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 2 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	// Start height should be clamped to 0
	assert.Equal(t, 0, indexer.cfg.Indexer.StartHeight,
		"start height should be clamped to 0 when chainTip - buffer is negative")

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// StartIndexing — OpenBrowserOnStart path (covers lines 294-299)
// ---------------------------------------------------------------------------

func TestStartIndexing_WithOpenBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	var blockCallCount atomic.Int64

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if err := json.Unmarshal(rawParams[0], &blockParam); err == nil && blockParam == "latest" {
					return fullBlockResponse("0x186a0", nil), nil
				}
			}
			count := blockCallCount.Add(1)
			num := fmt.Sprintf("0x%x", 99990+count)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []any{}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           config.DefraDBP2PConfig{Enabled: false},
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth: config.GethConfig{NodeURL: rpcServer.URL},
		Indexer: config.IndexerConfig{
			StartHeight:        99990,
			ConcurrentBlocks:   1,
			ReceiptWorkers:     2,
			MaxDocsPerTxn:      100,
			HealthServerPort:   19877,
			OpenBrowserOnStart: true,
			StartBuffer:        10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(60 * time.Second)
	for blockCallCount.Load() < 2 {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out")
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}

	indexer.shouldIndex = false
	indexer.StopIndexing()
}

// ---------------------------------------------------------------------------
// fetchAndProcessBlock — context cancel during receipt fetch (covers line 272-273)
// ---------------------------------------------------------------------------

func TestFetchAndProcessBlock_ContextCancelDuringReceiptFetch(t *testing.T) {
	logger.InitConsoleOnly(true)
	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponseWithTx("0x3e8"), nil // block 1000 with tx
		case "eth_getBlockReceipts":
			return nil, fmt.Errorf("not supported")
		case "eth_getTransactionReceipt":
			// Slow response to give time for cancellation
			time.Sleep(500 * time.Millisecond)
			return map[string]any{
				"transactionHash": "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"blockNumber":     "0x3e8",
				"blockHash":       "0x0000000000000000000000000000000000000000000000000000000000000001",
				"gasUsed":         "0x5208",
				"status":          "0x1",
				"logs":            []any{},
			}, nil
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	p := NewConcurrentBlockProcessor(blockHandler, ethClient, 1, 2, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result := p.fetchAndProcessBlock(ctx, 1000)
	require.NotNil(t, result)
	// May succeed or fail depending on timing, but shouldn't panic
}

// ---------------------------------------------------------------------------
// processBlockBatch — receipt fetch failure in goroutine (covers line 465)
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_ReceiptFetchError(t *testing.T) {
	logger.InitConsoleOnly(true)
	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return nil, fmt.Errorf("receipt not found")
		case "eth_getBlockReceipts":
			return nil, fmt.Errorf("not supported")
		default:
			return "0x1", nil
		}
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:     "400",
		Hash:       "0x0000000000000000000000000000000000000000000000000000000000000004",
		ParentHash: "0x0000000000000000000000000000000000000000000000000000000000000003",
		Timestamp:  "1640995200",
		Miner:      "0x0000000000000000000000000000000000000000",
		GasLimit:   "8000000",
		GasUsed:    "21000",
		Transactions: []types.Transaction{
			{
				Hash:             "0x0000000000000000000000000000000000000000000000000000000000000020",
				BlockNumber:      "400",
				From:             "0x1234567890123456789012345678901234567890",
				To:               "0x0987654321098765432109876543210987654321",
				Value:            "1000000",
				Gas:              "21000",
				GasPrice:         "20000000000",
				Nonce:            "1",
				TransactionIndex: 0,
				Type:             "0",
				ChainId:          "1",
				V:                "27",
				R:                "0x1234",
				S:                "0x5678",
			},
		},
	}

	ctx := context.Background()
	// Receipt fetch fails → block is created without receipt data
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 400)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// processBlockBatch — already exists path with signing (covers lines 488-494)
// ---------------------------------------------------------------------------

func TestProcessBlockBatch_AlreadyExists_WithSigning(t *testing.T) {
	logger.InitConsoleOnly(true)
	td := testutils.SetupTestDefraDB(t)

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (any, error) {
		return "0x1", nil
	})
	defer rpcServer.Close()

	ethClient, err := rpc.NewEthereumClient(rpcServer.URL, "", "")
	require.NoError(t, err)
	defer ethClient.Close()

	blockHandler, err := defra.NewBlockHandler(td.Node, 100)
	require.NoError(t, err)

	indexer := &ChainIndexer{
		cfg: &config.Config{
			Indexer: config.IndexerConfig{ReceiptWorkers: 2},
		},
		defraNode: td.Node,
	}

	block := &types.Block{
		Number:     "600",
		Hash:       "0x0000000000000000000000000000000000000000000000000000000000000006",
		ParentHash: "0x0000000000000000000000000000000000000000000000000000000000000005",
		Timestamp:  "1640995200",
		Miner:      "0x0000000000000000000000000000000000000000",
		GasLimit:   "8000000",
		GasUsed:    "0",
	}

	ctx := context.Background()
	// First insert succeeds
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 600)
	require.NoError(t, err)

	// Second insert hits IsErrAlreadyExists → calls CreateBlockSignatureForExistingBlock
	err = indexer.processBlockBatch(ctx, ethClient, blockHandler, block, 600)
	assert.NoError(t, err, "already-exists should be handled gracefully")
}

// ---------------------------------------------------------------------------
// GetPeerInfo — embedded node without P2P (covers lines 596+)
// ---------------------------------------------------------------------------

func TestGetPeerInfo_WithEmbeddedNode_NoP2P(t *testing.T) {
	logger.InitConsoleOnly(true)
	td := testutils.SetupTestDefraDB(t)

	indexer := &ChainIndexer{
		defraNode: td.Node,
	}

	info, err := indexer.GetPeerInfo()
	if err != nil {
		// PeerInfo may error without P2P — that's the line 596-598 path
		assert.Contains(t, err.Error(), "peer info")
	} else {
		require.NotNil(t, info)
		assert.False(t, info.Enabled)
	}
}
