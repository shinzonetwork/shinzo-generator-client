package bitcoin

import "sort"

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

// networkIdentity pins one supported chain.network spelling to the two
// values that identify it on the wire: the chain name Bitcoin Core reports
// from getblockchaininfo, and the genesis block hash getblockhash returns
// for height 0.
type networkIdentity struct {
	coreChain   string
	genesisHash string
}

// networkIdentities maps each supported chain.network to the identity
// Connect pins the endpoint against. The chain name and the genesis hash
// are permanent properties of each public network, so a node cannot serve
// one while running another, and a network outside this table fails
// Connect closed before any request is made.
var networkIdentities = map[string]networkIdentity{ //nolint:gochecknoglobals // fixed network-identity table, same pattern as SupportedChainAdapters
	"Mainnet":  {"main", "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"},
	"Regtest":  {"regtest", "0f9188f13cb7b2c71f2a335e3a4fc328bf5beb436012afca590b1a11466e2206"},
	"Signet":   {"signet", "00000008819873e925422c1ff0f99f7cc9bbb232af63a077a480a3633bee1ef6"},
	"Testnet":  {"test", "000000000933ea01ad0ee984209779baaec3ced90fa3f408719526f8d77f4943"},
	"Testnet4": {"testnet4", "00000000da84f2bafbbc53dee25a72ae507ff4914b867c565be350b0da8bf043"},
}

// SupportedBitcoinNetworks lists the chain.network spellings the Connect
// identity check covers, alphabetically, for the unsupported-network error.
func SupportedBitcoinNetworks() []string {
	networks := make([]string, 0, len(networkIdentities))
	for network := range networkIdentities {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	return networks
}
