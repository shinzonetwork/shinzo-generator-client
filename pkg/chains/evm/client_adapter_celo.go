package evm

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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

// celoRawTx holds the raw tx fields needed to rebuild a standard envelope.
type celoRawTx struct {
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

// DecodeTransactions decodes standard envelopes with go-ethereum's RPC JSON
// decoder and maps every other type onto the closest standard envelope;
// one transaction is returned per raw tx, in order.
func (celoClientAdapter) DecodeTransactions(rawTxs []json.RawMessage) ([]*ethtypes.Transaction, error) {
	txs := make([]*ethtypes.Transaction, len(rawTxs))
	for i, rawTx := range rawTxs {
		var fields celoRawTx
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
		case celoDepositTxType:
			txs[i] = ethtypes.NewTx(&ethtypes.LegacyTx{
				Nonce:    uint64(fields.Nonce),
				GasPrice: new(big.Int),
				Gas:      uint64(fields.Gas),
				To:       fields.To,
				Value:    bigOrZero(fields.Value),
				Data:     fields.Input,
			})
		default:
			// CIP-64 (0x7b), CIP-66 (0x7a) and unknown types: a fee-cap tx
			// when the RPC reports one, a gas-price tx otherwise.
			txs[i] = fields.closestEnvelope()
		}
	}
	return txs, nil
}

// closestEnvelope maps a non-standard tx onto DynamicFeeTx when it carries
// EIP-1559 fees, else onto LegacyTx.
func (f celoRawTx) closestEnvelope() *ethtypes.Transaction {
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

// ResolveFrom returns the RPC-reported sender for every tx that carries one.
func (celoClientAdapter) ResolveFrom(_ *ethtypes.Transaction, rawTx json.RawMessage) (string, bool) {
	return rawFrom(rawTx)
}

func (celoClientAdapter) YParity(_ *ethtypes.Transaction, rawTx json.RawMessage) string {
	return rawYParity(rawTx)
}

func bigOrZero(b *hexutil.Big) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return b.ToInt()
}
