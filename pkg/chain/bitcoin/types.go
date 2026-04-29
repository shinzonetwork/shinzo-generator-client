package bitcoin

// Block is the decoded form of Bitcoin Core `getblock` (verbosity 3).
// Only fields that this indexer actually persists or needs for routing are
// declared. Bitcoin Core emits additional fields (mediantime, target,
// difficulty, size, weight, nTx, coinbase_tx, …) which are derivable from
// the kept fields or chain context; encoding/json drops them silently.
type Block struct {
	Hash              string `json:"hash"`
	Height            int64  `json:"height"`
	Version           int32  `json:"version"`
	PreviousBlockHash string `json:"previousblockhash,omitempty"`
	MerkleRoot        string `json:"merkleroot"`
	Time              int64  `json:"time"`
	Bits              string `json:"bits"`
	Nonce             uint32 `json:"nonce"`
	Chainwork         string `json:"chainwork"`
	Tx                []Tx   `json:"tx"`
}

// Tx is a transaction inside a verbosity-3 block.
// `Hex`, `Size`, `Vsize`, `Weight` are not decoded — they are derivable from
// the structured fields by re-serialization.
type Tx struct {
	Txid     string   `json:"txid"`
	Hash     string   `json:"hash"`
	Version  uint32   `json:"version"`
	Locktime uint32   `json:"locktime"`
	Vin      []Vin    `json:"vin"`
	Vout     []Vout   `json:"vout"`
	Fee      *float64 `json:"fee,omitempty"`
}

// IsCoinbase reports whether the transaction is a coinbase tx.
// A coinbase has exactly one input with the `coinbase` field populated.
func (t *Tx) IsCoinbase() bool {
	return len(t.Vin) == 1 && t.Vin[0].Coinbase != ""
}

// Vin is a transaction input. Coinbase inputs leave Txid/Vout/ScriptSig zero
// and populate Coinbase. Regular inputs reference a previous output by
// (Txid, Vout) and carry that output's denormalized metadata in PrevOut.
type Vin struct {
	Txid      string     `json:"txid,omitempty"`
	Vout      uint32     `json:"vout,omitempty"`
	ScriptSig *ScriptSig `json:"scriptSig,omitempty"`
	Sequence  uint32     `json:"sequence"`
	Coinbase  string     `json:"coinbase,omitempty"`
	Witness   []string   `json:"txinwitness,omitempty"`
	PrevOut   *PrevOut   `json:"prevout,omitempty"`
}

// ScriptSig is the unlocking script on a non-coinbase input.
// Asm is the disassembly; we only persist Hex.
type ScriptSig struct {
	Hex string `json:"hex"`
}

// PrevOut carries the spent output's value, address and script type from
// Bitcoin Core's block undo data. Height/generated/full scriptPubKey are
// dropped: they are derivable by looking up the previous transaction.
type PrevOut struct {
	Value        float64      `json:"value"`
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// Vout is a transaction output.
type Vout struct {
	Value        float64      `json:"value"`
	N            uint32       `json:"n"`
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// ScriptPubKey is the locking script on an output. We persist the raw
// script (Hex) and the modern Bitcoin Core decoded fields (Address, Type).
// Asm and Desc are dropped — both are pure functions of Hex.
type ScriptPubKey struct {
	Hex     string `json:"hex"`
	Address string `json:"address,omitempty"`
	Type    string `json:"type"`
}
