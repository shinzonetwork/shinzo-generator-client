package evm

// AccessListEntry represents an access list entry for EIP-2930 transactions.
type AccessListEntry struct {
	Address     string   `json:"address"`
	StorageKeys []string `json:"storageKeys"`
}

// TransactionReceipt represents an Ethereum transaction receipt.
type TransactionReceipt struct {
	TransactionHash   string `json:"transactionHash"`
	TransactionIndex  string `json:"transactionIndex"`
	BlockHash         string `json:"blockHash"`
	BlockNumber       string `json:"blockNumber"`
	From              string `json:"from"`
	To                string `json:"to"`
	CumulativeGasUsed string `json:"cumulativeGasUsed"`
	GasUsed           string `json:"gasUsed"`
	ContractAddress   string `json:"contractAddress"`
	Logs              []Log  `json:"logs"`
	Status            string `json:"status"`
}

// Block represents an Ethereum block.
type Block struct {
	Hash             string        `json:"hash"`
	Number           string        `json:"number"`
	Timestamp        string        `json:"timestamp"`
	ParentHash       string        `json:"parentHash"`
	Difficulty       string        `json:"difficulty"`
	TotalDifficulty  string        `json:"totalDifficulty"`
	GasUsed          string        `json:"gasUsed"`
	GasLimit         string        `json:"gasLimit"`
	BaseFeePerGas    string        `json:"baseFeePerGas,omitempty"`
	Nonce            string        `json:"nonce"`
	Miner            string        `json:"miner"`
	Size             string        `json:"size"`
	StateRoot        string        `json:"stateRoot"`
	Sha3Uncles       string        `json:"sha3Uncles"`
	TransactionsRoot string        `json:"transactionsRoot"`
	ReceiptsRoot     string        `json:"receiptsRoot"`
	LogsBloom        string        `json:"logsBloom"`
	ExtraData        string        `json:"extraData"`
	MixHash          string        `json:"mixHash"`
	Uncles           []string      `json:"uncles"`
	Transactions     []Transaction `json:"transactions,omitempty"`
}

// Transaction represents an Ethereum transaction.
type Transaction struct {
	Hash                 string            `json:"hash"`
	BlockHash            string            `json:"blockHash"`
	BlockNumber          string            `json:"blockNumber"`
	From                 string            `json:"from"`
	To                   string            `json:"to"`
	Value                string            `json:"value"`
	Gas                  string            `json:"gas"`
	GasPrice             string            `json:"gasPrice"`
	MaxFeePerGas         string            `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas string            `json:"maxPriorityFeePerGas,omitempty"`
	Input                string            `json:"input"`
	Nonce                string            `json:"nonce"`
	TransactionIndex     int               `json:"transactionIndex"`
	Type                 string            `json:"type"`
	ChainID              string            `json:"chainId,omitempty"`
	AccessList           []AccessListEntry `json:"accessList,omitempty"`
	V                    string            `json:"v"`
	R                    string            `json:"r"`
	S                    string            `json:"s"`
	Status               bool              `json:"status"`
	GasUsed              string            `json:"gasUsed,omitempty"`
	CumulativeGasUsed    string            `json:"cumulativeGasUsed,omitempty"`
	EffectiveGasPrice    string            `json:"effectiveGasPrice,omitempty"`
	Logs                 []Log             `json:"logs,omitempty"`
}

// Log represents an Ethereum event log.
type Log struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex int      `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         int      `json:"logIndex"`
	Removed          bool     `json:"removed"`
}
