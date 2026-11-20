package evm

// Tests for the raw wire layer. The block suite covers the tolerant
// passthrough conversion (ConvertRawBlock): header passthrough, identity
// fields read from the response, the one fee rule, unknown-envelope
// passthrough, and the malformed-input hard errors. The receipt suite
// covers the normalization pass: direct unmarshal of
// eth_getTransactionReceipt / eth_getBlockReceipts responses into the
// package's receipt representation.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// convertFixtureBlock marshals a block map and converts it through the
// default passthrough helper.
func convertFixtureBlock(t *testing.T, block map[string]any) *Block {
	t.Helper()
	raw, err := json.Marshal(block)
	require.NoError(t, err)
	converted, err := ConvertRawBlock(raw)
	require.NoError(t, err)
	return converted
}

// rawTx builds one raw transaction object from field overrides on top of a
// minimal well-formed 0x71-style tx.
func rawTx(overrides map[string]any) map[string]any {
	tx := map[string]any{
		"type":     "0x71",
		"hash":     rawTxHash,
		"nonce":    "0x1",
		"gasPrice": "0x3b9aca00",
		"gas":      "0x5208",
		"to":       "0x0000000000000000000000000000000000000002",
		"value":    "0x1",
		"input":    "0xdeadbeef",
		"chainId":  "0x144",
		"from":     "0x1111111111111111111111111111111111111111",
		"yParity":  "0x0",
		"v":        "0x1",
		"r":        "0x2",
		"s":        "0x3",
	}
	for key, value := range overrides {
		if value == nil {
			delete(tx, key)
			continue
		}
		tx[key] = value
	}
	return tx
}

// conversionBlock builds a raw block fixture carrying the given txs, with
// block-level overrides applied after the defaults. The shared block
// fixture omits baseFeePerGas, so the post-London default is added here and
// the absent-field test deletes it.
func conversionBlock(txs []any, overrides map[string]any) map[string]any {
	block := fullBlockResponse(testBlockNumberHex, txs)
	block["hash"] = rawBlockHash
	block["size"] = "0x1a4"
	block["totalDifficulty"] = "0x1f4"
	block["baseFeePerGas"] = "0x0"
	for key, value := range overrides {
		if value == nil {
			delete(block, key)
			continue
		}
		block[key] = value
	}
	return block
}

// --- header passthrough ---

func TestConvertRawBlock_HeaderPassthrough(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock([]any{rawTx(nil)}, nil))

	assert.Equal(t, rawBlockHash, block.Hash, "the block hash comes from the response, never recomputed")
	assert.Equal(t, "100", block.Number)
	assert.Equal(t, "420", block.Size, "size comes from the response")
	assert.Equal(t, "500", block.TotalDifficulty, "total difficulty comes from the response")
	assert.Equal(t, "16777216", block.GasLimit, "0x1000000 renders as a decimal string")
	assert.Equal(t, "21000", block.GasUsed)
	assert.Equal(t, "0", block.Nonce, "the wire nonce is a hex quantity rendered decimal")
	assert.Equal(t, "0", block.Difficulty)
	assert.Equal(t, "0", block.BaseFeePerGas, "a zero base fee is stored, not omitted")
	assert.Equal(t, common.HexToAddress("0x0000000000000000000000000000000000000000").Hex(), block.Miner)
	assert.Equal(t, strings.Repeat("0", 512), block.LogsBloom,
		"logsBloom keeps the typed path's bare-hex convention, no 0x prefix")
	assert.Empty(t, block.ExtraData, "an empty extraData stores as empty bare hex")
	assert.NotEmpty(t, block.Timestamp)
	assert.NotEmpty(t, block.ParentHash)
	assert.NotEmpty(t, block.StateRoot)
	assert.NotEmpty(t, block.Sha3Uncles)
	assert.NotEmpty(t, block.TransactionsRoot)
	assert.NotEmpty(t, block.ReceiptsRoot)
	assert.NotEmpty(t, block.MixHash)
	assert.NotNil(t, block.Uncles)
	assert.Empty(t, block.Uncles)
}

func TestConvertRawBlock_AbsentOptionalHeaderFieldsStayEmpty(t *testing.T) {
	t.Parallel()
	overrides := map[string]any{
		"totalDifficulty": nil,
		"baseFeePerGas":   nil,
		"difficulty":      nil,
		"size":            nil,
		"nonce":           nil,
		"mixHash":         nil,
	}
	block := convertFixtureBlock(t, conversionBlock(nil, overrides))

	assert.Empty(t, block.TotalDifficulty, "a chain that omits totalDifficulty (Bor, modern Ethereum) stays empty naturally")
	assert.Empty(t, block.BaseFeePerGas)
	assert.Empty(t, block.Difficulty)
	assert.Empty(t, block.Size)
	assert.Empty(t, block.Nonce)
	assert.Empty(t, block.MixHash)
	assert.Empty(t, block.ExtraData, "an absent extraData stores empty")
}

func TestConvertRawBlock_UnclesFromResponse(t *testing.T) {
	t.Parallel()
	uncle := "0x1111111111111111111111111111111111111111111111111111111111111111"
	block := convertFixtureBlock(t, conversionBlock(nil, map[string]any{"uncles": []any{uncle, rawBlockHash}}))

	require.Len(t, block.Uncles, 2)
	assert.Equal(t, uncle, block.Uncles[0], "uncle hashes come from the response")
	assert.Equal(t, rawBlockHash, block.Uncles[1])
}

// --- per-tx identity passthrough ---

func TestConvertRawBlock_TxIdentityFromResponse(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock([]any{rawTx(nil)}, nil))

	require.Len(t, block.Transactions, 1)
	tx := block.Transactions[0]
	assert.Equal(t, rawTxHash, tx.Hash, "the tx hash comes from the response's own hash field")
	assert.Equal(t, common.HexToAddress("0x1111111111111111111111111111111111111111").Hex(), tx.From,
		"from comes from the response, normalized to the checksummed format the typed path emitted")
	assert.Equal(t, "113", tx.Type, "the wire hex type renders as the decimal string the typed path emitted")
	assert.Equal(t, "0x0", tx.YParity, "yParity is passthrough in the wire's own format")
	assert.Equal(t, "1", tx.V)
	assert.Equal(t, "2", tx.R)
	assert.Equal(t, "3", tx.S)
	assert.Equal(t, "1", tx.Value)
	assert.Equal(t, "21000", tx.Gas)
	assert.Equal(t, "1", tx.Nonce)
	assert.Equal(t, "324", tx.ChainID)
	assert.Equal(t, "1000000000", tx.GasPrice)
	assert.Equal(t, "0xdeadbeef", tx.Input)
	assert.Equal(t, common.HexToAddress("0x0000000000000000000000000000000000000002").Hex(), tx.To)
	assert.Equal(t, rawBlockHash, tx.BlockHash)
	assert.Equal(t, "100", tx.BlockNumber)
	assert.Equal(t, 0, tx.TransactionIndex)
	assert.True(t, tx.Status, "signed payloads default to success; the receipt merge overwrites it")
}

func TestConvertRawBlock_TransactionIndexesArePositional(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock([]any{rawTx(nil), rawTx(nil)}, nil))

	require.Len(t, block.Transactions, 2)
	assert.Equal(t, 0, block.Transactions[0].TransactionIndex)
	assert.Equal(t, 1, block.Transactions[1].TransactionIndex)
}

func TestConvertRawBlock_AccessListPassthrough(t *testing.T) {
	t.Parallel()
	tx := rawTx(map[string]any{
		"accessList": []any{
			map[string]any{
				"address":     "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2",
				"storageKeys": []any{"0x0000000000000000000000000000000000000000000000000000000000000001"},
			},
		},
	})
	block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))

	require.Len(t, block.Transactions, 1)
	require.Len(t, block.Transactions[0].AccessList, 1)
	entry := block.Transactions[0].AccessList[0]
	assert.Equal(t, common.HexToAddress("0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2").Hex(), entry.Address,
		"access-list addresses keep the checksummed format")
	require.Len(t, entry.StorageKeys, 1)
	assert.Equal(t, "0x0000000000000000000000000000000000000000000000000000000000000001", entry.StorageKeys[0])
}

func TestConvertRawBlock_ContractCreationHasEmptyTo(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock([]any{rawTx(map[string]any{"to": nil})}, nil))

	require.Len(t, block.Transactions, 1)
	assert.Empty(t, block.Transactions[0].To, "a null to field is a contract creation")
}

// --- the one fee rule ---

// TestConvertRawBlock_FeeRule pins the one fee rule: a tx that carries a
// gasPrice field stores its wire value; types 0x2/0x3 without one keep the
// stored semantic gasPrice = maxFeePerGas; unknown and legacy types stay
// empty where the wire omits fees. The blob row pins the deliberate delta
// the golden gate documents: blob and setcode txs store the wire
// maxFeePerGas/maxPriorityFeePerGas instead of the typed path's empty
// strings.
func TestConvertRawBlock_FeeRule(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                     string
		tx                       map[string]any
		wantGasPrice             string
		wantMaxFeePerGas         string
		wantMaxPriorityFeePerGas string
	}{
		{
			name:         "type 2 with gasPrice stores the wire value",
			tx:           rawTx(map[string]any{"type": "0x2", "gasPrice": "0xa", "maxFeePerGas": "0xb", "maxPriorityFeePerGas": "0xc"}),
			wantGasPrice: "10", wantMaxFeePerGas: "11", wantMaxPriorityFeePerGas: "12",
		},
		{
			name:         "type 2 without gasPrice keeps gasPrice = maxFeePerGas",
			tx:           rawTx(map[string]any{"type": "0x2", "gasPrice": nil, "maxFeePerGas": "0xb", "maxPriorityFeePerGas": "0xc"}),
			wantGasPrice: "11", wantMaxFeePerGas: "11", wantMaxPriorityFeePerGas: "12",
		},
		{
			name:         "type 3 without gasPrice keeps gasPrice = maxFeePerGas",
			tx:           rawTx(map[string]any{"type": "0x3", "gasPrice": nil, "maxFeePerGas": "0xb", "maxPriorityFeePerGas": "0xc"}),
			wantGasPrice: "11", wantMaxFeePerGas: "11", wantMaxPriorityFeePerGas: "12",
		},
		{
			name:         "unknown type without gasPrice stays empty",
			tx:           rawTx(map[string]any{"type": "0xff", "gasPrice": nil, "maxFeePerGas": "0xb"}),
			wantGasPrice: "", wantMaxFeePerGas: "11", wantMaxPriorityFeePerGas: "",
		},
		{
			name:         "legacy without gasPrice stays empty",
			tx:           rawTx(map[string]any{"type": "0x0", "gasPrice": nil}),
			wantGasPrice: "", wantMaxFeePerGas: "", wantMaxPriorityFeePerGas: "",
		},
		{
			name:         "blob type 3 stores the wire fee fields",
			tx:           rawTx(map[string]any{"type": "0x3", "gasPrice": "0x1", "maxFeePerGas": "0x2", "maxPriorityFeePerGas": "0x3"}),
			wantGasPrice: "1", wantMaxFeePerGas: "2", wantMaxPriorityFeePerGas: "3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			block := convertFixtureBlock(t, conversionBlock([]any{tc.tx}, nil))
			require.Len(t, block.Transactions, 1)
			tx := block.Transactions[0]
			assert.Equal(t, tc.wantGasPrice, tx.GasPrice, "gasPrice follows the one fee rule")
			assert.Equal(t, tc.wantMaxFeePerGas, tx.MaxFeePerGas, "the wire fee fields are stored for every type")
			assert.Equal(t, tc.wantMaxPriorityFeePerGas, tx.MaxPriorityFeePerGas)
		})
	}
}

// --- envelope tolerance ---

// TestConvertRawBlock_EnvelopeTolerance pins the passthrough rule: the
// envelope type is metadata, not identity, so unknown and chain-specific
// envelopes (OP deposit, Arbitrum L1 message) convert with zero per-chain
// code, foreign fields are ignored, and a missing type is the legacy
// default the typed path produced.
func TestConvertRawBlock_EnvelopeTolerance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		tx           map[string]any
		wantType     string
		wantGasPrice string
		wantChainID  string
		wantV        string
		wantR        string
		wantS        string
	}{
		{
			name:     "unknown 0xff with a foreign field",
			tx:       rawTx(map[string]any{"type": "0xff", "someUnknownField": "0x1"}),
			wantType: "255", wantGasPrice: "1000000000", wantChainID: "324", wantV: "1", wantR: "2", wantS: "3",
		},
		{
			name:     "OP deposit 0x7e: zeroed signatures, empty fees",
			tx:       rawTx(map[string]any{"type": "0x7e", "gasPrice": nil, "chainId": nil, "v": "0x0", "r": "0x0", "s": "0x0"}),
			wantType: "126", wantGasPrice: "", wantChainID: "", wantV: "0", wantR: "0", wantS: "0",
		},
		{
			name:     "Arbitrum L1 message 0x64: aliased from, empty fees",
			tx:       rawTx(map[string]any{"type": "0x64", "gasPrice": nil, "chainId": nil, "v": "0x0", "r": "0x0", "s": "0x0"}),
			wantType: "100", wantGasPrice: "", wantChainID: "", wantV: "0", wantR: "0", wantS: "0",
		},
		{
			name:     "missing type is legacy",
			tx:       rawTx(map[string]any{"type": nil}),
			wantType: "0", wantGasPrice: "1000000000", wantChainID: "324", wantV: "1", wantR: "2", wantS: "3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			block := convertFixtureBlock(t, conversionBlock([]any{tc.tx}, nil))
			require.Len(t, block.Transactions, 1)
			tx := block.Transactions[0]
			assert.Equal(t, rawTxHash, tx.Hash, "identity comes from the response's own hash field")
			assert.Equal(t, common.HexToAddress("0x1111111111111111111111111111111111111111").Hex(), tx.From,
				"the from field is explicit in the response and checksummed")
			assert.Equal(t, "0x0", tx.YParity, "yParity passthrough in the wire's own format")
			assert.Equal(t, tc.wantType, tx.Type, "the wire hex type renders as the decimal string")
			assert.Equal(t, tc.wantGasPrice, tx.GasPrice)
			assert.Equal(t, tc.wantChainID, tx.ChainID)
			assert.Equal(t, tc.wantV, tx.V)
			assert.Equal(t, tc.wantR, tx.R)
			assert.Equal(t, tc.wantS, tx.S)
			assert.Empty(t, tx.MaxFeePerGas, "these envelopes carry no maxFeePerGas on the wire")
		})
	}
}

// --- malformed input is a hard error ---

// TestConvertRawBlock_HardErrors pins the hard-error boundary: malformed
// input and missing identity fields fail the conversion instead of storing
// guessed values, and the error names the failing tx position.
func TestConvertRawBlock_HardErrors(t *testing.T) {
	t.Parallel()

	_, err := ConvertRawBlock(json.RawMessage(`not-an-object`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode raw block")

	cases := []struct {
		name           string
		txs            []any
		blockOverrides map[string]any
		wantErr        string
	}{
		{name: "block without number", blockOverrides: map[string]any{"number": nil}, wantErr: "no number"},
		{name: "block without hash", blockOverrides: map[string]any{"hash": nil}, wantErr: "no hash"},
		{name: "tx without hash", txs: []any{rawTx(map[string]any{"hash": nil})}, wantErr: "missing or malformed hash"},
		{name: "tx with malformed hash", txs: []any{rawTx(map[string]any{"hash": "0x123"})}, wantErr: "missing or malformed hash"},
		{name: "tx without from", txs: []any{rawTx(map[string]any{"from": nil})}, wantErr: "missing or malformed from"},
		{name: "tx with malformed from", txs: []any{rawTx(map[string]any{"from": "0x1234"})}, wantErr: "missing or malformed from"},
		{name: "tx object that is not an object", txs: []any{"not-an-object"}, wantErr: "failed to decode"},
		{name: "error names the failing tx position", txs: []any{rawTx(nil), rawTx(map[string]any{"hash": nil})}, wantErr: "transaction 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(conversionBlock(tc.txs, tc.blockOverrides))
			require.NoError(t, err)
			_, err = ConvertRawBlock(raw)
			require.Error(t, err, "broken responses must fail instead of storing guessed values")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestConvertRawBlock_EmptyBlockStaysRepresentable(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock(nil, nil))

	assert.Empty(t, block.Transactions, "a receiptless, txless block stores an empty transaction list")
}

// --- receipt normalization (convertRawReceipt / convertRawLog) ---

// receiptFixture marshals a receipt map into the raw bytes convertRawReceipt
// consumes.
func receiptFixture(t *testing.T, receipt map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(receipt)
	require.NoError(t, err)
	return raw
}

// baseRawReceipt is a standard post-London successful receipt in the wire's
// hex quantity conventions.
func baseRawReceipt() map[string]any {
	return map[string]any{
		"transactionHash":   rawTxHash,
		"transactionIndex":  "0x6",
		"blockHash":         rawBlockHash,
		"blockNumber":       testBlockNumberHex,
		"cumulativeGasUsed": "0x1029844",
		"gasUsed":           "0x84b3b",
		"effectiveGasPrice": "0x4a817c800",
		"contractAddress":   "0x0000000000000000000000000000000000000000",
		"logsBloom":         "0x" + strings.Repeat("0", 512),
		"logs":              []any{},
		"status":            "0x1",
	}
}

func TestConvertRawReceipt_Success(t *testing.T) {
	t.Parallel()
	receipt, err := convertRawReceipt(receiptFixture(t, baseRawReceipt()))
	require.NoError(t, err)

	assert.Equal(t, rawTxHash, receipt.TransactionHash)
	assert.Equal(t, "6", receipt.TransactionIndex, "the wire hex quantity renders as today's decimal string")
	assert.Equal(t, rawBlockHash, receipt.BlockHash)
	assert.Equal(t, "100", receipt.BlockNumber)
	assert.Equal(t, "16947268", receipt.CumulativeGasUsed)
	assert.Equal(t, "543547", receipt.GasUsed)
	assert.Equal(t, "20000000000", receipt.EffectiveGasPrice,
		"the wire value 0x4a817c800 must survive the decode as a decimal string")
	assert.Equal(t, "1", receipt.Status, "the wire's hex status renders as today's decimal format")
	assert.Empty(t, receipt.ContractAddress, "a zero contract address stores empty")
	assert.Empty(t, receipt.Logs)
	assert.Empty(t, receipt.From, "from/to stay unpopulated, matching the deleted convertGethReceipt")
	assert.Empty(t, receipt.To)
}

// TestConvertRawReceipt_StatusAndFeeRules pins the outcome and fee
// conventions: a status of 0x0 or an absent status field indexes failed
// (an unseen outcome must not default to success); a pre-Byzantium receipt
// carries a post-state root instead of a status field and indexes as
// successful, matching how explorers report pre-Byzantium transactions;
// and an omitted effectiveGasPrice (pre-London nodes) stores empty.
func TestConvertRawReceipt_StatusAndFeeRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                  string
		mutate                func(receipt map[string]any)
		wantStatus            string
		wantEffectiveGasPrice string
	}{
		{
			name:       "failed status",
			mutate:     func(receipt map[string]any) { receipt["status"] = "0x0" },
			wantStatus: "0", wantEffectiveGasPrice: "20000000000",
		},
		{
			name:       "absent status stays failed",
			mutate:     func(receipt map[string]any) { delete(receipt, "status") },
			wantStatus: "0", wantEffectiveGasPrice: "20000000000",
		},
		{
			name: "pre-Byzantium root indexes success",
			mutate: func(receipt map[string]any) {
				delete(receipt, "status")
				receipt["root"] = "0x" + strings.Repeat("a", 64)
			},
			wantStatus: "1", wantEffectiveGasPrice: "20000000000",
		},
		{
			name:       "absent effectiveGasPrice stays empty",
			mutate:     func(receipt map[string]any) { delete(receipt, "effectiveGasPrice") },
			wantStatus: "1", wantEffectiveGasPrice: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receipt := baseRawReceipt()
			tc.mutate(receipt)
			converted, err := convertRawReceipt(receiptFixture(t, receipt))
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, converted.Status)
			assert.Equal(t, tc.wantEffectiveGasPrice, converted.EffectiveGasPrice)
		})
	}
}

func TestConvertRawReceipt_ContractAddress(t *testing.T) {
	t.Parallel()

	t.Run("deployed contract keeps its address", func(t *testing.T) {
		t.Parallel()
		receipt := baseRawReceipt()
		receipt["contractAddress"] = "0x6b175474e89094c44da98b954eedeac495271d0f"

		converted, err := convertRawReceipt(receiptFixture(t, receipt))
		require.NoError(t, err)
		assert.Equal(t, common.HexToAddress("0x6b175474e89094c44da98b954eedeac495271d0f").Hex(), converted.ContractAddress,
			"the contract address keeps the checksummed convention")
	})

	t.Run("absent contract address stores empty", func(t *testing.T) {
		t.Parallel()
		receipt := baseRawReceipt()
		delete(receipt, "contractAddress")

		converted, err := convertRawReceipt(receiptFixture(t, receipt))
		require.NoError(t, err)
		assert.Empty(t, converted.ContractAddress)
	})
}

func TestConvertRawReceipt_LogNormalization(t *testing.T) {
	t.Parallel()
	receipt := baseRawReceipt()
	receipt["logs"] = []any{
		map[string]any{
			"address":          "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2",
			"topics":           []any{"0x40e9cecb9f5f1f1c5b9c97dec2917b7ee92e57ba5563708daca94dd84ad7112f"},
			"data":             "0xdeadbeef",
			"blockNumber":      testBlockNumberHex,
			"transactionHash":  rawTxHash,
			"transactionIndex": "0x6",
			"blockHash":        rawBlockHash,
			"logIndex":         "0x3",
			"removed":          false,
		},
	}

	converted, err := convertRawReceipt(receiptFixture(t, receipt))
	require.NoError(t, err)

	require.Len(t, converted.Logs, 1)
	log := converted.Logs[0]
	assert.NotEmpty(t, log.Address, "the log address is kept in the checksummed convention")
	assert.Equal(t, []string{"0x40e9cecb9f5f1f1c5b9c97dec2917b7ee92e57ba5563708daca94dd84ad7112f"}, log.Topics)
	assert.Equal(t, "deadbeef", log.Data, "log data keeps the typed path's bare-hex convention, no 0x prefix")
	assert.Equal(t, "100", log.BlockNumber)
	assert.Equal(t, rawTxHash, log.TransactionHash)
	assert.Equal(t, 6, log.TransactionIndex, "log indices decode as today's int convention")
	assert.Equal(t, rawBlockHash, log.BlockHash)
	assert.Equal(t, 3, log.LogIndex)
	assert.False(t, log.Removed)
}

func TestConvertRawReceipt_MalformedJSONFails(t *testing.T) {
	t.Parallel()
	_, err := convertRawReceipt(json.RawMessage(`not-an-object`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode raw receipt")
}

func TestConvertRawReceipt_MalformedLogFails(t *testing.T) {
	t.Parallel()
	receipt := baseRawReceipt()
	receipt["logs"] = []any{"not-an-object"}

	_, err := convertRawReceipt(receiptFixture(t, receipt))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "receipt log 0")
}
