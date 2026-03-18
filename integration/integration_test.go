package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

const graphqlURL = "http://127.0.0.1:9181/api/v0/graphql"

var defraNode *node.Node

func TestMain(m *testing.M) {
	// Initialize logger for integration tests first
	logger.InitWithFiles(true)
	logger.Test("TestMain - Starting self-contained integration tests with mock data")

	// Clean up any existing integration DefraDB data
	logger.Test("Cleaning up existing integration DefraDB data...")
	cleanupPaths := []string{
		"./.defra",
		"./.defra",
	}
	for _, path := range cleanupPaths {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			logger.Sugar.Warnf("Failed to clean existing data at %s: %v", path, err)
		}
	}

	// Start embedded DefraDB directly for mock data testing (no indexer needed)
	logger.Test("Starting embedded DefraDB for mock data testing...")
	ctx := context.Background()

	// Create DefraDB node directly without indexer
	opts := options.Node().
		SetDisableAPI(false).
		SetDisableP2P(true)
	opts.Store().SetPath("./.defra")
	opts.HTTP().SetAddress("127.0.0.1:9181")

	var err error
	defraNode, err = node.New(ctx, opts)
	if err != nil {
		logger.Sugar.Fatalf("Failed to create DefraDB node: %v", err)
	}

	// node.Start returns only after the HTTP API is healthy (calls HealthCheck internally)
	err = defraNode.Start(ctx)
	if err != nil {
		logger.Sugar.Fatalf("Failed to start DefraDB node: %v", err)
	}

	// Apply schema synchronously — no race with mock data insertion
	err = applySchema(ctx, defraNode)
	if err != nil && !strings.Contains(err.Error(), "collection already exists") {
		logger.Sugar.Fatalf("Failed to apply schema: %v", err)
	}

	logger.Test("DefraDB node started successfully with schema applied")

	// Insert mock test data
	logger.Test("Inserting mock test data...")
	if err := insertMockData(); err != nil {
		logger.Sugar.Errorf("Failed to insert mock data: %v", err)
		os.Exit(1)
	}
	logger.Test("Mock data inserted successfully!")

	// Run tests
	exitCode := m.Run()

	// Teardown
	logger.Test("TestMain - Teardown")
	if defraNode != nil {
		ctx := context.Background()
		defraNode.Close(ctx)
		logger.Test("DefraDB node closed")
	}

	os.Exit(exitCode)
}

func TestGraphQLConnection(t *testing.T) {
	logger.Test("Testing GraphQL connection")
	resp, err := http.Post(graphqlURL, "application/json", bytes.NewBuffer([]byte(`{"query":"query { __typename }"}`)))
	if err != nil {
		t.Fatalf("Failed to connect to GraphQL endpoint: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Unexpected status code: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if _, ok := result["data"]; !ok {
		t.Fatalf("No data field in response: %s", string(body))
	}
}

func postGraphQLQuery(t *testing.T, query string, variables map[string]any) map[string]any {
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	b, _ := json.Marshal(payload)
	resp, err := http.Post(graphqlURL, "application/json", bytes.NewBuffer(b))
	if err != nil {
		t.Fatalf("Failed to POST query: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Unexpected status code: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	return result
}

// Helper to find the project root by looking for go.mod
func getProjectRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("Could not find project root (go.mod)")
		}
		dir = parent
	}
}

// Helper to extract a named query from a .graphql file
func loadGraphQLQuery(filename, queryName string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	content := string(data)
	start := strings.Index(content, "query "+queryName)
	if start == -1 {
		return "", fmt.Errorf("query %s not found", queryName)
	}
	// Find the next "query " after start, or end of file
	next := strings.Index(content[start+1:], "query ")
	var query string
	if next == -1 {
		query = content[start:]
	} else {
		query = content[start : start+next+1]
	}
	query = strings.TrimSpace(query)
	return query, nil
}

func MakeQuery(t *testing.T, queryPath string, query string, args map[string]any) map[string]any {
	query, err := loadGraphQLQuery(queryPath, query)
	if err != nil {
		t.Errorf("Failed to load query %v", err)
	}
	result := postGraphQLQuery(t, query, args)
	return result
}

func insertMockData() error {
	// Store DocIDs for relationship establishment
	var block1DocID, block2DocID, tx1DocID, tx2DocID string

	// Create Block 1
	block1Mutation := map[string]any{
		"query": fmt.Sprintf(`mutation {
			create_%s(input: {`, constants.CollectionBlock) + `
				hash: "0x1000001000000000000000000000000000000000000000000000000000000001"
				number: 1000001
				timestamp: "1640995200"
				parentHash: "0x1000000000000000000000000000000000000000000000000000000000000000"
				difficulty: "1000000"
				gasUsed: "21000"
				gasLimit: "8000000"
				nonce: "1000001"
				miner: "0x1000000000000000000000000000000000000001"
				size: "1024"
				stateRoot: "0x1000001000000000000000000000000000000000000000000000000000000001"
				sha3Uncles: "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"
				transactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
				receiptsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
				logsBloom: "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				extraData: "0x"
				mixHash: "0x0000000000000000000000000000000000000000000000000000000000000000"
				totalDifficulty: "1000000"
				baseFeePerGas: ""
			}) {
				_docID
				hash
				number
			}
		}`,
	}

	// Execute Block 1 creation and extract DocID
	jsonData, err := json.Marshal(block1Mutation)
	if err != nil {
		return fmt.Errorf("failed to marshal block1 mutation: %v", err)
	}

	resp, err := http.Post("http://127.0.0.1:9181/api/v0/graphql", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("block1 creation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("block1 creation failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read block1 response: %v", err)
	}

	var block1Resp map[string]any
	if err := json.Unmarshal(body, &block1Resp); err != nil {
		return fmt.Errorf("failed to parse block1 response: %v", err)
	}

	if errors, hasErrors := block1Resp["errors"]; hasErrors {
		return fmt.Errorf("GraphQL errors in block1 creation: %v", errors)
	}

	// Extract Block 1 DocID
	if data, ok := block1Resp["data"].(map[string]any); ok {
		if createBlock, ok := data[fmt.Sprintf("create_%s", constants.CollectionBlock)].([]any); ok && len(createBlock) > 0 {
			if blockData, ok := createBlock[0].(map[string]any); ok {
				if docID, ok := blockData["_docID"].(string); ok {
					block1DocID = docID
					logger.Testf("Block 1 created with DocID: %s", block1DocID)
				}
			}
		}
	}

	// Create Block 2
	block2Mutation := map[string]any{
		"query": fmt.Sprintf(`mutation {
			create_%s(input: {`, constants.CollectionBlock) + `
				hash: "0x1000002000000000000000000000000000000000000000000000000000000002"
				number: 1000002
				timestamp: "1640995212"
				parentHash: "0x1000001000000000000000000000000000000000000000000000000000000001"
				difficulty: "1000000"
				gasUsed: "42000"
				gasLimit: "8000000"
				nonce: "1000002"
				miner: "0x1000000000000000000000000000000000000002"
				size: "2048"
				stateRoot: "0x1000002000000000000000000000000000000000000000000000000000000002"
				sha3Uncles: "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"
				transactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
				receiptsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
				logsBloom: "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
				extraData: "0x"
				mixHash: "0x0000000000000000000000000000000000000000000000000000000000000000"
				totalDifficulty: "1000000"
				baseFeePerGas: ""
			}) {
				_docID
				hash
				number
			}
		}`,
	}

	// Execute Block 2 creation and extract DocID
	jsonData, err = json.Marshal(block2Mutation)
	if err != nil {
		return fmt.Errorf("failed to marshal block2 mutation: %v", err)
	}

	resp, err = http.Post("http://127.0.0.1:9181/api/v0/graphql", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("block2 creation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("block2 creation failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read block2 response: %v", err)
	}

	var block2Resp map[string]any
	if err := json.Unmarshal(body, &block2Resp); err != nil {
		return fmt.Errorf("failed to parse block2 response: %v", err)
	}

	if errors, hasErrors := block2Resp["errors"]; hasErrors {
		return fmt.Errorf("GraphQL errors in block2 creation: %v", errors)
	}

	// Extract Block 2 DocID
	if data, ok := block2Resp["data"].(map[string]any); ok {
		if createBlock, ok := data[fmt.Sprintf("create_%s", constants.CollectionBlock)].([]any); ok && len(createBlock) > 0 {
			if blockData, ok := createBlock[0].(map[string]any); ok {
				if docID, ok := blockData["_docID"].(string); ok {
					block2DocID = docID
					logger.Testf("Block 2 created with DocID: %s", block2DocID)
				}
			}
		}
	}

	// Create Transaction 1 with relationship to Block 1
	tx1Mutation := map[string]any{
		"query": fmt.Sprintf(`mutation {
			create_%s(input: {`, constants.CollectionTransaction) + fmt.Sprintf(`
				hash: "0x2000001000000000000000000000000000000000000000000000000000000001"
				blockHash: "0x1000001000000000000000000000000000000000000000000000000000000001"
				blockNumber: 1000001
				from: "0x3000000000000000000000000000000000000001"
				to: "0x3000000000000000000000000000000000000002"
				value: "1000000000000000000"
				gas: "21000"
				gasPrice: "20000000000"
				gasUsed: "21000"
				input: "0x"
				nonce: "1"
				transactionIndex: 0
				type: "0"
				chainId: "1"
				v: "27"
				r: "0x1000000000000000000000000000000000000000000000000000000000000001"
				s: "0x1000000000000000000000000000000000000000000000000000000000000001"
				status: true
				cumulativeGasUsed: "21000"
				effectiveGasPrice: "20000000000"
				block: "%s"
			}) {
				_docID
				hash
			}
		}`, block1DocID),
	}

	// Execute Transaction 1 creation
	jsonData, err = json.Marshal(tx1Mutation)
	if err != nil {
		return fmt.Errorf("failed to marshal tx1 mutation: %v", err)
	}

	resp, err = http.Post("http://127.0.0.1:9181/api/v0/graphql", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("tx1 creation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tx1 creation failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read tx1 response: %v", err)
	}

	var tx1Resp map[string]any
	if err := json.Unmarshal(body, &tx1Resp); err != nil {
		return fmt.Errorf("failed to parse tx1 response: %v", err)
	}

	if errors, hasErrors := tx1Resp["errors"]; hasErrors {
		return fmt.Errorf("GraphQL errors in tx1 creation: %v", errors)
	}

	// Extract Transaction 1 DocID
	if data, ok := tx1Resp["data"].(map[string]any); ok {
		if createTx, ok := data[fmt.Sprintf("create_%s", constants.CollectionTransaction)].([]any); ok && len(createTx) > 0 {
			if txData, ok := createTx[0].(map[string]any); ok {
				if docID, ok := txData["_docID"].(string); ok {
					tx1DocID = docID
					logger.Testf("Transaction 1 created with DocID: %s", tx1DocID)
				}
			}
		}
	}

	// Create Transaction 2 with relationship to Block 2
	tx2Mutation := map[string]any{
		"query": fmt.Sprintf(`mutation {
			create_%s(input: {`, constants.CollectionTransaction) + fmt.Sprintf(`
				hash: "0x2000002000000000000000000000000000000000000000000000000000000002"
				blockHash: "0x1000002000000000000000000000000000000000000000000000000000000002"
				blockNumber: 1000002
				from: "0x3000000000000000000000000000000000000003"
				to: "0x3000000000000000000000000000000000000004"
				value: "2000000000000000000"
				gas: "21000"
				gasPrice: "25000000000"
				gasUsed: "21000"
				input: "0x"
				nonce: "2"
				transactionIndex: 0
				type: "0"
				chainId: "1"
				v: "28"
				r: "0x2000000000000000000000000000000000000000000000000000000000000002"
				s: "0x2000000000000000000000000000000000000000000000000000000000000002"
				status: true
				cumulativeGasUsed: "21000"
				effectiveGasPrice: "25000000000"
				block: "%s"
			}) {
				_docID
				hash
			}
		}`, block2DocID),
	}

	// Execute Transaction 2 creation
	jsonData, err = json.Marshal(tx2Mutation)
	if err != nil {
		return fmt.Errorf("failed to marshal tx2 mutation: %v", err)
	}

	resp, err = http.Post("http://127.0.0.1:9181/api/v0/graphql", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("tx2 creation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tx2 creation failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read tx2 response: %v", err)
	}

	var tx2Resp map[string]any
	if err := json.Unmarshal(body, &tx2Resp); err != nil {
		return fmt.Errorf("failed to parse tx2 response: %v", err)
	}

	if errors, hasErrors := tx2Resp["errors"]; hasErrors {
		return fmt.Errorf("GraphQL errors in tx2 creation: %v", errors)
	}

	// Extract Transaction 2 DocID
	if data, ok := tx2Resp["data"].(map[string]any); ok {
		if createTx, ok := data[fmt.Sprintf("create_%s", constants.CollectionTransaction)].([]any); ok && len(createTx) > 0 {
			if txData, ok := createTx[0].(map[string]any); ok {
				if docID, ok := txData["_docID"].(string); ok {
					tx2DocID = docID
					logger.Testf("Transaction 2 created with DocID: %s", tx2DocID)
				}
			}
		}
	}

	// Create Log 1 for Transaction 1
	log1Mutation := map[string]any{
		"query": fmt.Sprintf(`mutation {
			create_%s(input: {`, constants.CollectionLog) + fmt.Sprintf(`
				address: "0x4000000000000000000000000000000000000001"
				topics: ["0x5000000000000000000000000000000000000000000000000000000000000001", "0x5000000000000000000000000000000000000000000000000000000000000002"]
				data: "0x6000000000000000000000000000000000000000000000000000000000000001"
				transactionHash: "0x2000001000000000000000000000000000000000000000000000000000000001"
				blockHash: "0x1000001000000000000000000000000000000000000000000000000000000001"
				blockNumber: 1000001
				transactionIndex: 0
				logIndex: 0
				removed: "false"
				block: "%s"
				transaction: "%s"
			}) {
				_docID
				address
				topics
			}
		}`, block1DocID, tx1DocID),
	}

	// Execute Log 1 creation
	jsonData, err = json.Marshal(log1Mutation)
	if err != nil {
		return fmt.Errorf("failed to marshal log1 mutation: %v", err)
	}

	resp, err = http.Post("http://127.0.0.1:9181/api/v0/graphql", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("log1 creation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("log1 creation failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read log1 response: %v", err)
	}

	var log1Resp map[string]any
	if err := json.Unmarshal(body, &log1Resp); err != nil {
		return fmt.Errorf("failed to parse log1 response: %v", err)
	}

	if errors, hasErrors := log1Resp["errors"]; hasErrors {
		return fmt.Errorf("GraphQL errors in log1 creation: %v", errors)
	}

	logger.Testf("Log 1 created successfully: %s", string(body))

	// Create Log 2 for Transaction 2
	log2Mutation := map[string]any{
		"query": fmt.Sprintf(`mutation {
			create_%s(input: {`, constants.CollectionLog) + fmt.Sprintf(`
				address: "0x4000000000000000000000000000000000000002"
				topics: ["0x5000000000000000000000000000000000000000000000000000000000000003", "0x5000000000000000000000000000000000000000000000000000000000000004"]
				data: "0x6000000000000000000000000000000000000000000000000000000000000002"
				transactionHash: "0x2000002000000000000000000000000000000000000000000000000000000002"
				blockHash: "0x1000002000000000000000000000000000000000000000000000000000000002"
				blockNumber: 1000002
				transactionIndex: 0
				logIndex: 0
				removed: "false"
				block: "%s"
				transaction: "%s"
			}) {
				_docID
				address
				topics
			}
		}`, block2DocID, tx2DocID),
	}

	// Execute Log 2 creation
	jsonData, err = json.Marshal(log2Mutation)
	if err != nil {
		return fmt.Errorf("failed to marshal log2 mutation: %v", err)
	}

	resp, err = http.Post("http://127.0.0.1:9181/api/v0/graphql", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("log2 creation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("log2 creation failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read log2 response: %v", err)
	}

	var log2Resp map[string]any
	if err := json.Unmarshal(body, &log2Resp); err != nil {
		return fmt.Errorf("failed to parse log2 response: %v", err)
	}

	if errors, hasErrors := log2Resp["errors"]; hasErrors {
		return fmt.Errorf("GraphQL errors in log2 creation: %v", errors)
	}

	logger.Testf("Log 2 created successfully: %s", string(body))
	logger.Test("Mock data with relationships and logs inserted successfully!")

	return nil
}

// applySchema applies the GraphQL schema to DefraDB node
func applySchema(ctx context.Context, defraNode *node.Node) error {
	fmt.Println("Applying schema...")

	_, err := defraNode.DB.AddSchema(ctx, schema.GetSchema())
	return err
}
