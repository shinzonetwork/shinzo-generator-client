package evm

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// rawStandardClientAdapter is the base for chains whose block hash go-ethereum
// cannot recompute from the header (e.g. the CometBFT header hash on
// cosmos-evm chains) but whose transactions are all standard envelopes
// (legacy, 0x1, 0x2, 0x4):
//
//   - The block is read on the raw path, where the RPC-provided hash is kept
//     (block and tx blockHash alike).
//   - Each raw tx decodes with go-ethereum's RPC JSON decoder, so tx hashes
//     and senders are recomputed exactly as on the typed path.
//
// Sender recovery and yParity keep the default behaviour. Chain adapters
// embed it and add only what is unique to their chain.
type rawStandardClientAdapter struct {
	defaultClientAdapter
}

func (rawStandardClientAdapter) NeedsRawBlock() bool {
	return true
}

// DecodeTransactions decodes each raw tx object with go-ethereum's RPC JSON
// decoder; one transaction is returned per raw tx, in order.
func (rawStandardClientAdapter) DecodeTransactions(rawTxs []json.RawMessage) ([]*ethtypes.Transaction, error) {
	txs := make([]*ethtypes.Transaction, len(rawTxs))
	for i, rawTx := range rawTxs {
		tx := new(ethtypes.Transaction)
		if err := tx.UnmarshalJSON(rawTx); err != nil {
			return nil, fmt.Errorf("decode transaction %d: %w", i, err)
		}
		txs[i] = tx
	}
	return txs, nil
}

// rawFrom returns the raw tx's sender, checksummed, exactly as the RPC
// reported it; ok=false when the field is absent or the tx cannot be read.
// Chains whose signatures go-ethereum cannot verify (Celo, Kaia) use it as
// their ResolveFrom.
func rawFrom(rawTx json.RawMessage) (string, bool) {
	var fields struct {
		From *common.Address `json:"from"`
	}
	if err := json.Unmarshal(rawTx, &fields); err != nil || fields.From == nil {
		return "", false
	}
	return fields.From.Hex(), true
}
