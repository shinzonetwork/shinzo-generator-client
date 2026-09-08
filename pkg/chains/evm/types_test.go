package evm

import (
	"encoding/json"
	"testing"
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

	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("Failed to marshal TransactionReceipt: %v", err)
	}

	var unmarshaled TransactionReceipt
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal TransactionReceipt: %v", err)
	}

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

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Failed to marshal Block: %v", err)
	}

	var unmarshaled Block
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Block: %v", err)
	}

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
		Status:           true,
		Logs:             []Log{},
	}

	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("Failed to marshal Transaction: %v", err)
	}

	var unmarshaled Transaction
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Transaction: %v", err)
	}

	if unmarshaled.Hash != tx.Hash {
		t.Errorf("Hash mismatch: got %s, want %s", unmarshaled.Hash, tx.Hash)
	}
	if unmarshaled.Status != tx.Status {
		t.Errorf("Status mismatch: got %t, want %t", unmarshaled.Status, tx.Status)
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

	data, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("Failed to marshal Log: %v", err)
	}

	var unmarshaled Log
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal Log: %v", err)
	}

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
