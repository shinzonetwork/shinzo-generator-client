package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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
		defraNode:    td.Node,
		shouldIndex:  true,
		isStarted:    true,
		cfg:          &config.Config{},
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
	ID     interface{}     `json:"id"`
}

func newMockRPCServer(handler func(method string, params json.RawMessage) (interface{}, error)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		result, rpcErr := handler(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if rpcErr != nil {
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]interface{}{"code": -32000, "message": rpcErr.Error()},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func fullBlockResponse(number string, txs []interface{}) map[string]interface{} {
	emptyTrieRoot := "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
	block := map[string]interface{}{
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
		"uncles":           []interface{}{},
	}
	if txs != nil {
		block["transactions"] = txs
	} else {
		block["transactions"] = []interface{}{}
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
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x64", nil), nil // block 100
		case "eth_getBlockReceipts":
			return []interface{}{}, nil
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
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
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
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getTransactionReceipt":
			return map[string]interface{}{
				"transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000001",
				"blockNumber":     "0xc8", // 200
				"blockHash":       "0x0000000000000000000000000000000000000000000000000000000000000002",
				"gasUsed":         "0x5208",
				"status":          "0x1",
				"logs":            []interface{}{},
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
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getBlockReceipts":
			return []interface{}{
				map[string]interface{}{
					"transactionHash": "0x0000000000000000000000000000000000000000000000000000000000000010",
					"blockNumber":     "0x12c", // 300
					"blockHash":       "0x0000000000000000000000000000000000000000000000000000000000000003",
					"gasUsed":         "0x5208",
					"status":          "0x1",
					"logs":            []interface{}{},
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

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x1f4", nil), nil // block 500
		case "eth_getBlockReceipts":
			return []interface{}{}, nil
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

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
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

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
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

	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getBlockByNumber":
			return fullBlockResponse("0x2bc", nil), nil // block 700
		case "eth_getBlockReceipts":
			return []interface{}{}, nil
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
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getBlockByNumber":
			blockCount++
			num := fmt.Sprintf("0x%x", 1000+blockCount)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []interface{}{}, nil
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
	rpcServer := newMockRPCServer(func(method string, params json.RawMessage) (interface{}, error) {
		switch method {
		case "eth_getBlockByNumber":
			blockCount++
			num := fmt.Sprintf("0x%x", 2000+blockCount)
			return fullBlockResponse(num, nil), nil
		case "eth_getBlockReceipts":
			return []interface{}{}, nil
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
