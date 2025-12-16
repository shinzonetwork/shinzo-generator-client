package types

import (
	"encoding/json"
	"testing"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
)

func TestTransactionReceiptJSONMarshaling(t *testing.T) {
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
	block := Block{
		BaseFeePerGas: "1000000000",
		Difficulty:    "1000000",
		ExtraData:     "extra",
		GasLimit:      "8000000",
		GasUsed:       "4000000",
		Hash:          "0x1234567890abcdef",
		L1BlockNumber: "24000000",
		LogsBloom:     "0xlogsbloom",
		MixHash:       "0xmixhash",
		Nonce:         "123456789",
		Number:        12345,
		ParentHash:    "0xparent",
		ReceiptsRoot:  "0xreceiptsroot",
		SendCount:     "100",
		SendRoot:      "0xsendroot",
		Sha3Uncles:    "0xsha3uncles",
		Size:          "1024",
		StateRoot:     "0xstateroot",
		Timestamp:     "1600000000",
		Transactions:  []Transaction{},
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
		t.Errorf("Number mismatch: got %d, want %d", unmarshaled.Number, block.Number)
	}
	if unmarshaled.L1BlockNumber != block.L1BlockNumber {
		t.Errorf("L1BlockNumber mismatch: got %s, want %s", unmarshaled.L1BlockNumber, block.L1BlockNumber)
	}
}

func TestTransactionJSONMarshaling(t *testing.T) {
	tx := Transaction{
		// Transaction fields
		BlockHash:        "0xblockhash",
		BlockNumber:      12345,
		From:             "0xfrom",
		Gas:              "21000",
		GasPrice:         "20000000000",
		Hash:             "0xtxhash",
		Input:            "0xinput",
		Nonce:            "1",
		To:               "0xto",
		TransactionIndex: 0,
		Value:            "1000",
		Type:             "0x0",
		ChainId:          "42161",
		V:                "0x25",
		R:                "0xr",
		S:                "0xs",
		// Receipt fields
		ContractAddress:   "",
		CumulativeGasUsed: "100000",
		EffectiveGasPrice: "20000000000",
		GasUsed:           "21000",
		GasUsedForL1:      "0",
		L1BlockNumber:     "24000000",
		Status:            "0x1",
		Timeboosted:       false,
		LogsBloom:         "0xlogsbloom",
		Logs:              []Log{},
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
	if unmarshaled.Status != tx.Status {
		t.Errorf("Status mismatch: got %s, want %s", unmarshaled.Status, tx.Status)
	}
	if unmarshaled.BlockNumber != tx.BlockNumber {
		t.Errorf("BlockNumber mismatch: got %d, want %d", unmarshaled.BlockNumber, tx.BlockNumber)
	}
	if unmarshaled.GasUsed != tx.GasUsed {
		t.Errorf("GasUsed mismatch: got %s, want %s", unmarshaled.GasUsed, tx.GasUsed)
	}
}

func TestLogJSONMarshaling(t *testing.T) {
	log := Log{
		Address:          "0xcontract",
		Topics:           []string{"0xtopic1", "0xtopic2"},
		Data:             "0xlogdata",
		BlockNumber:      12345,
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

func TestDefraDocJSONMarshaling(t *testing.T) {
	doc := DefraDoc{
		JSON: map[string]interface{}{
			"hash":   "0x1234567890abcdef",
			"number": 12345,
			"data":   "test data",
		},
	}

	// Test marshaling
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Failed to marshal DefraDoc: %v", err)
	}

	// Test unmarshaling
	var unmarshaled DefraDoc
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal DefraDoc: %v", err)
	}

	// Verify data integrity
	jsonMap, ok := unmarshaled.JSON.(map[string]interface{})
	if !ok {
		t.Errorf("Expected JSON to be map[string]interface{}, got %T", unmarshaled.JSON)
	}
	if jsonMap["hash"] != "0x1234567890abcdef" {
		t.Errorf("Hash mismatch in JSON: got %v, want %s", jsonMap["hash"], "0x1234567890abcdef")
	}
}
