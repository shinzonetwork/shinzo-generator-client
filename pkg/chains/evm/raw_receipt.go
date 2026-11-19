package evm

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// The raw receipt path: eth_getTransactionReceipt and eth_getBlockReceipts
// responses unmarshal directly into the package's receipt representation
// through package-owned mirrors, then one normalization pass converts hex
// quantities to the decimal and int conventions today's stored output uses.
// The go-ethereum receipt decode leaves the block path entirely with this
// file; the fetcher's merge and batch-acceptance machinery above it is
// untouched.

// rawReceiptObject mirrors an eth_getTransactionReceipt / eth_getBlockReceipts
// response object tolerantly. Pointer fields distinguish "absent" from zero,
// and the pre-Byzantium post-state root is carried so a root receipt still
// indexes as successful (getReceiptStatus's semantic).
type rawReceiptObject struct {
	TransactionHash   *common.Hash      `json:"transactionHash"`
	TransactionIndex  *rawQuantity      `json:"transactionIndex"`
	BlockHash         *common.Hash      `json:"blockHash"`
	BlockNumber       *rawQuantity      `json:"blockNumber"`
	CumulativeGasUsed *rawQuantity      `json:"cumulativeGasUsed"`
	GasUsed           *rawQuantity      `json:"gasUsed"`
	EffectiveGasPrice *rawBig           `json:"effectiveGasPrice"`
	ContractAddress   *common.Address   `json:"contractAddress"`
	Status            *rawQuantity      `json:"status"`
	Root              string            `json:"root"`
	Logs              []json.RawMessage `json:"logs"`
}

// rawLogObject mirrors one log of a raw receipt response tolerantly.
type rawLogObject struct {
	Address          *common.Address `json:"address"`
	Topics           []common.Hash   `json:"topics"`
	Data             *hexutil.Bytes  `json:"data"`
	BlockNumber      *rawQuantity    `json:"blockNumber"`
	TransactionHash  *common.Hash    `json:"transactionHash"`
	TransactionIndex *rawQuantity    `json:"transactionIndex"`
	BlockHash        *common.Hash    `json:"blockHash"`
	LogIndex         *rawQuantity    `json:"logIndex"`
	Removed          bool            `json:"removed"`
}

// convertRawReceipt unmarshals one raw receipt response object and applies
// the normalization pass, producing today's exact stored formats.
func convertRawReceipt(raw json.RawMessage) (*TransactionReceipt, error) {
	var rawReceipt rawReceiptObject
	if err := json.Unmarshal(raw, &rawReceipt); err != nil {
		return nil, fmt.Errorf("failed to decode raw receipt: %w", err)
	}

	logs := make([]Log, 0, len(rawReceipt.Logs))
	for i, rawLog := range rawReceipt.Logs {
		log, err := convertRawLog(rawLog)
		if err != nil {
			return nil, fmt.Errorf("failed to decode receipt log %d: %w", i, err)
		}
		logs = append(logs, *log)
	}

	return &TransactionReceipt{
		TransactionHash:   hashOrEmpty(rawReceipt.TransactionHash),
		TransactionIndex:  uint64OrEmpty(rawReceipt.TransactionIndex),
		BlockHash:         hashOrEmpty(rawReceipt.BlockHash),
		BlockNumber:       uint64OrEmpty(rawReceipt.BlockNumber),
		CumulativeGasUsed: uint64OrEmpty(rawReceipt.CumulativeGasUsed),
		GasUsed:           uint64OrEmpty(rawReceipt.GasUsed),
		EffectiveGasPrice: bigOrEmpty(rawReceipt.EffectiveGasPrice),
		ContractAddress:   receiptContractAddress(rawReceipt.ContractAddress),
		Logs:              logs,
		Status:            receiptStatus(rawReceipt.Status, rawReceipt.Root),
	}, nil
}

// convertRawLog converts one raw log object into today's stored conventions:
// checksummed address, lowercase 0x topics and hashes, bare-hex data (no 0x
// prefix), decimal block number, and int indices.
func convertRawLog(rawLog json.RawMessage) (*Log, error) {
	var raw rawLogObject
	if err := json.Unmarshal(rawLog, &raw); err != nil {
		return nil, err
	}

	topics := make([]string, len(raw.Topics))
	for i, topic := range raw.Topics {
		topics[i] = topic.Hex()
	}

	log := &Log{
		Topics:  topics,
		Data:    bytesOrEmpty(raw.Data),
		Removed: raw.Removed,
	}
	if raw.Address != nil {
		log.Address = raw.Address.Hex()
	}
	if raw.BlockNumber != nil {
		log.BlockNumber = uint64OrEmpty(raw.BlockNumber)
	}
	if raw.TransactionHash != nil {
		log.TransactionHash = raw.TransactionHash.Hex()
	}
	if raw.TransactionIndex != nil {
		log.TransactionIndex = int(uint64(*raw.TransactionIndex)) //nolint:gosec // indices are bounded by block gas limits
	}
	if raw.BlockHash != nil {
		log.BlockHash = raw.BlockHash.Hex()
	}
	if raw.LogIndex != nil {
		log.LogIndex = int(uint64(*raw.LogIndex)) //nolint:gosec // indices are bounded by block gas limits
	}
	return log, nil
}

// receiptStatusSuccess is the wire's success status value: the quantity
// "0x1" decodes to it, and the converted format renders it as "1".
const receiptStatusSuccess = 1

// receiptStatus maps the receipt's execution outcome to the converted
// decimal "1"/"0" format. A pre-Byzantium receipt carries a post-state root
// instead of a status field and is indexed as successful, matching how
// explorers report pre-Byzantium transactions; the check comes first, the
// way getReceiptStatus ordered it.
func receiptStatus(status *rawQuantity, root string) string {
	if root != "" {
		return "1"
	}
	if status != nil && *status == receiptStatusSuccess {
		return "1"
	}
	return "0"
}

// receiptContractAddress renders the receipt's contract address in the
// converted convention: absent or zero stays empty, else checksummed hex.
func receiptContractAddress(address *common.Address) string {
	if address == nil || *address == (common.Address{}) {
		return ""
	}
	return address.Hex()
}
