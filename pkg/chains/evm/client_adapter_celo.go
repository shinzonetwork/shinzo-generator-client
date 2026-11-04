package evm

import (
	"encoding/json"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// celoDepositTxType is the OP-stack deposit envelope; the L1 info deposit
// sits at index 0 of every block.
const celoDepositTxType = 0x7e

// celoClientAdapter covers the JSON-RPC differences of Celo mainnet (an
// OP-stack L2 since block 31,056,500):
//
//   - Fee-currency txs (0x7b, 0x7a) are mapped onto DynamicFeeTx and deposits
//     (0x7e) onto a zero-gas-price LegacyTx; unknown types map onto the
//     closest standard envelope so a new type never fails the block. The
//     client keeps the raw hash and type, so remapped txs still join their
//     receipts.
//   - The sender is the RPC's from for every tx: deposits are unsigned, and
//     legacy txs may be signed over the Celo payload (feeCurrency,
//     gatewayFee), which go-ethereum's signer recovers wrongly.
//   - yParity is read from the raw tx.
//
// Not indexed: feeCurrency, sourceHash, mint, isSystemTx. For 0x7b/0x7a txs
// the gas prices are denominated in the fee currency, not CELO.
type celoClientAdapter struct {
	rawStandardClientAdapter
}

func init() {
	chainAdapters["celo"] = celoClientAdapter{}
}

// DecodeTransactions decodes standard envelopes with go-ethereum's RPC JSON
// decoder and maps deposits and every other type onto the closest standard
// envelope; one transaction is returned per raw tx, in order.
func (celoClientAdapter) DecodeTransactions(rawTxs []json.RawMessage) ([]*ethtypes.Transaction, error) {
	return decodeRemapping(rawTxs, celoDepositTxType)
}

// ResolveFrom returns the RPC-reported sender for every tx that carries one.
func (celoClientAdapter) ResolveFrom(_ *ethtypes.Transaction, rawTx json.RawMessage) (string, bool) {
	return rawFrom(rawTx)
}

func (celoClientAdapter) YParity(_ *ethtypes.Transaction, rawTx json.RawMessage) string {
	return rawYParity(rawTx)
}
