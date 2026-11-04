package evm

import (
	"encoding/json"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// polygonStateSyncTxType is Bor's state-sync envelope (PIP-74): an unsigned
// tx from the zero address to the state receiver (0x…1001), appended to
// sprint-boundary blocks that carry L1 bridge events.
const polygonStateSyncTxType = 0x7f

// polygonClientAdapter covers the JSON-RPC differences of Polygon PoS (Bor),
// mainnet and Amoy alike:
//
//   - The block is read on the raw path: go-ethereum's typed decode rejects
//     the whole block on a state-sync tx (0x7f). The header hash itself
//     recomputes correctly.
//   - State-sync txs are mapped onto a zero-gas-price LegacyTx and unknown
//     types onto the closest standard envelope, so a new type never fails
//     the block. The client keeps the raw hash and type, so remapped txs
//     still join their receipts.
//   - The sender is the RPC's from for every tx: state-sync txs (and the
//     pre-PIP-74 synthetic state-sync tx) are unsigned.
//   - yParity is read from the raw tx.
//
// Not indexed: the state-sync payload (Heimdall event id, L1 contract and
// tx hash) and the block author, which Bor keeps in extraData's seal
// (miner is reported as the zero address).
type polygonClientAdapter struct {
	rawStandardClientAdapter
}

func init() {
	chainAdapters["polygon"] = polygonClientAdapter{}
}

// DecodeTransactions decodes standard envelopes with go-ethereum's RPC JSON
// decoder and maps state-sync and every other type onto the closest
// standard envelope; one transaction is returned per raw tx, in order.
func (polygonClientAdapter) DecodeTransactions(rawTxs []json.RawMessage) ([]*ethtypes.Transaction, error) {
	return decodeRemapping(rawTxs, polygonStateSyncTxType)
}

// ResolveFrom returns the RPC-reported sender for every tx that carries one.
func (polygonClientAdapter) ResolveFrom(_ *ethtypes.Transaction, rawTx json.RawMessage) (string, bool) {
	return rawFrom(rawTx)
}

func (polygonClientAdapter) YParity(_ *ethtypes.Transaction, rawTx json.RawMessage) string {
	return rawYParity(rawTx)
}
