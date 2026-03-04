//go:build integration
// +build integration

package integration

import (
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

var logsQueryPath string

func init() {
	logsQueryPath = filepath.Join(getProjectRoot(nil), "queries/logs.graphql")
}

func assertLogHasTopics(t *testing.T, logMap map[string]any) {
	topics, ok := logMap["topics"].([]any)
	if !ok || len(topics) == 0 {
		t.Errorf("Log missing or empty topics field: %v", logMap)
	}
}

func TestGetAllTransactionLogs(t *testing.T) {
	txHash := getArbitraryTransactionHash(t)
	result := MakeQuery(t, logsQueryPath, "GetAllTransactionLogs", map[string]any{"txHash": txHash})
	logList, ok := result["data"].(map[string]any)[constants.CollectionLog].([]any)
	if !ok {
		t.Errorf("No logs returned for txHash %v: %v", txHash, result)
		return
	}
	for _, l := range logList {
		logMap, ok := l.(map[string]any)
		if !ok {
			t.Errorf("Log is not a map: %v", l)
			continue
		}
		if logMap["transactionHash"] != nil && logMap["transactionHash"] != txHash {
			t.Errorf("Log transactionHash does not match: got %v, want %v", logMap["transactionHash"], txHash)
		}
		assertLogHasTopics(t, logMap)
	}
}

// Helper to get any block from the database
func getArbitraryBlock(t *testing.T) map[string]any {
	result := MakeQuery(t, blockQueryPath, "GetLatestBlocks", map[string]any{
		"limit": 1,
	})

	if result == nil {
		t.Fatalf("No blocks returned from DefraDB - database may be empty")
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatalf("Invalid data format in GraphQL response: %v", result)
	}

	blocks, ok := data[constants.CollectionBlock].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("No blocks returned")
	}

	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("Invalid block format: %v", blocks[0])
	}

	return block
}

func TestGetAllBlockLogs(t *testing.T) {
	block := getArbitraryBlock(t)
	blockHash, ok := block["hash"].(string)
	if !ok || len(blockHash) == 0 {
		t.Fatalf("Block hash missing or empty in block: %v", block)
	}
	result := MakeQuery(t, logsQueryPath, "GetAllBlockLogs", map[string]any{"blockHash": blockHash})
	logList, ok := result["data"].(map[string]any)[constants.CollectionLog].([]any)
	if !ok {
		t.Errorf("No logs returned for blockHash %v: %v", blockHash, result)
		return
	}
	for _, l := range logList {
		logMap, ok := l.(map[string]any)
		if !ok {
			t.Errorf("Log is not a map: %v", l)
			continue
		}
		if logMap["blockHash"] != nil && logMap["blockHash"] != blockHash {
			t.Errorf("Log blockHash does not match: got %v, want %v", logMap["blockHash"], blockHash)
		}
		assertLogHasTopics(t, logMap)
	}
}

func TestGetAllLogsByTopic(t *testing.T) {
	topic := getArbitraryTopic(t)
	result := MakeQuery(t, logsQueryPath, "GetAllLogsByTopic", map[string]any{"topic": topic})
	logList, ok := result["data"].(map[string]any)[constants.CollectionLog].([]any)
	if !ok {
		t.Errorf("No logs returned for topic %v: %v", topic, result)
		return
	}
	found := false
	for _, l := range logList {
		logMap, ok := l.(map[string]any)
		if !ok {
			t.Errorf("Log is not a map: %v", l)
			continue
		}
		assertLogHasTopics(t, logMap)
		topics, _ := logMap["topics"].([]any)
		topicFound := false
		for _, tpc := range topics {
			tpcStr, ok := tpc.(string)
			if ok && tpcStr == topic {
				topicFound = true
				break
			}
		}
		if !topicFound {
			t.Errorf("Log does not contain topic %v: %v", topic, logMap)
		} else {
			found = true
		}
	}
	if !found {
		t.Errorf("No log contained the topic %v", topic)
	}
}
