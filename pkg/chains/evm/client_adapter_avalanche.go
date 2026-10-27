package evm

import (
	"encoding/json"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// avalancheClientAdapter covers the JSON-RPC differences of the Avalanche
// C-Chain (coreth):
//
//   - Coreth headers carry fields go-ethereum's header does not hash
//     (extDataHash, extDataGasUsed, blockGasCost and the ACP-176 fee
//     fields). The typed path would silently store a recomputed, wrong
//     block hash, so the block is read on the raw path where the
//     RPC-provided hash is kept.
//   - Transactions are standard envelopes, and coreth reports yParity on
//     typed txs, so it is read from the raw tx.
//   - Atomic (X/P-Chain import/export) txs live in blockExtraData, not in
//     transactions; they are not indexed.
//
// Sender recovery keeps the default behaviour: every C-Chain tx is signed.
type avalancheClientAdapter struct {
	rawStandardClientAdapter
}

func init() {
	chainAdapters["avalanche"] = avalancheClientAdapter{}
}

func (avalancheClientAdapter) YParity(_ *ethtypes.Transaction, rawTx json.RawMessage) string {
	return rawYParity(rawTx)
}

// rawYParity returns the raw tx's yParity exactly as the RPC reported it,
// "" when the field is absent (legacy txs) or the tx cannot be read.
func rawYParity(rawTx json.RawMessage) string {
	if len(rawTx) == 0 {
		return ""
	}
	var fields struct {
		YParity string `json:"yParity"`
	}
	if err := json.Unmarshal(rawTx, &fields); err != nil {
		return ""
	}
	return fields.YParity
}
