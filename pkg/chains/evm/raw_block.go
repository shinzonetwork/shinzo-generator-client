package evm

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
)

// rawTx mirrors the eth_getBlockByNumber transaction JSON. Quantities are
// hex strings on the wire; decode() normalizes them to the decimal strings
// convertTransaction produces.
type rawTx struct {
	Hash                 string            `json:"hash"`
	BlockHash            string            `json:"blockHash"`
	BlockNumber          string            `json:"blockNumber"`
	From                 string            `json:"from"`
	To                   *string           `json:"to"`
	Value                string            `json:"value"`
	Gas                  string            `json:"gas"`
	GasPrice             string            `json:"gasPrice"`
	MaxFeePerGas         string            `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string            `json:"maxPriorityFeePerGas"`
	Input                string            `json:"input"`
	Nonce                string            `json:"nonce"`
	TransactionIndex     string            `json:"transactionIndex"`
	Type                 string            `json:"type"`
	ChainID              string            `json:"chainId"`
	AccessList           []AccessListEntry `json:"accessList"`
	V                    string            `json:"v"`
	R                    string            `json:"r"`
	S                    string            `json:"s"`
}

// rawBlock mirrors the eth_getBlockByNumber block JSON.
type rawBlock struct {
	Hash             string   `json:"hash"`
	Number           string   `json:"number"`
	Timestamp        string   `json:"timestamp"`
	ParentHash       string   `json:"parentHash"`
	Difficulty       string   `json:"difficulty"`
	GasUsed          string   `json:"gasUsed"`
	GasLimit         string   `json:"gasLimit"`
	BaseFeePerGas    string   `json:"baseFeePerGas"`
	Nonce            string   `json:"nonce"`
	Miner            string   `json:"miner"`
	Size             string   `json:"size"`
	StateRoot        string   `json:"stateRoot"`
	Sha3Uncles       string   `json:"sha3Uncles"`
	TransactionsRoot string   `json:"transactionsRoot"`
	ReceiptsRoot     string   `json:"receiptsRoot"`
	LogsBloom        string   `json:"logsBloom"`
	ExtraData        string   `json:"extraData"`
	MixHash          string   `json:"mixHash"`
	Uncles           []string `json:"uncles"`
	Transactions     []rawTx  `json:"transactions"`
}

// getBlockByNumberRaw re-fetches the block via a raw eth_getBlockByNumber
// call on the client's existing (authenticated) transport and decodes it
// tolerantly. It is the fallback for blocks containing transaction types
// go-ethereum rejects, e.g. Bor state-sync deposits (type 0x7f).
func (c *EthereumClient) getBlockByNumberRaw(ctx context.Context, client *ethclient.Client, blockNumber *big.Int) (*Block, error) {
	var raw json.RawMessage
	err := client.Client().CallContext(ctx, &raw, "eth_getBlockByNumber", hexutil.EncodeBig(blockNumber), true)
	if err != nil {
		return nil, fmt.Errorf("raw eth_getBlockByNumber %v: %w", blockNumber, err)
	}
	var rb rawBlock
	if err := json.Unmarshal(raw, &rb); err != nil {
		return nil, fmt.Errorf("decode raw block %v: %w", blockNumber, err)
	}
	return rb.decode(), nil
}

// decode maps the raw JSON block to our Block type following the conventions
// of convertGethBlock/convertTransaction: quantities as decimal strings,
// logsBloom/extraData without the 0x prefix, EIP-1559 fee fields only for
// dynamic-fee txs, status hardcoded true, totalDifficulty "".
func (rb *rawBlock) decode() *Block {
	txs := make([]Transaction, 0, len(rb.Transactions))
	for i := range rb.Transactions {
		txs = append(txs, rb.Transactions[i].decode())
	}
	return &Block{
		Hash:             rb.Hash,
		Number:           hexToDec(rb.Number),
		Timestamp:        hexToDec(rb.Timestamp),
		ParentHash:       rb.ParentHash,
		Difficulty:       hexToDec(rb.Difficulty),
		TotalDifficulty:  "",
		GasUsed:          hexToDec(rb.GasUsed),
		GasLimit:         hexToDec(rb.GasLimit),
		BaseFeePerGas:    hexToDec(rb.BaseFeePerGas),
		Nonce:            hexToDec(rb.Nonce),
		Miner:            rb.Miner,
		Size:             hexToDec(rb.Size),
		StateRoot:        rb.StateRoot,
		Sha3Uncles:       rb.Sha3Uncles,
		TransactionsRoot: rb.TransactionsRoot,
		ReceiptsRoot:     rb.ReceiptsRoot,
		LogsBloom:        strings.TrimPrefix(rb.LogsBloom, "0x"),
		ExtraData:        strings.TrimPrefix(rb.ExtraData, "0x"),
		MixHash:          rb.MixHash,
		Uncles:           rb.Uncles,
		Transactions:     txs,
	}
}

func (rt *rawTx) decode() Transaction {
	from := rt.From
	if from == "" {
		from = ZeroAddress // unsigned transaction (e.g. Bor state-sync deposit)
	}
	to := ""
	if rt.To != nil {
		to = *rt.To
	}
	txIndex, _ := hexToInt(rt.TransactionIndex)
	txType := hexToDec(rt.Type)

	// Mirror getMaxFeePerGas/getMaxPriorityFeePerGas: EIP-1559 fee fields are
	// only reported for dynamic-fee (type 2) transactions.
	maxFee, maxPriorityFee := "", ""
	if txType == "2" {
		maxFee = hexToDec(rt.MaxFeePerGas)
		maxPriorityFee = hexToDec(rt.MaxPriorityFeePerGas)
	}

	accessList := rt.AccessList
	if accessList == nil {
		accessList = []AccessListEntry{}
	}

	return Transaction{
		Hash:                 rt.Hash,
		BlockHash:            rt.BlockHash,
		BlockNumber:          hexToDec(rt.BlockNumber),
		From:                 from,
		To:                   to,
		Value:                hexToDec(rt.Value),
		Gas:                  hexToDec(rt.Gas),
		GasPrice:             hexToDec(rt.GasPrice),
		MaxFeePerGas:         maxFee,
		MaxPriorityFeePerGas: maxPriorityFee,
		Input:                rt.Input,
		Nonce:                hexToDec(rt.Nonce),
		TransactionIndex:     txIndex,
		Type:                 txType,
		ChainID:              hexToDec(rt.ChainID),
		AccessList:           accessList,
		V:                    hexToDec(rt.V),
		R:                    hexToDec(rt.R),
		S:                    hexToDec(rt.S),
		Status:               true,
	}
}

// hexToDec converts a 0x-prefixed hex quantity to its decimal string.
// Non-hex input is returned unchanged.
func hexToDec(s string) string {
	if !strings.HasPrefix(s, "0x") {
		return s
	}
	n, ok := new(big.Int).SetString(s[2:], 16) //nolint:mnd // hex base
	if !ok {
		return s
	}
	return n.String()
}

// hexToInt converts a 0x-prefixed hex quantity to an int.
func hexToInt(s string) (int, error) {
	n, err := hexutil.DecodeUint64(s)
	return int(n), err //nolint:gosec // transaction indices fit in int
}
