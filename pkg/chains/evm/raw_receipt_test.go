package evm

// Tests for the raw receipt path: direct unmarshal of
// eth_getTransactionReceipt / eth_getBlockReceipts responses into the
// package's receipt representation plus the normalization pass.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestConvertRawReceipt_FailedStatus(t *testing.T) {
	t.Parallel()
	receipt := baseRawReceipt()
	receipt["status"] = "0x0"

	converted, err := convertRawReceipt(receiptFixture(t, receipt))
	require.NoError(t, err)
	assert.Equal(t, "0", converted.Status)
}

func TestConvertRawReceipt_AbsentStatusStaysFailed(t *testing.T) {
	t.Parallel()
	// A node that omits the status field entirely: an unseen outcome must
	// not default to success.
	receipt := baseRawReceipt()
	delete(receipt, "status")

	converted, err := convertRawReceipt(receiptFixture(t, receipt))
	require.NoError(t, err)
	assert.Equal(t, "0", converted.Status)
}

func TestConvertRawReceipt_PreByzantiumRootIndexesSuccess(t *testing.T) {
	t.Parallel()
	// A pre-Byzantium receipt carries a post-state root instead of a status
	// field and is indexed as successful, matching how explorers report
	// pre-Byzantium transactions.
	receipt := baseRawReceipt()
	delete(receipt, "status")
	receipt["root"] = "0x" + strings.Repeat("a", 64)

	converted, err := convertRawReceipt(receiptFixture(t, receipt))
	require.NoError(t, err)
	assert.Equal(t, "1", converted.Status)
}

func TestConvertRawReceipt_NilEffectiveGasPriceStaysEmpty(t *testing.T) {
	t.Parallel()
	receipt := baseRawReceipt()
	delete(receipt, "effectiveGasPrice")

	converted, err := convertRawReceipt(receiptFixture(t, receipt))
	require.NoError(t, err)
	assert.Empty(t, converted.EffectiveGasPrice,
		"a node that omits effectiveGasPrice (pre-London) stores empty")
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
