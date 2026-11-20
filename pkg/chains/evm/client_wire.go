package evm

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// The raw wire layer: everything between the node's JSON-RPC bytes and the
// package's stored representation. Tolerant mirrors of the block and receipt
// responses (eth_getBlockByNumber, eth_getTransactionReceipt,
// eth_getBlockReceipts) decode in one unmarshal pass each, then one
// normalization pass converts hex quantities to the decimal and int
// conventions the stored documents use.
//
// Every value the node computed (hash, from, type, yParity) is read from the
// response and never rederived. Unknown envelope types pass through with no
// error: an indexer needs field passthrough, not envelope semantics, and
// unknown fields are ignored by construction. Tolerance is about unknown
// envelope types, not broken responses: malformed JSON and missing identity
// fields are hard errors.
//
// The conversion is exposed as a package-level helper so the default
// adapter delegates here and any future registered adapter reuses it
// instead of duplicating it.

// rawQuantity decodes a wire QUANTITY tolerantly. hexutil.Uint64 rejects
// zero-padded hex, but real headers carry the nonce as fixed-width
// 0x0000000000000000 and some clients pad other quantities, so the raw
// mirrors parse the digits themselves: hex or decimal, any width. Absent
// (and null) stays a zero value without error.
type rawQuantity uint64

func (v *rawQuantity) UnmarshalJSON(input []byte) error {
	text := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(input)), `"`))
	if text == "" || text == jsonNull {
		return nil
	}
	digits := text
	base := 10
	if strings.HasPrefix(digits, "0x") || strings.HasPrefix(digits, "0X") {
		base = 16
		digits = digits[2:]
	}
	parsed, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return fmt.Errorf("invalid hex quantity %q: %w", text, err)
	}
	*v = rawQuantity(parsed)
	return nil
}

// rawBig decodes a wire quantity of unbounded size tolerantly, the same way
// rawQuantity does for uint64: hex or decimal digits of any width (hexutil.Big
// rejects zero-padded hex, but some clients pad values such as signature r/s),
// with an optional minus sign. Absent (and null) keeps the zero value.
type rawBig struct {
	value *big.Int
}

func (v *rawBig) UnmarshalJSON(input []byte) error {
	text := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(input)), `"`))
	if text == "" || text == jsonNull {
		return nil
	}
	digits := text
	base := 10
	negative := strings.HasPrefix(digits, "-")
	if negative {
		digits = digits[1:]
	}
	if strings.HasPrefix(digits, "0x") || strings.HasPrefix(digits, "0X") {
		base = 16
		digits = digits[2:]
	}
	parsed, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return fmt.Errorf("invalid hex quantity %q", text)
	}
	if negative {
		parsed.Neg(parsed)
	}
	v.value = parsed
	return nil
}

// String renders the value in decimal, the converted number format. A field
// the wire carries as null renders as the big.Int zero.
func (v *rawBig) String() string {
	if v == nil || v.value == nil {
		return "0"
	}
	return v.value.String()
}

// rawBlockObject mirrors the eth_getBlockByNumber response tolerantly.
// Pointer fields distinguish "absent" from zero so a chain that omits a
// field (Bor's totalDifficulty, modern Ethereum's totalDifficulty) stores
// an empty string instead of a fabricated zero.
type rawBlockObject struct {
	Hash             *common.Hash      `json:"hash"`
	Number           *rawBig           `json:"number"`
	Timestamp        *rawQuantity      `json:"timestamp"`
	ParentHash       *common.Hash      `json:"parentHash"`
	Difficulty       *rawBig           `json:"difficulty"`
	TotalDifficulty  *rawBig           `json:"totalDifficulty"`
	GasUsed          *rawQuantity      `json:"gasUsed"`
	GasLimit         *rawQuantity      `json:"gasLimit"`
	BaseFeePerGas    *rawBig           `json:"baseFeePerGas"`
	Nonce            *rawQuantity      `json:"nonce"`
	Miner            *common.Address   `json:"miner"`
	Size             *rawQuantity      `json:"size"`
	StateRoot        *common.Hash      `json:"stateRoot"`
	Sha3Uncles       *common.Hash      `json:"sha3Uncles"`
	TransactionsRoot *common.Hash      `json:"transactionsRoot"`
	ReceiptsRoot     *common.Hash      `json:"receiptsRoot"`
	LogsBloom        *hexutil.Bytes    `json:"logsBloom"`
	ExtraData        *hexutil.Bytes    `json:"extraData"`
	MixHash          *common.Hash      `json:"mixHash"`
	Uncles           []common.Hash     `json:"uncles"`
	Transactions     []json.RawMessage `json:"transactions"`
}

// rawAccessListEntry mirrors one EIP-2930 access-list entry of a raw tx object.
type rawAccessListEntry struct {
	Address     string   `json:"address"`
	StorageKeys []string `json:"storageKeys"`
}

// rawTxObject mirrors one transaction of the raw block response tolerantly.
// Identity fields (hash, from) stay strings so a missing or malformed value
// can be rejected explicitly; yParity stays a string for exact passthrough.
type rawTxObject struct {
	Hash                 string               `json:"hash"`
	Type                 *rawQuantity         `json:"type"`
	From                 string               `json:"from"`
	To                   *common.Address      `json:"to"`
	Value                *rawBig              `json:"value"`
	Gas                  *rawQuantity         `json:"gas"`
	GasPrice             *rawBig              `json:"gasPrice"`
	MaxFeePerGas         *rawBig              `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *rawBig              `json:"maxPriorityFeePerGas"`
	Input                *hexutil.Bytes       `json:"input"`
	Nonce                *rawQuantity         `json:"nonce"`
	ChainID              *rawBig              `json:"chainId"`
	AccessList           []rawAccessListEntry `json:"accessList"`
	V                    *rawBig              `json:"v"`
	R                    *rawBig              `json:"r"`
	S                    *rawBig              `json:"s"`
	YParity              string               `json:"yParity"`
}

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

// ConvertRawBlock converts a raw eth_getBlockByNumber response (full txs)
// into the package's Block type. One unmarshal pass covers the header and
// the transaction array bounds; each transaction object is then unmarshaled
// once and its raw JSON released, so the same bytes are never parsed twice
// and no raw JSON is retained alongside the decoded form.
func ConvertRawBlock(rawBlock json.RawMessage) (*Block, error) {
	var raw rawBlockObject
	if err := json.Unmarshal(rawBlock, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode raw block: %w", err)
	}
	if raw.Number == nil {
		return nil, fmt.Errorf("raw block header has no number")
	}
	if raw.Hash == nil {
		return nil, fmt.Errorf("raw block header has no hash")
	}

	block := &Block{
		Hash:             raw.Hash.Hex(),
		Number:           raw.Number.String(),
		Timestamp:        uint64OrEmpty(raw.Timestamp),
		ParentHash:       hashOrEmpty(raw.ParentHash),
		Difficulty:       bigOrEmpty(raw.Difficulty),
		TotalDifficulty:  bigOrEmpty(raw.TotalDifficulty),
		GasUsed:          uint64OrEmpty(raw.GasUsed),
		GasLimit:         uint64OrEmpty(raw.GasLimit),
		BaseFeePerGas:    bigOrEmpty(raw.BaseFeePerGas),
		Nonce:            uint64OrEmpty(raw.Nonce),
		Miner:            addressOrZero(raw.Miner),
		Size:             uint64OrEmpty(raw.Size),
		StateRoot:        hashOrEmpty(raw.StateRoot),
		Sha3Uncles:       hashOrEmpty(raw.Sha3Uncles),
		TransactionsRoot: hashOrEmpty(raw.TransactionsRoot),
		ReceiptsRoot:     hashOrEmpty(raw.ReceiptsRoot),
		LogsBloom:        bytesOrEmpty(raw.LogsBloom),
		ExtraData:        bytesOrEmpty(raw.ExtraData),
		MixHash:          hashOrEmpty(raw.MixHash),
		Uncles:           hashesOrEmpty(raw.Uncles),
		Transactions:     make([]Transaction, 0, len(raw.Transactions)),
	}

	for i, rawTx := range raw.Transactions {
		tx, err := convertRawTransaction(rawTx, block.Hash, block.Number, i)
		if err != nil {
			return nil, err
		}
		block.Transactions = append(block.Transactions, *tx)
	}

	return block, nil
}

// convertRawTransaction converts one raw tx object. Identity fields are the
// node's own values and are mandatory: hash, from. Unknown envelope types
// pass through carrying whatever the wire provides.
func convertRawTransaction(rawTx json.RawMessage, blockHash, blockNumber string, index int) (*Transaction, error) {
	var raw rawTxObject
	if err := json.Unmarshal(rawTx, &raw); err != nil {
		return nil, fmt.Errorf("raw transaction %d: failed to decode: %w", index, err)
	}

	rawTxHash, err := normalizeBlockHash(raw.Hash)
	if err != nil {
		return nil, fmt.Errorf("raw transaction %d: missing or malformed hash: %w", index, err)
	}
	txHash := rawTxHash.Hex()
	from, err := normalizeAddress(raw.From)
	if err != nil {
		return nil, fmt.Errorf("raw transaction %s (%d): missing or malformed from: %w", txHash, index, err)
	}

	txType := "0"
	if raw.Type != nil {
		txType = fmt.Sprintf("%d", uint64(*raw.Type))
	}

	return &Transaction{
		Hash:                 txHash,
		BlockHash:            blockHash,
		BlockNumber:          blockNumber,
		From:                 from,
		To:                   addressOrEmpty(raw.To),
		Value:                bigOrEmpty(raw.Value),
		Gas:                  uint64OrEmpty(raw.Gas),
		GasPrice:             txGasPrice(&raw),
		MaxFeePerGas:         bigOrEmpty(raw.MaxFeePerGas),
		MaxPriorityFeePerGas: bigOrEmpty(raw.MaxPriorityFeePerGas),
		Input:                inputOrEmptyHex(raw.Input),
		Nonce:                uint64OrEmpty(raw.Nonce),
		TransactionIndex:     index,
		Type:                 txType,
		ChainID:              bigOrEmpty(raw.ChainID),
		AccessList:           accessList(raw.AccessList),
		V:                    bigOrEmpty(raw.V),
		R:                    bigOrEmpty(raw.R),
		S:                    bigOrEmpty(raw.S),
		YParity:              raw.YParity,
		// Signed payloads carry no execution outcome, so status defaults to
		// true; the fetcher's receipt merge overwrites it with the executed
		// result.
		Status: true,
	}, nil
}

// txGasPrice is the one fee rule. Txs that carry a gasPrice field store its
// value; txs of types 0x2/0x3 without one keep today's stored semantic,
// gasPrice = maxFeePerGas; unknown types passthrough whatever fee fields
// the raw tx carries and stay empty where it omits them. The receipt
// merge's effectiveGasPrice is the authoritative fee value for every chain.
func txGasPrice(raw *rawTxObject) string {
	if raw.GasPrice != nil {
		return raw.GasPrice.String()
	}
	if raw.Type != nil && (*raw.Type == ethtypesDynamicFeeTxType || *raw.Type == ethtypesBlobTxType) && raw.MaxFeePerGas != nil {
		return raw.MaxFeePerGas.String()
	}
	return ""
}

// The two standard tx types the fee rule covers. Mirrored here as plain
// constants so the passthrough conversion needs no go-ethereum tx types.
const (
	ethtypesDynamicFeeTxType = 2
	ethtypesBlobTxType       = 3

	// jsonNull is the JSON null literal a node sends for a missing block or
	// receipt; the raw fetchers compare the raw result bytes against it.
	jsonNull = "null"
)

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

// normalizeAddress checksums a wire address via common.HexToAddress and
// rejects values the round trip cannot reproduce, so a padded or malformed
// address cannot slip through as identity.
func normalizeAddress(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	a := common.HexToAddress(trimmed)
	if !strings.EqualFold(a.Hex(), trimmed) {
		return "", fmt.Errorf("not a 0x-prefixed %d-byte hex address", common.AddressLength)
	}
	return a.Hex(), nil
}

// hashOrEmpty renders an optional wire hash; absent stays empty.
func hashOrEmpty(h *common.Hash) string {
	if h == nil {
		return ""
	}
	return h.Hex()
}

// hashesOrEmpty renders optional uncle hashes as a non-nil slice, matching
// the typed path's always-present uncles array.
func hashesOrEmpty(hashes []common.Hash) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = h.Hex()
	}
	return out
}

// addressOrZero renders an optional wire address; absent keeps the typed
// path's zero-address value for an empty coinbase.
func addressOrZero(a *common.Address) string {
	if a == nil {
		return common.Address{}.Hex()
	}
	return a.Hex()
}

// addressOrEmpty renders an optional wire address; absent stays empty
// (contract creation).
func addressOrEmpty(a *common.Address) string {
	if a == nil {
		return ""
	}
	return a.Hex()
}

// uint64OrEmpty renders an optional hex quantity as a decimal string.
func uint64OrEmpty(v *rawQuantity) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d", uint64(*v))
}

// bigOrEmpty renders an optional hex quantity as a big decimal string, the
// format the typed path emitted via big.Int.String(). A field the wire
// carries as null renders empty, the same as an absent one.
func bigOrEmpty(v *rawBig) string {
	if v == nil || v.value == nil {
		return ""
	}
	return v.value.String()
}

// bytesOrEmpty renders optional bytes in today's bare-hex convention (no
// 0x prefix), the format the typed path emitted for logsBloom and extraData.
func bytesOrEmpty(b *hexutil.Bytes) string {
	if b == nil {
		return ""
	}
	return common.Bytes2Hex(*b)
}

// inputOrEmptyHex renders optional calldata in the typed path's 0x-prefixed
// lowercase convention; absent calldata stores "0x".
func inputOrEmptyHex(b *hexutil.Bytes) string {
	if b == nil {
		return "0x"
	}
	return "0x" + common.Bytes2Hex(*b)
}

// accessList converts the raw tx's access list entries; absent stays nil so
// the omitempty output matches the typed path for txs without one.
func accessList(raw []rawAccessListEntry) []AccessListEntry {
	if len(raw) == 0 {
		return nil
	}
	out := make([]AccessListEntry, 0, len(raw))
	for _, entry := range raw {
		address, err := normalizeAddress(entry.Address)
		if err != nil {
			// An access-list address is not identity: a malformed one keeps
			// the wire value rather than failing the block. Real nodes serve
			// well-formed entries.
			address = entry.Address
		}
		keys := make([]string, len(entry.StorageKeys))
		for i, key := range entry.StorageKeys {
			if h, err := normalizeBlockHash(key); err == nil {
				keys[i] = h.Hex()
			} else {
				keys[i] = key
			}
		}
		out = append(out, AccessListEntry{Address: address, StorageKeys: keys})
	}
	return out
}
