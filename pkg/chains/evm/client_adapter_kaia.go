package evm

import (
	"encoding/json"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// kaiaClientAdapter covers the JSON-RPC differences of Kaia mainnet (formerly
// Klaytn Cypress):
//
//   - The block is read on the raw path: Kaia's header hash covers fields the
//     eth_ namespace does not expose, so go-ethereum recomputes a wrong hash
//     without error.
//   - The eth_ namespace reports Kaia-native txs (value transfers, fee-delegated
//     executions, ...) as 0x0 with standard legacy fields, so they decode with
//     the standard decoder. Their recomputed hash is wrong, which the client's
//     raw hash and type cover.
//   - The sender is the RPC's from for every tx: native txs are signed over
//     Kaia's RLP payload, which go-ethereum's signer recovers wrongly.
//
// yParity keeps the default: the RPC does not report it. Not indexed: the
// native tx type, feePayer and feeRatio (kaia_ namespace only); for
// fee-delegated txs the gas was paid by the fee payer, not from.
type kaiaClientAdapter struct {
	rawStandardClientAdapter
}

func init() {
	chainAdapters["kaia"] = kaiaClientAdapter{}
}

// ResolveFrom returns the RPC-reported sender for every tx that carries one.
func (kaiaClientAdapter) ResolveFrom(_ *ethtypes.Transaction, rawTx json.RawMessage) (string, bool) {
	return rawFrom(rawTx)
}
