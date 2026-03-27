//go:build integration
// +build integration

package integration

import (
	"path/filepath"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

var transactionQueryPath string

func init() {
	transactionQueryPath = filepath.Join(getProjectRoot(nil), "queries/transaction.graphql")
}

// Helper to get an arbitrary transaction from a mock block with transactions
func getArbitraryTransaction(t *testing.T) map[string]any {
	blockQueryPath := filepath.Join(getProjectRoot(nil), "queries/blocks.graphql")
	blockNumber := 1000001 // Use our predictable mock block with transactions
	variables := map[string]any{"blockNumber": blockNumber}
	result := MakeQuery(t, blockQueryPath, "GetBlockWithTransactions", variables)
	blockList, ok := result["data"].(map[string]any)[constants.CollectionBlock].([]any)
	if !ok || len(blockList) == 0 {
		t.Fatalf("No block with number %v found; cannot test transactions.", blockNumber)
	}
	block := blockList[0].(map[string]any)
	transactions, ok := block["transactions"].([]any)
	if !ok || len(transactions) == 0 {
		t.Fatalf("No transactions found in block %d: %v", blockNumber, result)
	}
	firstTx, ok := transactions[0].(map[string]any)
	if !ok {
		t.Fatalf("Transaction is not a map: %v", transactions[0])
	}
	return firstTx
}

func getArbitraryTransactionHash(t *testing.T) string {
	tx := getArbitraryTransaction(t)
	hash, ok := tx["hash"].(string)
	if !ok || len(hash) == 0 {
		t.Fatalf("Transaction hash missing or empty: %v", tx)
	}
	return hash
}

func getArbitraryAddress(t *testing.T) string {
	tx := getArbitraryTransaction(t)
	address, ok := tx["from"].(string)
	if !ok || len(address) == 0 {
		t.Fatalf("Transaction from address missing or empty: %v", tx)
	}
	return address
}

// Helper to get an arbitrary transaction with logs from a mock block
func getArbitraryTransactionWithLogs(t *testing.T) map[string]any {
	blockQueryPath := filepath.Join(getProjectRoot(nil), "queries/blocks.graphql")
	blockNumber := 1000002 // Use our second mock block with transactions
	variables := map[string]any{"blockNumber": blockNumber}
	result := MakeQuery(t, blockQueryPath, "GetBlockWithTransactions", variables)
	blockList, ok := result["data"].(map[string]any)[constants.CollectionBlock].([]any)
	if !ok || len(blockList) == 0 {
		t.Fatalf("No block with number %v found; cannot test transactions.", blockNumber)
	}
	block := blockList[0].(map[string]any)
	transactions, ok := block["transactions"].([]any)
	if !ok || len(transactions) == 0 {
		t.Fatalf("No transactions found in block %d: %v", blockNumber, result)
	}
	for _, txRaw := range transactions {
		tx, ok := txRaw.(map[string]any)
		if !ok {
			continue
		}
		logs, ok := tx["logs"].([]any)
		if ok && len(logs) > 0 {
			return tx
		}
	}
	t.Fatalf("No transaction with logs found in block %d: %v", blockNumber, result)
	return nil
}

// Helper to get an arbitrary topic (from a transaction's log)
func getArbitraryTopic(t *testing.T) string {
	tx := getArbitraryTransactionWithLogs(t)
	logs, _ := tx["logs"].([]any)
	firstLog, ok := logs[0].(map[string]any)
	if !ok {
		t.Fatalf("Log is not a map: %v", logs[0])
	}
	topics, ok := firstLog["topics"].([]any)
	if !ok || len(topics) == 0 {
		t.Fatalf("No topics found in log: %v", firstLog)
	}
	topic, ok := topics[0].(string)
	if !ok || len(topic) == 0 {
		t.Fatalf("Topic is not a string: %v", topics[0])
	}
	return topic
}

func TestGetTransactionByHash(t *testing.T) {
	transactionHash := getArbitraryTransactionHash(t)
	result := MakeQuery(t, transactionQueryPath, "GetTransactionByHash", map[string]any{"txHash": transactionHash})
	transactionList, ok := result["data"].(map[string]any)[constants.CollectionTransaction].([]any)
	if !ok || len(transactionList) == 0 {
		t.Errorf("No transactions returned: %v", result)
		return
	}
	hash, ok := transactionList[0].(map[string]any)["hash"]
	if !ok {
		t.Errorf("Transaction missing hash field: %v", transactionList[0])
		return
	}
	hashStr, ok := hash.(string)
	if !ok {
		t.Errorf("Transaction hash is not a string: %v", hash)
	} else if len(hashStr) == 0 {
		t.Errorf("Got empty hash: %v", transactionList[0])
	}

	if hashStr != transactionHash {
		t.Errorf("Transaction returned doesn't match our transaction hash input: received %v ; given %v", transactionList, transactionHash)
	}
}

func TestGetTransactionsInvolvingAddress(t *testing.T) {
	address := getArbitraryAddress(t)
	result := MakeQuery(t, transactionQueryPath, "GetTransactionsInvolvingAddress", map[string]any{"address": address})
	transactionList, ok := result["data"].(map[string]any)[constants.CollectionTransaction].([]any)
	if !ok || len(transactionList) == 0 {
		t.Errorf("No transactions returned for address %v: %v", address, result)
		return
	}
	found := false
	for _, tx := range transactionList {
		txMap, ok := tx.(map[string]any)
		if !ok {
			t.Errorf("Transaction is not a map: %v", tx)
			continue
		}
		if txMap["from"] == address || txMap["to"] == address {
			found = true
		} else {
			t.Errorf("Transaction does not involve address %v: %v", address, txMap)
		}
	}
	if !found {
		t.Errorf("No transaction involved the address %v", address)
	}
}

func TestGetAllTransactionWithTopic(t *testing.T) {
	topic := getArbitraryTopic(t)
	result := MakeQuery(t, transactionQueryPath, "GetAllTransactionWithTopic", map[string]any{"topic": topic})
	logList, ok := result["data"].(map[string]any)[constants.CollectionLog].([]any)
	if !ok || len(logList) == 0 {
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
		topics, ok := logMap["topics"].([]any)
		if !ok {
			t.Errorf("Log topics missing or not a list: %v", logMap)
			continue
		}
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
