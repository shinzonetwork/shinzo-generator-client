package types

// TransactionReceipt represents an Ethereum transaction receipt
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

type Block struct {
	BaseFeePerGas string        `json:"baseFeePerGas"`
	Difficulty    string        `json:"difficulty"`
	ExtraData     string        `json:"extraData"`
	GasLimit      string        `json:"gasLimit"`
	GasUsed       string        `json:"gasUsed"`
	Hash          string        `json:"hash"`
	LogsBloom     string        `json:"logsBloom"`
	MixHash       string        `json:"mixHash"`
	Nonce         string        `json:"nonce"`
	Number        int           `json:"number"`
	ParentHash    string        `json:"parentHash"`
	ReceiptsRoot  string        `json:"receiptsRoot"`
	Sha3Uncles    string        `json:"sha3Uncles"`
	Size          string        `json:"size"`
	StateRoot     string        `json:"stateRoot"`
	Timestamp     string        `json:"timestamp"`
	Transactions  []Transaction `json:"transactions,omitempty"`
	// Avalanche-specific fields
	BlockExtraData string `json:"blockExtraData,omitempty"`
	BlockGasCost   string `json:"blockGasCost,omitempty"`
	ExtDataGasUsed string `json:"extDataGasUsed,omitempty"`
	ExtDataHash    string `json:"extDataHash,omitempty"`
}

type Transaction struct {
	// Transaction fields
	BlockHash        string `json:"blockHash"`
	BlockNumber      int    `json:"blockNumber"`
	From             string `json:"from"`
	Gas              string `json:"gas"`
	GasPrice         string `json:"gasPrice"`
	Hash             string `json:"hash"`
	Input            string `json:"input"`
	Nonce            string `json:"nonce"`
	To               string `json:"to"`
	TransactionIndex int    `json:"transactionIndex"`
	Value            string `json:"value"`
	Type             string `json:"type"`
	ChainId          string `json:"chainId"`
	V                string `json:"v"`
	R                string `json:"r"`
	S                string `json:"s"`
	// Receipt fields
	ContractAddress   string `json:"contractAddress"`
	CumulativeGasUsed string `json:"cumulativeGasUsed"`
	EffectiveGasPrice string `json:"effectiveGasPrice"`
	GasUsed           string `json:"gasUsed"`
	Status            string `json:"status"`
	LogsBloom         string `json:"logsBloom"`
	Logs              []Log  `json:"logs,omitempty"`
}

type Log struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      int      `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex int      `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         int      `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

type Response struct {
	Data map[string][]struct {
		DocID string `json:"_docID"` // the document ID of the item in the collection
	} `json:"data"` // the data returned from the query
}

type Request struct {
	Type  string `json:"type"`
	Query string `json:"query"`
}

type Error struct {
	Level   int    `json:"level"`
	Message string `json:"message"`
}

type DefraDoc struct {
	JSON interface{} `json:"json"`
}
