package evm

// Tests for the default adapter's tolerant passthrough conversion
// (ConvertRawBlock): header passthrough, identity fields read from the
// response, the one fee rule, unknown-envelope passthrough, and the
// malformed-input hard errors.

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

func TestConvertRawBlock_MissingTypeIsLegacy(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock([]any{rawTx(map[string]any{"type": nil})}, nil))

	require.Len(t, block.Transactions, 1)
	assert.Equal(t, "0", block.Transactions[0].Type,
		"a tx object without a type field is a legacy tx, the typed path's type 0")
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

func TestConvertRawBlock_FeeRule(t *testing.T) {
	t.Parallel()

	t.Run("type 2 with gasPrice stores the wire value", func(t *testing.T) {
		t.Parallel()
		tx := rawTx(map[string]any{"type": "0x2", "gasPrice": "0xa", "maxFeePerGas": "0xb", "maxPriorityFeePerGas": "0xc"})
		block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))
		assert.Equal(t, "10", block.Transactions[0].GasPrice)
		assert.Equal(t, "11", block.Transactions[0].MaxFeePerGas)
		assert.Equal(t, "12", block.Transactions[0].MaxPriorityFeePerGas)
	})

	t.Run("type 2 without gasPrice keeps gasPrice = maxFeePerGas", func(t *testing.T) {
		t.Parallel()
		tx := rawTx(map[string]any{"type": "0x2", "gasPrice": nil, "maxFeePerGas": "0xb", "maxPriorityFeePerGas": "0xc"})
		block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))
		assert.Equal(t, "11", block.Transactions[0].GasPrice, "today's stored semantic for EIP-1559 txs is preserved")
	})

	t.Run("type 3 without gasPrice keeps gasPrice = maxFeePerGas", func(t *testing.T) {
		t.Parallel()
		tx := rawTx(map[string]any{"type": "0x3", "gasPrice": nil, "maxFeePerGas": "0xb", "maxPriorityFeePerGas": "0xc"})
		block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))
		assert.Equal(t, "11", block.Transactions[0].GasPrice)
	})

	t.Run("unknown type without gasPrice stays empty", func(t *testing.T) {
		t.Parallel()
		tx := rawTx(map[string]any{"type": "0xff", "gasPrice": nil, "maxFeePerGas": "0xb"})
		block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))
		assert.Empty(t, block.Transactions[0].GasPrice,
			"unknown types passthrough the fee fields the raw tx carries and stay empty where it omits them")
		assert.Equal(t, "11", block.Transactions[0].MaxFeePerGas)
	})

	t.Run("legacy without gasPrice stays empty", func(t *testing.T) {
		t.Parallel()
		tx := rawTx(map[string]any{"type": "0x0", "gasPrice": nil})
		block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))
		assert.Empty(t, block.Transactions[0].GasPrice)
	})
}

// TestConvertRawBlock_FeeFieldPassthroughOnBlobTx pins the deliberate
// delta the golden gate documents: blob and setcode txs store the wire
// maxFeePerGas/maxPriorityFeePerGas instead of the typed path's empty
// strings.
func TestConvertRawBlock_FeeFieldPassthroughOnBlobTx(t *testing.T) {
	t.Parallel()
	tx := rawTx(map[string]any{
		"type":                 "0x3",
		"gasPrice":             "0x1",
		"maxFeePerGas":         "0x2",
		"maxPriorityFeePerGas": "0x3",
	})
	block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))

	require.Len(t, block.Transactions, 1)
	assert.Equal(t, "2", block.Transactions[0].MaxFeePerGas,
		"the wire value is stored for every type, replacing the typed path's empty string")
	assert.Equal(t, "3", block.Transactions[0].MaxPriorityFeePerGas)
}

// --- unknown envelope and class-C passthrough ---

func TestConvertRawBlock_UnknownTypePassthrough(t *testing.T) {
	t.Parallel()
	tx := rawTx(map[string]any{"type": "0xff", "someUnknownField": "0x1"})
	block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))

	require.Len(t, block.Transactions, 1)
	assert.Equal(t, "255", block.Transactions[0].Type,
		"an unknown envelope type passes through with no error and no substitution")
	assert.Equal(t, rawTxHash, block.Transactions[0].Hash)
}

func TestConvertRawBlock_OpDepositPassthrough(t *testing.T) {
	t.Parallel()
	// OP Mainnet deposit: explicit identity, zeroed signatures, empty fees.
	tx := rawTx(map[string]any{
		"type":     "0x7e",
		"gasPrice": nil,
		"chainId":  nil,
		"v":        "0x0",
		"r":        "0x0",
		"s":        "0x0",
	})
	block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))

	require.Len(t, block.Transactions, 1)
	txOut := block.Transactions[0]
	assert.Equal(t, "126", txOut.Type, "0x7e becomes the decimal 126")
	assert.Equal(t, common.HexToAddress("0x1111111111111111111111111111111111111111").Hex(), txOut.From,
		"the deposit's from is explicit in the response")
	assert.Equal(t, "0x0", txOut.YParity)
	assert.Equal(t, "0", txOut.V)
	assert.Equal(t, "0", txOut.R)
	assert.Equal(t, "0", txOut.S, "zeroed signatures passthrough as zeros")
	assert.Empty(t, txOut.GasPrice, "deposits carry no fee fields")
	assert.Empty(t, txOut.MaxFeePerGas)
	assert.Empty(t, txOut.ChainID)
}

func TestConvertRawBlock_ArbitrumL1MessagePassthrough(t *testing.T) {
	t.Parallel()
	// Arbitrum L1 message: aliased from, empty fee fields.
	tx := rawTx(map[string]any{"type": "0x64", "gasPrice": nil, "chainId": nil, "v": "0x0", "r": "0x0", "s": "0x0"})
	block := convertFixtureBlock(t, conversionBlock([]any{tx}, nil))

	require.Len(t, block.Transactions, 1)
	txOut := block.Transactions[0]
	assert.Equal(t, "100", txOut.Type)
	assert.Equal(t, common.HexToAddress("0x1111111111111111111111111111111111111111").Hex(), txOut.From)
	assert.Empty(t, txOut.GasPrice)
	assert.Equal(t, "0", txOut.V)
}

// --- malformed input is a hard error ---

func TestConvertRawBlock_HardErrors(t *testing.T) {
	t.Parallel()

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		_, err := ConvertRawBlock(json.RawMessage(`not-an-object`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode raw block")
	})

	t.Run("block without number", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock(nil, map[string]any{"number": nil})
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no number")
	})

	t.Run("block without hash", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock(nil, map[string]any{"hash": nil})
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no hash")
	})

	t.Run("tx without hash", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock([]any{rawTx(map[string]any{"hash": nil})}, nil)
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing or malformed hash")
	})

	t.Run("tx with malformed hash", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock([]any{rawTx(map[string]any{"hash": "0x123"})}, nil)
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing or malformed hash")
	})

	t.Run("tx without from", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock([]any{rawTx(map[string]any{"from": nil})}, nil)
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing or malformed from")
	})

	t.Run("tx with malformed from", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock([]any{rawTx(map[string]any{"from": "0x1234"})}, nil)
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing or malformed from")
	})

	t.Run("tx object that is not an object", func(t *testing.T) {
		t.Parallel()
		block := conversionBlock([]any{"not-an-object"}, nil)
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		_, err = ConvertRawBlock(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode")
	})
}

func TestConvertRawBlock_EmptyBlockStaysRepresentable(t *testing.T) {
	t.Parallel()
	block := convertFixtureBlock(t, conversionBlock(nil, nil))

	assert.Empty(t, block.Transactions, "a receiptless, txless block stores an empty transaction list")
}

func TestConvertRawBlock_ErrorNamesTheTxIndex(t *testing.T) {
	t.Parallel()
	block := conversionBlock([]any{rawTx(nil), rawTx(map[string]any{"hash": nil})}, nil)
	raw, err := json.Marshal(block)
	require.NoError(t, err)

	_, err = ConvertRawBlock(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction 1", "the error must name the failing tx position")
}
