package tron

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// txTRC10RawData is a raw_data literal the way a real node serves a
// TRC-10 asset transfer, carrying keys the typed view does not model
// (parameter type_url, value permission_id) and an amount above the
// float64 mantissa limit: 2^53 + 1, the smallest int64 a float64 decode
// rounds to 2^53.
const txTRC10RawData = `{"contract":[{"parameter":{"value":{"amount":9007199254740993,"asset_name":"1002000","owner_address":"41072a35d3cd7112c08fe2b04f5c99e73e0a3c099e","to_address":"41118f7b4382bea375ee420f1b211a4619f3ba78ad","permission_id":2},"type_url":"type.googleapis.com/protocol.TransferAssetContract"},"type":"TransferAssetContract"}],"expiration":1578594916000,"timestamp":1578594883902}`

// TestBuildTransactionData_RawDataVerbatimAndExactAmount pins the two wire
// contracts of the transaction document: the rawData field carries the
// node's raw_data bytes exactly as served (keys the typed view does not
// model included), and the extracted amount keeps int64 exactness because
// the view decodes with UseNumber instead of rounding through float64.
func TestBuildTransactionData_RawDataVerbatimAndExactAmount(t *testing.T) {
	t.Parallel()

	blockJSON := `{"blockID":"b1","block_header":{"raw_data":{"number":10,"timestamp":1,"parentHash":"p"}},"transactions":[{"txID":"t1","raw_data":` + txTRC10RawData + `,"ret":[{"contractRet":"SUCCESS"}]}]}`
	var block Block
	require.NoError(t, json.Unmarshal([]byte(blockJSON), &block), "the wire decode must capture raw_data verbatim")

	tx := &block.Transactions[0]
	doc := buildTransactionData(&block, tx, &TransactionInfo{ID: "t1", Fee: 1})

	assert.Equal(t, txTRC10RawData, doc[RawDataFieldName], "rawData must be the node's bytes, byte-for-byte")
	assert.Equal(t, int64(9007199254740993), doc[AmountFieldName], "a TRC-10 amount above 2^53 must not round")
	assert.Equal(t, "TransferAssetContract", doc[TypeFieldName])
	assert.Equal(t, "41072a35d3cd7112c08fe2b04f5c99e73e0a3c099e", doc[FromFieldName])
	assert.Equal(t, "41118f7b4382bea375ee420f1b211a4619f3ba78ad", doc[ToFieldName])
}

// TestBuildTransactionData_NoRawData covers the synthetic path: a
// transaction without raw_data bytes keeps an empty rawData field and a
// zero trio, matching how the DefraDB-backed helper tests build documents.
func TestBuildTransactionData_NoRawData(t *testing.T) {
	t.Parallel()

	doc := buildTransactionData(fakeTronBlock(10), &Transaction{TxID: "t1"}, nil)
	assert.Equal(t, "", doc[RawDataFieldName])
	assert.Equal(t, "", doc[TypeFieldName])
	assert.Equal(t, int64(0), doc[AmountFieldName])
}

// TestIntField_NumberShapes pins the numeric-key extraction across every
// value shape the value map can carry: json.Number from the UseNumber view
// decode (exact for int64, Float64 fallback for exponent spellings), and
// the hand-built map cases.
func TestIntField_NumberShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  int64
	}{
		{"json number integer", json.Number("9007199254740993"), 9007199254740993},
		{"json number exponent falls back to float", json.Number("1e3"), 1000},
		{"json number unparsable", json.Number("x"), 0},
		{"float64", float64(7), 7},
		{"int64", int64(8), 8},
		{"int", int(9), 9},
		{"string is not a number", "10", 0},
		{"missing key", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, intField(map[string]any{"amount": tt.value}, "amount"))
		})
	}
}

// TestAssetNameString_NumberShapes pins the TRC-10 asset id normalization:
// a json.Number keeps its digits verbatim, a float64 renders as its integer
// digits, a string passes through, anything else is empty.
func TestAssetNameString_NumberShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"json number keeps digits", json.Number("1002000"), "1002000"},
		{"float64 renders digits", float64(105), "105"},
		{"string passes through", "1002000", "1002000"},
		{"nil is empty", nil, ""},
		{"bool is empty", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, assetNameString(tt.value))
		})
	}
}
