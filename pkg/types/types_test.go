package types

import (
	"encoding/json"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

func TestTransactionReceiptJSONMarshaling(t *testing.T) {
	t.Parallel()
	receipt := TransactionReceipt{
		TransactionHash:   "0x1234567890abcdef",
		TransactionIndex:  "0",
		BlockHash:         "0xabcdef1234567890",
		BlockNumber:       "12345",
		From:              "0xfrom",
		To:                "0xto",
		CumulativeGasUsed: "100000",
		GasUsed:           "21000",
		ContractAddress:   "0xcontract",
		Status:            "0x1",
		Logs:              []Log{},
	}

	// Test marshaling
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("Failed to marshal TransactionReceipt: %v", err)
	}

	// Test unmarshaling
	var unmarshaled TransactionReceipt
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal TransactionReceipt: %v", err)
	}

	// Verify data integrity
	if unmarshaled.TransactionHash != receipt.TransactionHash {
		t.Errorf("TransactionHash mismatch: got %s, want %s", unmarshaled.TransactionHash, receipt.TransactionHash)
	}
	if unmarshaled.BlockNumber != receipt.BlockNumber {
		t.Errorf("BlockNumber mismatch: got %s, want %s", unmarshaled.BlockNumber, receipt.BlockNumber)
	}
}

func TestBlockJSONMarshaling(t *testing.T) {
	t.Parallel()
	block := Block{
		Hash:             "0x1234567890abcdef",
		Number:           "12345",
		Timestamp:        "1600000000",
		ParentHash:       "0xparent",
		Difficulty:       "1000000",
		GasUsed:          "4000000",
		GasLimit:         "8000000",
		Nonce:            "123456789",
		Miner:            "0xminer",
		Size:             "1024",
		StateRoot:        "0xstateroot",
		Sha3Uncles:       "0xsha3uncles",
		TransactionsRoot: "0xtxroot",
		ReceiptsRoot:     "0xreceiptsroot",
		ExtraData:        "extra",
		Transactions:     []Transaction{},
	}

	// Test marshaling
	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Failed to marshal Block: %v", err)
	}

	// Test unmarshaling
	var unmarshaled Block
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Block: %v", err)
	}

	// Verify data integrity
	if unmarshaled.Hash != block.Hash {
		t.Errorf("Hash mismatch: got %s, want %s", unmarshaled.Hash, block.Hash)
	}
	if unmarshaled.Number != block.Number {
		t.Errorf("Number mismatch: got %s, want %s", unmarshaled.Number, block.Number)
	}
}

func TestTransactionJSONMarshaling(t *testing.T) {
	t.Parallel()
	tx := Transaction{
		Hash:             "0xtxhash",
		BlockHash:        "0xblockhash",
		BlockNumber:      "12345",
		From:             "0xfrom",
		To:               "0xto",
		Value:            "1000",
		Gas:              "21000",
		GasPrice:         "20000000000",
		Input:            "0xinput",
		Nonce:            "1",
		TransactionIndex: 0,
		Logs:             []Log{},
	}

	// Test marshaling
	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("Failed to marshal Transaction: %v", err)
	}

	// Test unmarshaling
	var unmarshaled Transaction
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Transaction: %v", err)
	}

	// Verify data integrity
	if unmarshaled.Hash != tx.Hash {
		t.Errorf("Hash mismatch: got %s, want %s", unmarshaled.Hash, tx.Hash)
	}
}

func TestLogJSONMarshaling(t *testing.T) {
	t.Parallel()
	log := Log{
		Address:          "0xcontract",
		Topics:           []string{"0xtopic1", "0xtopic2"},
		Data:             "0xlogdata",
		BlockNumber:      "12345",
		TransactionHash:  "0xtxhash",
		TransactionIndex: 0,
		BlockHash:        "0xblockhash",
		LogIndex:         0,
		Removed:          false,
	}

	// Test marshaling
	data, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("Failed to marshal Log: %v", err)
	}

	// Test unmarshaling
	var unmarshaled Log
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Log: %v", err)
	}

	// Verify data integrity
	if unmarshaled.Address != log.Address {
		t.Errorf("Address mismatch: got %s, want %s", unmarshaled.Address, log.Address)
	}
	if len(unmarshaled.Topics) != len(log.Topics) {
		t.Errorf("Topics length mismatch: got %d, want %d", len(unmarshaled.Topics), len(log.Topics))
	}
	if unmarshaled.Removed != log.Removed {
		t.Errorf("Removed mismatch: got %t, want %t", unmarshaled.Removed, log.Removed)
	}
}

func TestRequestJSONMarshaling(t *testing.T) {
	t.Parallel()
	request := Request{
		Type:  "query",
		Query: "{ Block { number } }",
	}

	// Test marshaling
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal Request: %v", err)
	}

	// Test unmarshaling
	var unmarshaled Request
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Request: %v", err)
	}

	// Verify data integrity
	if unmarshaled.Type != request.Type {
		t.Errorf("Type mismatch: got %s, want %s", unmarshaled.Type, request.Type)
	}
	if unmarshaled.Query != request.Query {
		t.Errorf("Query mismatch: got %s, want %s", unmarshaled.Query, request.Query)
	}
}

func TestResponseJSONMarshaling(t *testing.T) {
	t.Parallel()
	response := Response{
		Data: map[string][]struct {
			DocID string `json:"_docID"`
		}{
			constants.CollectionBlock: {
				{DocID: "doc-id-1"},
				{DocID: "doc-id-2"},
			},
		},
	}

	// Test marshaling
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal Response: %v", err)
	}

	// Test unmarshaling
	var unmarshaled Response
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Response: %v", err)
	}

	// Verify data integrity
	if len(unmarshaled.Data[constants.CollectionBlock]) != 2 {
		t.Errorf("Expected 2 Block entries, got %d", len(unmarshaled.Data[constants.CollectionBlock]))
	}
	if unmarshaled.Data[constants.CollectionBlock][0].DocID != "doc-id-1" {
		t.Errorf("DocID mismatch: got %s, want %s", unmarshaled.Data[constants.CollectionBlock][0].DocID, "doc-id-1")
	}
}

func TestUpdateStructsJSONMarshaling(t *testing.T) {
	t.Parallel()
	// Test UpdateTransactionStruct
	updateTx := UpdateTransactionStruct{
		BlockId: "block-id-123",
		TxHash:  "0xtxhash123",
	}

	data, err := json.Marshal(updateTx)
	if err != nil {
		t.Fatalf("Failed to marshal UpdateTransactionStruct: %v", err)
	}

	var unmarshaledTx UpdateTransactionStruct
	err = json.Unmarshal(data, &unmarshaledTx)
	if err != nil {
		t.Fatalf("Failed to unmarshal UpdateTransactionStruct: %v", err)
	}

	if unmarshaledTx.BlockId != updateTx.BlockId {
		t.Errorf("BlockId mismatch: got %s, want %s", unmarshaledTx.BlockId, updateTx.BlockId)
	}

	// Test UpdateLogStruct
	updateLog := UpdateLogStruct{
		BlockId:  "block-id-123",
		TxId:     "tx-id-456",
		TxHash:   "0xtxhash123",
		LogIndex: "0",
	}

	data, err = json.Marshal(updateLog)
	if err != nil {
		t.Fatalf("Failed to marshal UpdateLogStruct: %v", err)
	}

	var unmarshaledLog UpdateLogStruct
	err = json.Unmarshal(data, &unmarshaledLog)
	if err != nil {
		t.Fatalf("Failed to unmarshal UpdateLogStruct: %v", err)
	}

	if unmarshaledLog.TxId != updateLog.TxId {
		t.Errorf("TxId mismatch: got %s, want %s", unmarshaledLog.TxId, updateLog.TxId)
	}

}
