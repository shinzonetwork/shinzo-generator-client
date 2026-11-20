package bitcoin

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Bitcoin Core's getblock verbosity 3 payload, and the verbose results of
// getblockheader and getblockchaininfo, are decoded into the types in this
// file. btcjson has no result type modeling Core's full verbosity-3 object:
// its block and transaction result types stop at verbosity 2 and carry
// neither the per-transaction fee nor the per-input prevout undo data, so
// the decode types are owned by this package and btcjson types never leave
// it. The client maps the two non-verbosity-3 calls into the
// VerboseBlockHeader and ChainInfo targets below.
//
// Sums stay as the raw RPC floats here; the converter owns the satoshi
// conversion and writes documents in int64 satoshis only.

// Block is one getblock verbosity 3 result: the header fields the block
// documents persist, the archive-floor extras (confirmations, strippedsize,
// size, weight), and every transaction decoded in wire order.
type Block struct {
	Hash              string  `json:"hash"`
	Confirmations     int64   `json:"confirmations"`
	StrippedSize      int64   `json:"strippedsize"`
	Size              int64   `json:"size"`
	Weight            int64   `json:"weight"`
	Height            int64   `json:"height"`
	Version           uint32  `json:"version"`
	MerkleRoot        string  `json:"merkleroot"`
	Tx                []Tx    `json:"tx"`
	Time              int64   `json:"time"`
	Nonce             uint32  `json:"nonce"`
	Bits              string  `json:"bits"`
	Difficulty        float64 `json:"difficulty"`
	Chainwork         string  `json:"chainwork"`
	PreviousBlockHash string  `json:"previousblockhash,omitempty"`
}

// Tx is one parsed transaction. TxID stays the canonical identity (the
// double-SHA256 of the stripped serialization) and Hash is SegWit's wtxid,
// which differs from the txid for every transaction carrying witness data.
// Fee is nil for the coinbase transaction and whenever the undo data needed
// to compute it is unavailable.
type Tx struct {
	TxID     string   `json:"txid"`
	Hash     string   `json:"hash"`
	Version  uint32   `json:"version"`
	LockTime uint32   `json:"locktime"`
	Vin      []Vin    `json:"vin"`
	Vout     []Vout   `json:"vout"`
	Fee      *float64 `json:"fee,omitempty"`
}

// Vin is one transaction input. The coinbase input carries only
// Coinbase, Sequence and (post-SegWit) TxInWitness, while regular inputs
// carry TxID, Vout, ScriptSig and the denormalized Prevout when the node's
// undo data covers the spent output. Vout is a pointer because Core omits
// the field entirely for the coinbase input and vout zero on regular
// inputs must still re-marshal on the wire.
type Vin struct {
	TxID        string     `json:"txid,omitempty"`
	Vout        *uint32    `json:"vout,omitempty"`
	ScriptSig   *ScriptSig `json:"scriptSig,omitempty"`
	Sequence    uint32     `json:"sequence"`
	Coinbase    string     `json:"coinbase,omitempty"`
	TxInWitness []string   `json:"txinwitness,omitempty"`
	Prevout     *PrevOut   `json:"prevout,omitempty"`
}

// PrevOut is the denormalized spent output that verbosity 3 attaches to a
// regular input. Value is the spent amount in BTC as the raw RPC float; the
// converter turns it into int64 satoshis. Height is the height the prevout
// was minted at and Generated marks coinbase sources.
type PrevOut struct {
	Generated    bool         `json:"generated"`
	Height       int64        `json:"height"`
	Value        float64      `json:"value"`
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// Vout is one transaction output. Index mirrors Core's `n` field: the
// output's position within its transaction, the ordinal the outpoint pair
// (txid, n) keys resolution by.
type Vout struct {
	Index        uint32       `json:"n"`
	Value        float64      `json:"value"`
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// ScriptPubKey carries the output-relevant subset of Core's scriptPubKey
// object. Address is omitted by Core itself for scripts without a
// well-defined address, such as OP_RETURN endings.
type ScriptPubKey struct {
	Hex     string `json:"hex"`
	Address string `json:"address,omitempty"`
	Type    string `json:"type"`
}

// ScriptSig carries the unlocking script hex of a regular input. Core's asm
// disassembly is wire-only noise and stays out of the decode type.
type ScriptSig struct {
	Hex string `json:"hex"`
}

// VerboseBlockHeader mirrors the consumed fields of Bitcoin Core's
// getblock-header verbose result as a mapping target for
// btcjson.GetBlockHeaderVerboseResult, keeping btcjson on the client's side
// of the package boundary.
type VerboseBlockHeader struct {
	Hash              string
	Confirmations     int64
	Height            int64
	Version           uint32
	MerkleRoot        string
	Time              int64
	Nonce             uint64
	Bits              string
	Difficulty        float64
	PreviousBlockHash string
	NextBlockHash     string
}

// ChainInfo mirrors the fields of Bitcoin Core's getblockchaininfo result
// that document the archive floor: Pruned reports whether the node runs
// pruned mode, and PruneHeight the lowest block it still stores, above
// which undo data (and therefore verbosity 3) remains available.
type ChainInfo struct {
	Chain         string
	Blocks        int64
	Headers       int64
	BestBlockHash string
	Pruned        bool
	PruneHeight   int64
}

// btcFractionDigits is the satoshi scale: Core never serializes more
// precision, and 1e8 of it make a whole BTC.
const btcFractionDigits = 8

const (
	// decimalHalfDigit is the digit a cut remainder's leading character must
	// reach to trigger rounding away from zero.
	decimalHalfDigit = '5'

	// decimalNineDigit is the carrying digit the increment helper rolls
	// over.
	decimalNineDigit = '9'

	// oddDecimalDigits is the odd half of the decimal alphabet: exactly the
	// digits a banker's tie steps up from.
	oddDecimalDigits = "13579"
)

// btcToSatoshis converts a raw RPC amount (BTC as float64) into int64
// satoshis. Core serializes int64 satoshis as decimal floats, and every
// wire amount is far inside float64's 53-bit mantissa, so the shortest
// decimal representation that round-trips the float reproduces the
// serialized satoshis exactly; parsing that shortest form therefore never
// loses a satoshi. Rounding half-to-even at the 8th decimal engages only
// for inputs that are not exact wire values, as a bounded fallback.
// Non-finite, negative and out-of-range amounts are hard errors: wire data
// can never produce them.
func btcToSatoshis(f float64) (int64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("btc amount %v is not finite", f)
	}
	if f < 0 {
		return 0, fmt.Errorf("btc amount %v is negative", f)
	}
	shortest := strconv.FormatFloat(f, 'f', -1, 64)
	intDigits, fracDigits, hasFraction := strings.Cut(shortest, ".")
	wholeBTC, parseErr := strconv.ParseInt(intDigits, 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("btc amount %v is out of int64 satoshi range: %w", f, parseErr)
	}
	// Whole-coin amounts serialize without a fraction at all (0, 50, 21e6),
	// and those need no remainder split. Rounding happens exactly once,
	// here: a carried fraction can overflow the 8-digit scale into 9
	// digits ("0.999999995" rounds to "100000000"), and a second pass
	// would chop that carry back down.
	satRemainder := int64(0)
	if hasFraction {
		rounded := roundFraction(fracDigits)
		satRemainder, parseErr = strconv.ParseInt(rounded, 10, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("btc amount %v has an unparsable fraction: %w", f, parseErr)
		}
	}
	const satoshisPerBTC = 100000000
	// The overflow guard holds at room-temperature precision:
	// wholeBTC * satoshisPerBTC + satRemainder exceeds int64 iff wholeBTC
	// outruns what subtracting the remainder from the max leaves.
	if wholeBTC > (math.MaxInt64-satRemainder)/satoshisPerBTC {
		return 0, fmt.Errorf("btc amount %v overflows int64 satoshis", f)
	}
	return wholeBTC*satoshisPerBTC + satRemainder, nil
}

// roundFraction forces a fractional-digit string onto the satoshi scale,
// padding short fractions to the 8th decimal and cutting longer ones with
// half-to-even rounding on the remainder.
func roundFraction(frac string) string {
	switch {
	case len(frac) < btcFractionDigits:
		return frac + strings.Repeat("0", btcFractionDigits-len(frac))
	case len(frac) == btcFractionDigits:
		return frac
	default:
		kept := frac[:btcFractionDigits]
		rest := frac[btcFractionDigits:]
		switch {
		case restLowerHalf(rest):
			return kept
		case restUpperHalf(rest):
			return incrementDecimal(kept)
		default:
			// The remainder is exactly one half unit: banker's rounding
			// steps up only from an odd kept digit.
			if strings.ContainsRune(oddDecimalDigits, rune(kept[len(kept)-1])) {
				return incrementDecimal(kept)
			}
			return kept
		}
	}
}

// restLowerHalf reports whether the cut remainder sits strictly below one
// half of the last kept unit.
func restLowerHalf(rest string) bool {
	return rest[0] < decimalHalfDigit
}

// restUpperHalf reports whether the cut remainder sits strictly above one
// half of the last kept unit: the first digit outranks 5, or a leading 5 is
// followed by anything nonzero.
func restUpperHalf(rest string) bool {
	return rest[0] > decimalHalfDigit || (rest[0] == decimalHalfDigit && strings.Trim(rest[1:], "0") != "")
}

// incrementDecimal advances a digit string by one decimal unit, carrying
// through and beyond the leading digit the same way an integer increment
// does.
func incrementDecimal(digits string) string {
	incremented := []byte(digits)
	for carry := len(incremented) - 1; carry >= 0; carry-- {
		if incremented[carry] < decimalNineDigit {
			incremented[carry]++
			return string(incremented)
		}
		incremented[carry] = '0'
	}
	return "1" + string(incremented)
}
