package tron

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnmarshalBlockHeaderShape pins the wallet API's header nesting: the
// witness signature lives inside block_header, next to raw_data. A struct
// that put it at the block top level would silently lose every signature.
func TestUnmarshalBlockHeaderShape(t *testing.T) {
	t.Parallel()
	raw := `{
		"blockID": "0000000003fe262d52bfa4b2814f816fd2e57af5b98a33d60d8630a03a908e0e",
		"block_header": {
			"raw_data": {
				"number": 66987565,
				"txTrieRoot": "faf8fe3858339ead25cc892461c82a59b84dca4a51c45b026676bf3f45a352a2",
				"witness_address": "41b2f713d57dbcec679d93a8849fa0cd0e4db594ba",
				"parentHash": "0000000003fe262c85cd6b02033f4c3e5c1efa35de256a17bd906dc61fb1aeed",
				"version": 34,
				"timestamp": 1777445121000
			},
			"witness_signature": "5b3cf6cb15d52947989f7726f4907a144b39ccd667a1a0f98707b40cdfe65b96173d"
		}
	}`

	var block Block
	require.NoError(t, json.Unmarshal([]byte(raw), &block))
	assert.Equal(t, "0000000003fe262d52bfa4b2814f816fd2e57af5b98a33d60d8630a03a908e0e", block.BlockID)
	assert.Equal(t, int64(66987565), block.BlockHeader.RawData.Number)
	assert.Equal(t, int64(1777445121000), block.BlockHeader.RawData.Timestamp)
	assert.Equal(t, "0000000003fe262c85cd6b02033f4c3e5c1efa35de256a17bd906dc61fb1aeed", block.BlockHeader.RawData.ParentHash)
	assert.Equal(t, "41b2f713d57dbcec679d93a8849fa0cd0e4db594ba", block.BlockHeader.RawData.WitnessAddress)
	assert.Equal(t, int64(34), block.BlockHeader.RawData.Version)
	assert.Equal(t, "faf8fe3858339ead25cc892461c82a59b84dca4a51c45b026676bf3f45a352a2", block.BlockHeader.RawData.TxTrieRoot)
	assert.Equal(t, "5b3cf6cb15d52947989f7726f4907a144b39ccd667a1a0f98707b40cdfe65b96173d", block.BlockHeader.WitnessSignature)
}

// TestUnmarshalTransactionShapes covers the transfer and the smart-contract
// trigger body: ret stays nested in its list, and the contract value keeps
// its polymorphic keys for doc_builders to extract.
func TestUnmarshalTransactionShapes(t *testing.T) {
	t.Parallel()
	raw := `{
		"txID": "f52df87da4991d1cd8333f48526b2f55375a22e2705e9eaf7edce2a12e53827b",
		"raw_data": {
			"contract": [
				{
					"type": "TriggerSmartContract",
					"parameter": {
						"value": {
							"owner_address": "41072a35d3cd7112c08fe2b04f5c99e73e0a3c099e",
							"contract_address": "41a614f803b6fd780986a42c78ec9c7f77e6ded13c",
							"call_value": 1000000,
							"data": "a9059cbb"
						}
					}
				}
			],
			"expiration": 1578594912345,
			"timestamp": 1578594884000,
			"fee_limit": 1000000000
		},
		"ret": [{"contractRet": "SUCCESS"}],
		"signature": ["0ecka8bYSRO0uPr9p43rkwpbQBOIPx1kn2fT69sBBQ7FF4KGWZrGnMn18VArOuMBX0AbT3ybsDImPWOfZjfkBQ=="]
	}`

	var block Block
	require.NoError(t, json.Unmarshal([]byte(`{"blockID": "b", "block_header": {"raw_data": {"number": 1, "timestamp": 2, "parentHash": "p"}}, "transactions": [`+raw+`]}`), &block))
	require.Len(t, block.Transactions, 1)

	tx := block.Transactions[0]
	assert.Equal(t, "f52df87da4991d1cd8333f48526b2f55375a22e2705e9eaf7edce2a12e53827b", tx.TxID)
	require.Len(t, tx.RawData.Contract, 1)
	assert.Equal(t, "TriggerSmartContract", tx.RawData.Contract[0].Type)
	assert.Equal(t, "41072a35d3cd7112c08fe2b04f5c99e73e0a3c099e", tx.RawData.Contract[0].Parameter.Value["owner_address"])
	assert.Equal(t, "41a614f803b6fd780986a42c78ec9c7f77e6ded13c", tx.RawData.Contract[0].Parameter.Value["contract_address"])
	require.Len(t, tx.Ret, 1)
	assert.Equal(t, "SUCCESS", tx.Ret[0].ContractRet)
	assert.Equal(t, int64(1000000000), tx.RawData.FeeLimit)
	assert.Len(t, tx.Signature, 1)
}

// TestUnmarshalTransactionInfoReceipt covers the receipt list shapes: fee
// may be absent (0), topics are a plain string array, and internal
// transactions carry callValueInfo with and without assetName.
func TestUnmarshalTransactionInfoReceipt(t *testing.T) {
	t.Parallel()
	raw := `[
		{
			"id": "98d0f4149bf8023a9df4d2b754f6c728a5b6d021c497c6cd5452b97f01b0548",
			"blockNumber": 1000000,
			"blockTimeStamp": 1578594852000,
			"fee": 433850,
			"receipt": {"result": "SUCCESS", "energy_fee": 433850, "energy_usage_total": 43385, "net_usage": 384},
			"contractResult": ["00"],
			"log": [
				{
					"address": "8d9dd663cf90256fdf2faba2a1be71f8a0147f8c",
					"topics": ["50b0fde04a05002ee4ba3c03973059b7d8af741148bf53ff213e39d2867a7930"],
					"data": "0x"
				}
			],
			"internal_transactions": [
				{
					"hash": "ef89ade61942aa3371a3f006e6406fd4758ef648ee38c04f70a57b296668d3ed",
					"caller_address": "418d9dd663cf90256fdf2faba2a1be71f8a0147f8c",
					"note": "63616c6c",
					"transferTo_address": "418d9dd663cf90256fdf2faba2a1be71f8a0147f8c",
					"callValueInfo": [{}]
				},
				{
					"caller_address": "418d9dd663cf90256fdf2faba2a1be71f8a0147f8c",
					"transferTo_address": "413a5bfb715d2086ec513b64e98531e2f58154c44c",
					"callValueInfo": [{"callValue": 700000}],
					"rejected": true
				}
			]
		},
		{
			"id": "7faabd22623eb53467d34b2a417262f6df75433566c104b6c6eec5a227090df6",
			"blockNumber": 1000000,
			"blockTimeStamp": 1578594852000,
			"receipt": {"result": "SUCCESS"}
		}
	]`

	var infos []*TransactionInfo
	require.NoError(t, json.Unmarshal([]byte(raw), &infos))
	require.Len(t, infos, 2)

	info := infos[0]
	assert.Equal(t, "98d0f4149bf8023a9df4d2b754f6c728a5b6d021c497c6cd5452b97f01b0548", info.ID)
	assert.Equal(t, int64(1000000), info.BlockNumber)
	assert.Equal(t, int64(1578594852000), info.BlockTimeStamp)
	assert.Equal(t, int64(433850), info.Fee)
	assert.Equal(t, "SUCCESS", info.Receipt.Result)
	assert.Equal(t, int64(433850), info.Receipt.EnergyFee)
	require.Len(t, info.Log, 1)
	assert.Equal(t, []string{"50b0fde04a05002ee4ba3c03973059b7d8af741148bf53ff213e39d2867a7930"}, info.Log[0].Topics)
	require.Len(t, info.InternalTransactions, 2)
	assert.True(t, info.InternalTransactions[1].Rejected)

	assert.Equal(t, int64(0), infos[1].Fee, "absent fee decodes to 0")
	assert.NotNil(t, infos[1], "second receipt decodes")
}

// TestUnmarshalEmptyBlockObject pins the not-found marker: an unknown
// height or hash decodes to the zero value, whose empty BlockID the client
// maps to a "not found" error.
func TestUnmarshalEmptyBlockObject(t *testing.T) {
	t.Parallel()
	var block Block
	require.NoError(t, json.Unmarshal([]byte(`{}`), &block))
	assert.Empty(t, block.BlockID)
	assert.Equal(t, int64(0), block.BlockHeader.RawData.Number)
}
