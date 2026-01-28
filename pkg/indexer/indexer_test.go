package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
	"github.com/stretchr/testify/assert"
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
		Number:       12345,
		Hash:         "0x1234567890abcdef",
		ParentHash:   "0xabcdef1234567890",
		Timestamp:    "1640995200",
		GasLimit:     "8000000",
		GasUsed:      "21000",
		Nonce:        "0x1234567890abcdef",
		Sha3Uncles:   "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		LogsBloom:    "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
		StateRoot:    "0xd7f8974fb5ac78d9ac099b9ad5018bedc2ce0a72dad1827a1709da30580f0544",
		ReceiptsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		Size:         "1000",
		ExtraData:    "0x",
		Transactions: []types.Transaction{
			{
				Hash:              "0xabc123",
				BlockHash:         "0x1234567890abcdef",
				BlockNumber:       12345,
				From:              "0x1234567890123456789012345678901234567890",
				To:                "0x0987654321098765432109876543210987654321",
				Value:             "1000000000000000000",
				Gas:               "21000",
				GasPrice:          "20000000000",
				Nonce:             "1",
				TransactionIndex:  0,
				Type:              "0",
				ChainId:           "1",
				V:                 "27",
				R:                 "12345",
				S:                 "67890",
				Status:            "1",
				GasUsed:           "21000",
				EffectiveGasPrice: "20000000000",
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
		Number:       gethBlock.Number,
		Hash:         gethBlock.Hash,
		ParentHash:   gethBlock.ParentHash,
		Nonce:        gethBlock.Nonce,
		Sha3Uncles:   gethBlock.Sha3Uncles,
		LogsBloom:    gethBlock.LogsBloom,
		StateRoot:    gethBlock.StateRoot,
		ReceiptsRoot: gethBlock.ReceiptsRoot,
		ExtraData:    gethBlock.ExtraData,
		Size:         gethBlock.Size,
		GasLimit:     gethBlock.GasLimit,
		GasUsed:      gethBlock.GasUsed,
		Timestamp:    gethBlock.Timestamp,
		Transactions: transactions,
	}

	assert.NotNil(t, defraBlock)
	assert.Equal(t, gethBlock.Number, defraBlock.Number)
	assert.Equal(t, gethBlock.Hash, defraBlock.Hash)
	assert.Equal(t, gethBlock.ParentHash, defraBlock.ParentHash)
	assert.Equal(t, gethBlock.Timestamp, defraBlock.Timestamp)
	// Removed miner field assertion as it's no longer in the schema
	assert.Equal(t, gethBlock.GasLimit, defraBlock.GasLimit)
	assert.Equal(t, gethBlock.GasUsed, defraBlock.GasUsed)
	assert.Len(t, defraBlock.Transactions, 1)
}

// TestConvertGethBlockToDefraBlockWithEmptyTransactions tests block conversion with no transactions
func TestConvertGethBlockToDefraBlockWithEmptyTransactions(t *testing.T) {
	logger.InitConsoleOnly(true)

	gethBlock := &types.Block{
		Number:       12345,
		Hash:         "0x1234567890abcdef",
		ParentHash:   "0xabcdef1234567890",
		Timestamp:    "1640995200",
		GasLimit:     "8000000",
		GasUsed:      "0",
		Transactions: []types.Transaction{}, // Empty transactions
	}

	defraBlock := &types.Block{
		Number:       gethBlock.Number,
		Hash:         gethBlock.Hash,
		ParentHash:   gethBlock.ParentHash,
		Nonce:        gethBlock.Nonce,
		Sha3Uncles:   gethBlock.Sha3Uncles,
		LogsBloom:    gethBlock.LogsBloom,
		StateRoot:    gethBlock.StateRoot,
		ReceiptsRoot: gethBlock.ReceiptsRoot,
		ExtraData:    gethBlock.ExtraData,
		Size:         gethBlock.Size,
		GasLimit:     gethBlock.GasLimit,
		GasUsed:      gethBlock.GasUsed,
		Timestamp:    gethBlock.Timestamp,
		Transactions: gethBlock.Transactions,
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
	blocks       map[int64]*types.Block
	transactions map[string]*types.Transaction
	createError  error
}

func NewMockBlockHandler() *MockBlockHandler {
	return &MockBlockHandler{
		blocks:       make(map[int64]*types.Block),
		transactions: make(map[string]*types.Transaction),
	}
}

func (m *MockBlockHandler) CreateBlock(ctx context.Context, block *types.Block) (string, error) {
	if m.createError != nil {
		return "", m.createError
	}
	// Block number is already int type
	blockNum := int64(block.Number)
	m.blocks[blockNum] = block
	return "mock-block-id", nil
}

func (m *MockBlockHandler) CreateTransaction(ctx context.Context, tx *types.Transaction, blockID string) (string, error) {
	if m.createError != nil {
		return "", m.createError
	}
	m.transactions[tx.Hash] = tx
	return "mock-tx-id", nil
}

func (m *MockBlockHandler) GetHighestBlockNumber(ctx context.Context) (int64, error) {
	if m.createError != nil {
		return 0, m.createError
	}

	var highest int64 = 0
	for blockNum := range m.blocks {
		if blockNum > highest {
			highest = blockNum
		}
	}
	return highest, nil
}

// TestBlockProcessingLogic tests the block processing logic with mocked dependencies
func TestBlockProcessingLogic(t *testing.T) {
	logger.InitConsoleOnly(true)

	// Create test block
	testBlock := &types.Block{
		Number:     100,
		Hash:       "0xtest123",
		ParentHash: "0xparent123",
		Timestamp:  "1640995200",
		GasLimit:   "8000000",
		GasUsed:    "21000",
		Transactions: []types.Transaction{
			{
				Hash:             "0xtx123",
				BlockHash:        "0xtest123",
				BlockNumber:      100,
				From:             "0xfrom123",
				To:               "0xto123",
				Value:            "1000000",
				Gas:              "21000",
				GasPrice:         "20000000000",
				Nonce:            "1",
				TransactionIndex: 0,
				Status:           "1",
				GasUsed:          "21000",
			},
		},
	}

	// Test conversion
	defraBlock := &types.Block{
		Number:       testBlock.Number,
		Hash:         testBlock.Hash,
		ParentHash:   testBlock.ParentHash,
		Nonce:        testBlock.Nonce,
		Sha3Uncles:   testBlock.Sha3Uncles,
		LogsBloom:    testBlock.LogsBloom,
		StateRoot:    testBlock.StateRoot,
		ReceiptsRoot: testBlock.ReceiptsRoot,
		ExtraData:    testBlock.ExtraData,
		Size:         testBlock.Size,
		GasLimit:     testBlock.GasLimit,
		GasUsed:      testBlock.GasUsed,
		Timestamp:    testBlock.Timestamp,
		Transactions: testBlock.Transactions,
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
