package tron

// Raw java-tron wallet API JSON shapes. The structs carry the wire field
// spellings; document building remaps them in doc_builders.go.

// Block is a java-tron block as served by getblockbynum, getnowblock, and
// getblockbyid: signed header plus full transaction bodies. An unknown
// height or hash arrives as HTTP 200 with body {} decoding to the zero
// value, so an empty BlockID is the not-found marker.
type Block struct {
	BlockID      string        `json:"blockID"`
	BlockHeader  BlockHeader   `json:"block_header"`
	Transactions []Transaction `json:"transactions,omitempty"`
}

// BlockHeader is block_header: the unsigned header content plus the witness
// signature. The wallet API nests the signature inside the header object.
type BlockHeader struct {
	RawData          BlockHeaderRawData `json:"raw_data"`
	WitnessSignature string             `json:"witness_signature"`
}

// BlockHeaderRawData is the unsigned header content. Numbers are int64 on
// the wire; the timestamp is milliseconds.
type BlockHeaderRawData struct {
	Number         int64  `json:"number"`
	Timestamp      int64  `json:"timestamp"`
	ParentHash     string `json:"parentHash"`
	WitnessAddress string `json:"witness_address"`
	Version        int64  `json:"version"`
	TxTrieRoot     string `json:"txTrieRoot"`
}

// Transaction is one transaction body from a block response.
type Transaction struct {
	TxID      string             `json:"txID"`
	RawData   TransactionRawData `json:"raw_data"`
	Ret       []TxRet            `json:"ret,omitempty"`
	Signature []string           `json:"signature,omitempty"`
}

// TransactionRawData is raw_data: the contract list plus the
// whole-transaction metadata. Re-serialized to a string for the transaction
// document's rawData field, since DefraDB has no nested object arrays.
type TransactionRawData struct {
	Contract      []TransactionContract `json:"contract"`
	RefBlockBytes string                `json:"ref_block_bytes,omitempty"`
	RefBlockHash  string                `json:"ref_block_hash,omitempty"`
	RefBlockNum   int64                 `json:"ref_block_num,omitempty"`
	Expiration    int64                 `json:"expiration,omitempty"`
	Timestamp     int64                 `json:"timestamp,omitempty"`
	FeeLimit      int64                 `json:"fee_limit,omitempty"`
	Data          string                `json:"data,omitempty"`
}

// TransactionContract is one contract call. The type field names the
// contract (TransferContract, TriggerSmartContract, ...), and the value map
// is polymorphic per contract type, so sender/recipient/amount extraction
// happens by key in doc_builders.go instead of a struct per contract.
type TransactionContract struct {
	Type      string                   `json:"type"`
	Parameter TransactionContractParam `json:"parameter"`
}

// TransactionContractParam wraps the polymorphic contract call value.
type TransactionContractParam struct {
	Value map[string]any `json:"value"`
}

// TxRet carries the VM result of the contract call, typically "SUCCESS".
type TxRet struct {
	ContractRet string `json:"contractRet"`
}

// TransactionInfo is the receipt of one transaction: execution result,
// paid fee, event logs, and internal transfers, as served by
// gettransactioninfobyblocknum. Its blockNumber and blockTimeStamp tie the
// receipt to its block (the only such tie available: receipts carry no
// block hash).
type TransactionInfo struct {
	ID                   string                `json:"id"`
	BlockNumber          int64                 `json:"blockNumber"`
	BlockTimeStamp       int64                 `json:"blockTimeStamp"`
	Fee                  int64                 `json:"fee"`
	Receipt              TxReceipt             `json:"receipt"`
	ContractResult       []string              `json:"contractResult"`
	ContractAddress      string                `json:"contract_address"`
	Log                  []TxLog               `json:"log"`
	InternalTransactions []InternalTransaction `json:"internal_transactions"`
}

// TxReceipt is the energy and bandwidth accounting of one transaction.
type TxReceipt struct {
	Result            string `json:"result"`
	EnergyUsage       int64  `json:"energy_usage"`
	EnergyUsageTotal  int64  `json:"energy_usage_total"`
	OriginEnergyUsage int64  `json:"origin_energy_usage"`
	EnergyFee         int64  `json:"energy_fee"`
	NetUsage          int64  `json:"net_usage"`
	NetFee            int64  `json:"net_fee"`
}

// TxLog is one event log. The wallet API serves only address, topics, and
// data; every other document field is derived from the parent receipt and
// block in doc_builders.go.
type TxLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// InternalTransaction is one internal movement inside a transaction, as
// reported by the node's internal_transactions list.
type InternalTransaction struct {
	Hash              string          `json:"hash"`
	CallerAddress     string          `json:"caller_address"`
	TransferToAddress string          `json:"transferTo_address"`
	CallValueInfo     []CallValueInfo `json:"callValueInfo"`
	Rejected          bool            `json:"rejected"`
	Note              string          `json:"note"`
}

// CallValueInfo is one asset movement inside an internal transaction:
// native sun via CallValue, or a TRC-10 asset via AssetName. The node
// serves the asset id either as a JSON string or as a JSON number
// depending on its protobuf-JSON conversion, so the raw form is kept and
// normalized in firstCallValue.
type CallValueInfo struct {
	CallValue int64 `json:"callValue"`
	AssetName any   `json:"assetName"`
}

// BlockBundle is the concrete return type of Fetcher.FetchBlock. It bundles
// the raw block with its transactions and receipts. The type is exported so
// other packages can construct it directly in tests; in production it stays
// behind the any return of FetchBlock and is type-asserted only by the
// converter in this package.
type BlockBundle struct {
	Block        *Block
	Transactions []*Transaction
	Receipts     []*TransactionInfo
}
