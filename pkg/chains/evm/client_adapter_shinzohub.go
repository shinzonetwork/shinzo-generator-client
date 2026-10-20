package evm

import (
	"encoding/json"
	"fmt"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// shinzohubClientAdapter covers the cosmos-evm JSON-RPC differences of the
// ShinzoHub chain:
//
//   - The block hash is the CometBFT header hash, which go-ethereum cannot
//     recompute from the EVM header, so the block is read on the raw path
//     where the RPC-provided hash is kept (block and tx blockHash alike).
//   - Logs emitted by the hub's precompiles carry blockNumber 0x0 (they are
//     built by hand in stateDB.AddLog), so every log is restamped from its
//     receipt, whose block number and hash are correct.
//
// Sender recovery and yParity keep the default behaviour.
type shinzohubClientAdapter struct {
	defaultClientAdapter
}

func init() {
	chainAdapters["shinzohub"] = shinzohubClientAdapter{}
}

func (shinzohubClientAdapter) NeedsRawBlock() bool {
	return true
}

// DecodeTransactions decodes each raw tx object with go-ethereum's RPC JSON
// decoder. ShinzoHub only produces standard envelopes (legacy, 0x1, 0x2), so
// no envelope mapping is needed; one transaction is returned per raw tx, in
// order.
func (shinzohubClientAdapter) DecodeTransactions(rawTxs []json.RawMessage) ([]*ethtypes.Transaction, error) {
	txs := make([]*ethtypes.Transaction, len(rawTxs))
	for i, rawTx := range rawTxs {
		tx := new(ethtypes.Transaction)
		if err := tx.UnmarshalJSON(rawTx); err != nil {
			return nil, fmt.Errorf("decode shinzohub transaction %d: %w", i, err)
		}
		txs[i] = tx
	}
	return txs, nil
}

// StampReceiptLogs sets each log's block number and hash from its receipt.
func (shinzohubClientAdapter) StampReceiptLogs(receipt *TransactionReceipt) {
	for i := range receipt.Logs {
		receipt.Logs[i].BlockNumber = receipt.BlockNumber
		receipt.Logs[i].BlockHash = receipt.BlockHash
	}
}
