package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
)

var (
	liveGraphqlURL = "" // Will be set dynamically when DefraDB starts
	liveDefraURL   = "" // Will be set dynamically when DefraDB starts
)

var (
	indexerStarted   = false
	indexerCtx       context.Context
	indexerCancel    context.CancelFunc
	liveChainIndexer *indexer.ChainIndexer
)

// TestMain sets up and tears down the live integration test environment
func TestMain(m *testing.M) {
	// Initialize logger for live integration tests first
	logger.InitConsoleOnly(true)
	logger.Test("TestMain - Starting live integration tests with real Ethereum data")

	// Check required environment variables
	if !checkRequiredEnvVars() {
		logger.Sugar.Error("Required environment variables not set. Set GETH_RPC_URL, GETH_WS_URL, and GETH_API_KEY")
		os.Exit(0) // treat as skipped instead of failed
	}

	// Clean up any existing live integration DefraDB data
	logger.Test("Cleaning up existing live integration DefraDB data...")
	if err := os.RemoveAll("./.defra"); err != nil {
		logger.Sugar.Warnf("Failed to clean existing live data: %v", err)
	}

	// Start live indexer with real Ethereum connections
	logger.Test("Starting live indexer with real Ethereum connections...")
	indexerCtx, indexerCancel = context.WithCancel(context.Background())
	go func() {
		// Load config for live testing
		cfg, err := config.LoadConfig("../../config/config.yaml")
		if err != nil {
			logger.Sugar.Errorf("Failed to load config: %v", err)
			return
		}

		// Override DefraDB store path for live testing
		cfg.DefraDB.Store.Path = "./.defra"

		// Override Geth config with environment variables for live testing
		cfg.Geth.NodeURL = os.Getenv("GETH_RPC_URL")
		cfg.Geth.WsURL = os.Getenv("GETH_WS_URL")
		cfg.Geth.APIKey = os.Getenv("GETH_API_KEY")

		// Start indexer with real connections - should succeed if env vars are set
		liveChainIndexer, err = indexer.CreateIndexer(cfg)
		if err != nil {
			logger.Sugar.Errorf("create indexer failed: %v", err)
			return
		}

		// Start indexer in background
		go func() {
			logger.Test("Starting indexer...")
			err = liveChainIndexer.StartIndexing(false) // false = start embedded DefraDB
			if err != nil {
				logger.Sugar.Errorf("Live indexer failed: %v", err)
				return
			}
			logger.Test("Indexer started successfully")
		}()

		// Wait for at least one block to be indexed (proves everything works)
		logger.Test("Waiting for blocks to be indexed...")
		if !waitForAnyBlock(60 * time.Second) {
			logger.Sugar.Error("No blocks were indexed - test failed")
			os.Exit(1)
		}

		logger.Test("✅ Live integration test passed - blocks are being indexed!")
		indexerStarted = true
	}()

	// Run tests
	result := m.Run()

	// Teardown
	logger.Test("TestMain - Live integration teardown")
	if liveChainIndexer != nil {
		liveChainIndexer.StopIndexing()
	}

	// Clean up test data
	time.Sleep(2 * time.Second) // Give time for cleanup
	os.RemoveAll("./.defra")

	os.Exit(result)
}

// checkRequiredEnvVars checks if all required environment variables are set for live testing
func checkRequiredEnvVars() bool {
	requiredVars := []string{"GETH_RPC_URL", "GETH_WS_URL", "GETH_API_KEY"}

	for _, envVar := range requiredVars {
		if os.Getenv(envVar) == "" {
			logger.Sugar.Errorf("❌ Missing required environment variable: %s", envVar)
			return false
		}
	}

	logger.Test("✓ All required environment variables are set")
	return true
}

// waitForAnyBlock waits for at least one block to be indexed
func waitForAnyBlock(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Try multiple approaches to find DefraDB
		var testURLs []string

		// Try to get the port directly from the indexer's embedded DefraDB
		if liveChainIndexer != nil {
			port := liveChainIndexer.GetDefraDBPort()
			if port > 0 {
				testURLs = append(testURLs, fmt.Sprintf("http://localhost:%d", port))
			}
		}

		// Try common DefraDB ports
		commonPorts := []int{9181, 9180, 9182}
		for _, port := range commonPorts {
			testURLs = append(testURLs, fmt.Sprintf("http://localhost:%d", port))
		}

		// Test each URL for blocks
		for _, testURL := range testURLs {
			query := `{"query":"{ Block { _count } }"}`
			client := &http.Client{Timeout: 2 * time.Second}

			req, err := http.NewRequest("POST", testURL+"/api/v0/graphql", strings.NewReader(query))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				if resp, err := client.Do(req); err == nil {
					if resp.StatusCode == 200 {
						body, err := io.ReadAll(resp.Body)
						resp.Body.Close()
						if err == nil {
							var result map[string]any
							if json.Unmarshal(body, &result) == nil {
								if data, ok := result["data"].(map[string]any); ok {
									if block, ok := data[constants.CollectionBlock].(map[string]any); ok {
										if count, ok := block["_count"].(float64); ok && count > 0 {
											logger.Test(fmt.Sprintf("✅ Found %v blocks indexed at %s", count, testURL))
											liveDefraURL = testURL
											liveGraphqlURL = testURL + "/api/v0/graphql"
											return true
										}
									}
								}
							}
						}
					} else {
						resp.Body.Close()
					}
				}
			}
		}

		time.Sleep(2 * time.Second)
	}
	return false
}

// testLiveDefraDBConnection tests if DefraDB is responding
func testLiveDefraDBConnection() bool {
	if liveDefraURL == "" {
		return false
	}
	resp, err := http.Get(liveDefraURL + "/api/v0/schema")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// waitForLiveBlocks waits for live blocks to be indexed
func waitForLiveBlocks(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hasLiveBlocks() {
			return true
		}
		time.Sleep(5 * time.Second) // Check every 5 seconds for live data
	}
	return false
}

// hasLiveBlocks checks if any blocks have been indexed from live Ethereum
func hasLiveBlocks() bool {
	if liveGraphqlURL == "" {
		return false
	}
	query := `{"query":"query { Block(limit: 1) { number hash } }"}`
	resp, err := http.Post(liveGraphqlURL, "application/json", bytes.NewBuffer([]byte(query)))
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		return false
	}

	blocks, ok := data[constants.CollectionBlock].([]any)
	return ok && len(blocks) > 0
}

// TestLiveEthereumConnection tests that the indexer can connect to real Ethereum
func TestLiveEthereumConnection(t *testing.T) {
	if !indexerStarted {
		t.Skip("Live indexer not started - skipping live tests")
	}

	logger.Test("Testing live Ethereum connection and block indexing")

	// Check that we have live blocks
	if !hasLiveBlocks() {
		t.Fatal("No live blocks found - indexer may not be connected to Ethereum")
	}

	logger.Test("✓ Live Ethereum connection successful - blocks are being indexed")
}

// TestLiveGetLatestBlocks tests querying latest blocks from live data
func TestLiveGetLatestBlocks(t *testing.T) {
	if !indexerStarted {
		t.Skip("Live indexer not started - skipping live tests")
	}

	query := `{"query":"query { Block(limit: 5, order: {number: DESC}) { number hash timestamp gasUsed gasLimit miner } }"}`

	resp, err := http.Post(liveGraphqlURL, "application/json", bytes.NewBuffer([]byte(query)))
	if err != nil {
		t.Fatalf("Failed to query live blocks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GraphQL query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatal("No data in response")
	}

	blocks, ok := data[constants.CollectionBlock].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatal("No blocks returned from live query")
	}

	logger.Testf("✓ Successfully queried %d live blocks", len(blocks))

	// Validate block structure
	firstBlock := blocks[0].(map[string]any)
	requiredFields := []string{"number", "hash", "timestamp", "gasUsed", "gasLimit", "miner"}

	for _, field := range requiredFields {
		if _, exists := firstBlock[field]; !exists {
			t.Errorf("Missing required field '%s' in live block", field)
		}
	}
}

// TestLiveGetTransactions tests querying transactions from live data
func TestLiveGetTransactions(t *testing.T) {
	if !indexerStarted {
		t.Skip("Live indexer not started - skipping live tests")
	}

	query := `{"query":"query { Transaction(limit: 3) { hash blockNumber from to value gas gasPrice gasUsed status } }"}`

	resp, err := http.Post(liveGraphqlURL, "application/json", bytes.NewBuffer([]byte(query)))
	if err != nil {
		t.Fatalf("Failed to query live transactions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Transaction query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode transaction response: %v", err)
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatal("No data in transaction response")
	}

	transactions, ok := data[constants.CollectionTransaction].([]any)
	if !ok {
		logger.Test("No transactions found in live data (this may be normal if blocks have no transactions)")
		return
	}

	logger.Testf("✓ Successfully queried %d live transactions", len(transactions))

	if len(transactions) > 0 {
		// Validate transaction structure
		firstTx := transactions[0].(map[string]any)
		requiredFields := []string{"hash", "blockNumber", "from", "to", "value", "gas", "gasPrice"}

		for _, field := range requiredFields {
			if _, exists := firstTx[field]; !exists {
				t.Errorf("Missing required field '%s' in live transaction", field)
			}
		}
	}
}

// TestLiveBlockTransactionRelationship tests the relationship between blocks and transactions in live data
func TestLiveBlockTransactionRelationship(t *testing.T) {
	if !indexerStarted {
		t.Skip("Live indexer not started - skipping live tests")
	}

	// Get a block with its transactions
	query := `{"query":"query { Block(limit: 1, filter: {}) { number hash transactions { hash blockNumber from to } } }"}`

	resp, err := http.Post(liveGraphqlURL, "application/json", bytes.NewBuffer([]byte(query)))
	if err != nil {
		t.Fatalf("Failed to query live block with transactions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Block-transaction query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode block-transaction response: %v", err)
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatal("No data in block-transaction response")
	}

	blocks, ok := data[constants.CollectionBlock].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatal("No blocks returned from live block-transaction query")
	}

	block := blocks[0].(map[string]any)
	blockNumber := block["number"]

	// Check if block has transactions
	transactions, hasTransactions := block["transactions"].([]any)
	if !hasTransactions {
		logger.Test("Block has no transactions (this may be normal)")
		return
	}

	logger.Testf("✓ Block %v has %d transactions", blockNumber, len(transactions))

	// Validate that transaction blockNumbers match the block number
	for i, tx := range transactions {
		txMap := tx.(map[string]any)
		txBlockNumber := txMap["blockNumber"]

		if txBlockNumber != blockNumber {
			t.Errorf("Transaction %d has blockNumber %v but belongs to block %v", i, txBlockNumber, blockNumber)
		}
	}
}

// TestLiveIndexerPerformance tests the performance of live indexing
func TestLiveIndexerPerformance(t *testing.T) {
	if !indexerStarted {
		t.Skip("Live indexer not started - skipping live tests")
	}

	// Get current block count
	initialCount := getLiveBlockCount()
	if initialCount == 0 {
		t.Skip("No blocks indexed yet - skipping performance test")
	}

	logger.Testf("Initial block count: %d", initialCount)

	// Wait for more blocks to be indexed
	time.Sleep(10 * time.Second)

	finalCount := getLiveBlockCount()
	logger.Testf("Final block count: %d", finalCount)

	if finalCount <= initialCount {
		logger.Test("Warning: No new blocks indexed during test period (this may be normal depending on network activity)")
	} else {
		blocksIndexed := finalCount - initialCount
		logger.Testf("✓ Indexed %d new blocks in 30 seconds", blocksIndexed)
	}
}

// getLiveBlockCount returns the total number of blocks indexed
func getLiveBlockCount() int {
	if liveGraphqlURL == "" {
		return 0
	}
	query := `{"query":"query { Block { _count } }"}`
	resp, err := http.Post(liveGraphqlURL, "application/json", bytes.NewBuffer([]byte(query)))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		return 0
	}

	blocks, ok := data[constants.CollectionBlock].([]any)
	if !ok {
		return 0
	}

	return len(blocks)
}
