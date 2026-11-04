package evm

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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

// rawEnvelope holds the raw tx fields needed to rebuild a standard envelope.
type rawEnvelope struct {
	Type                 hexutil.Uint64      `json:"type"`
	ChainID              *hexutil.Big        `json:"chainId"`
	Nonce                hexutil.Uint64      `json:"nonce"`
	Gas                  hexutil.Uint64      `json:"gas"`
	GasPrice             *hexutil.Big        `json:"gasPrice"`
	MaxFeePerGas         *hexutil.Big        `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *hexutil.Big        `json:"maxPriorityFeePerGas"`
	To                   *common.Address     `json:"to"`
	Value                *hexutil.Big        `json:"value"`
	Input                hexutil.Bytes       `json:"input"`
	AccessList           ethtypes.AccessList `json:"accessList"`
	V                    *hexutil.Big        `json:"v"`
	R                    *hexutil.Big        `json:"r"`
	S                    *hexutil.Big        `json:"s"`
}

// decodeRemapping decodes standard envelopes (legacy, 0x1, 0x2, 0x4) with
// go-ethereum's RPC JSON decoder, maps the chain's unsigned system tx type
// (Celo deposits, Polygon state syncs) onto a zero-gas-price LegacyTx, and
// maps every other type onto the closest standard envelope, so a new type
// never fails the block. One transaction is returned per raw tx, in order.
func decodeRemapping(rawTxs []json.RawMessage, systemTxType hexutil.Uint64) ([]*ethtypes.Transaction, error) {
	txs := make([]*ethtypes.Transaction, len(rawTxs))
	for i, rawTx := range rawTxs {
		var fields rawEnvelope
		if err := json.Unmarshal(rawTx, &fields); err != nil {
			return nil, fmt.Errorf("decode transaction %d: %w", i, err)
		}
		switch fields.Type {
		case ethtypes.LegacyTxType, ethtypes.AccessListTxType, ethtypes.DynamicFeeTxType, ethtypes.SetCodeTxType:
			tx := new(ethtypes.Transaction)
			if err := tx.UnmarshalJSON(rawTx); err != nil {
				return nil, fmt.Errorf("decode transaction %d: %w", i, err)
			}
			txs[i] = tx
		case systemTxType:
			txs[i] = ethtypes.NewTx(&ethtypes.LegacyTx{
				Nonce:    uint64(fields.Nonce),
				GasPrice: new(big.Int),
				Gas:      uint64(fields.Gas),
				To:       fields.To,
				Value:    bigOrZero(fields.Value),
				Data:     fields.Input,
			})
		default:
			// Chain fee-currency types (Celo 0x7b, 0x7a) and unknown types: a
			// fee-cap tx when the RPC reports one, a gas-price tx otherwise.
			txs[i] = fields.closestEnvelope()
		}
	}
	return txs, nil
}

// closestEnvelope maps a non-standard tx onto DynamicFeeTx when it carries
// EIP-1559 fees, else onto LegacyTx.
func (f rawEnvelope) closestEnvelope() *ethtypes.Transaction {
	if f.MaxFeePerGas != nil {
		return ethtypes.NewTx(&ethtypes.DynamicFeeTx{
			ChainID:    bigOrZero(f.ChainID),
			Nonce:      uint64(f.Nonce),
			GasTipCap:  bigOrZero(f.MaxPriorityFeePerGas),
			GasFeeCap:  bigOrZero(f.MaxFeePerGas),
			Gas:        uint64(f.Gas),
			To:         f.To,
			Value:      bigOrZero(f.Value),
			Data:       f.Input,
			AccessList: f.AccessList,
			V:          bigOrZero(f.V),
			R:          bigOrZero(f.R),
			S:          bigOrZero(f.S),
		})
	}
	return ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce:    uint64(f.Nonce),
		GasPrice: bigOrZero(f.GasPrice),
		Gas:      uint64(f.Gas),
		To:       f.To,
		Value:    bigOrZero(f.Value),
		Data:     f.Input,
		V:        bigOrZero(f.V),
		R:        bigOrZero(f.R),
		S:        bigOrZero(f.S),
	})
}

func bigOrZero(b *hexutil.Big) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return b.ToInt()
}
