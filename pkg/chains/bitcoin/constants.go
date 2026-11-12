package bitcoin

// Field names used to build Bitcoin documents.
//
// These constants live in pkg/chains/bitcoin, not pkg/constants, because
// they are Bitcoin-specific: non-test code outside this package must not
// reference them. The block document's own number/hash field names and the
// signature-document field names are the generator-host contract and live in
// pkg/constants. The boundary is enforced by the bitcoin-boundary depguard
// rule in .golangci.yml.
//
// The names are carried through from the Bitcoin Core JSON-RPC object shapes
// (getblock verbosity 3, its vin/vout entries, and the prevout object).

// TxIDFieldName is the document join field shared by the Transaction,
// TxInput, and TxOutput documents. Later write stages query it to link and
// purge documents across collections, and the link stamper resolves it to
// each input/output doc's owning transaction.
const TxIDFieldName = "txid"

// Block header payload field names, carried through from the getblock
// header shape.
const (
	ParentHashFieldName = "parentHash"
	MerkleRootFieldName = "merkleRoot"
	TimeFieldName       = "time"
	BitsFieldName       = "bits"
	NonceFieldName      = "nonce"
	ChainworkFieldName  = "chainwork"
)

// Transaction payload field names, carried through from the getblock
// verbosity 3 transaction shape. feeSat is nullable in the schema: it is
// always omitted for the coinbase transaction and whenever the prevout
// (undo) data needed to compute it is unavailable.
const (
	TransactionIndexFieldName = "txIndex"
	IsCoinbaseFieldName       = "isCoinbase"
	FeeSatFieldName           = "feeSat"
)

// Transaction input payload field names, carried through from the getblock
// verbosity 3 vin shape plus the denormalized prevout fields. prevoutValueSat
// is nullable: it is omitted on coinbase inputs and whenever the node's
// prune state hides the prevout.
const (
	InputIndexFieldName      = "inputIndex"
	PrevTxIDFieldName        = "prevTxid"
	PrevVoutFieldName        = "prevVout"
	ScriptSigFieldName       = "scriptSigHex"
	SequenceFieldName        = "sequence"
	CoinbaseFieldName        = "coinbase"
	WitnessFieldName         = "witness"
	PrevoutValueSatFieldName = "prevoutValueSat"
	PrevoutAddressFieldName  = "prevoutAddress"
	PrevoutTypeFieldName     = "prevoutType"
)

// Transaction output payload field names, carried through from the getblock
// verbosity 3 vout shape.
const (
	VoutIndexFieldName           = "n"
	ValueSatFieldName            = "valueSat"
	ScriptPubKeyHexFieldName     = "scriptPubKeyHex"
	ScriptPubKeyAddressFieldName = "scriptPubKeyAddress"
	ScriptPubKeyTypeFieldName    = "scriptPubKeyType"
)

// version and locktime are written into documents as string literals; they
// are intentionally not constants, mirroring how the evm adapter treats its
// literal-only fields.
