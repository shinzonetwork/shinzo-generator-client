package cosmos

import (
	"encoding/json"
)

// Bundle is the raw block returned by the cosmos-evm Fetcher for one height:
// the EVM view (eth JSON-RPC) and the native Cosmos view (CometBFT + REST) of
// the same block. EVM is the EVM chain family's raw block, opaque here and
// handed back to the EVM converter.
type Bundle struct {
	EVM    any
	Cosmos *Block
}

// Block is the native Cosmos view of one block: its transactions with their
// decoded messages and events, plus the block-level events. CometBFT v0.38+
// reports those as FinalizeBlock events; v0.37 (e.g. Kava) splits them into
// BeginBlock and EndBlock events instead.
type Block struct {
	Height         int64
	Hash           string // 0x-prefixed lowercase hex, same value as the EVM block hash
	Transactions   []Transaction
	FinalizeEvents []Event
	BeginEvents    []Event
	EndEvents      []Event
}

// Transaction is one Cosmos SDK transaction with its execution result.
type Transaction struct {
	Hash      string // uppercase hex sha256 of the tx bytes, as CometBFT reports it
	Index     int
	Code      uint32
	Codespace string
	Log       string
	GasWanted string
	GasUsed   string
	Memo      string
	Fee       string // auth_info.fee as JSON
	Messages  []Message
	Events    []Event
}

// Message is one decoded tx message: its type URL and its JSON body, exactly
// as the Cosmos REST API renders it (including "@type").
type Message struct {
	Index   int
	TypeURL string
	Body    string
}

// Event is one ABCI event: its type and attributes as JSON
// ([{"key","value","index"}...]). Index is the position within its
// transaction, or within its block phase for block-level events.
type Event struct {
	Index      int
	Type       string
	Attributes string
}

// --- Wire types ---

// lcdBlockWithTxs is the Cosmos REST response of
// GET /cosmos/tx/v1beta1/txs/block/{height}.
type lcdBlockWithTxs struct {
	Txs     []lcdTx `json:"txs"`
	BlockID struct {
		Hash string `json:"hash"` // base64
	} `json:"block_id"`
	Block struct {
		Header struct {
			Height string `json:"height"`
		} `json:"header"`
		Data struct {
			Txs []string `json:"txs"` // base64 tx bytes
		} `json:"data"`
	} `json:"block"`
	Pagination *struct {
		NextKey *string `json:"next_key"`
	} `json:"pagination"`
}

type lcdTx struct {
	Body struct {
		Messages []json.RawMessage `json:"messages"`
		Memo     string            `json:"memo"`
	} `json:"body"`
	AuthInfo struct {
		Fee json.RawMessage `json:"fee"`
	} `json:"auth_info"`
}

// rpcResponse is the CometBFT JSON-RPC envelope.
type rpcResponse[T any] struct {
	Result *T `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

// blockResults is the CometBFT /block_results result. v0.38 fills
// finalize_block_events; v0.37 fills begin_block_events and end_block_events.
type blockResults struct {
	Height              string      `json:"height"`
	TxsResults          []txResult  `json:"txs_results"`
	FinalizeBlockEvents []abciEvent `json:"finalize_block_events"`
	BeginBlockEvents    []abciEvent `json:"begin_block_events"`
	EndBlockEvents      []abciEvent `json:"end_block_events"`
}

type txResult struct {
	Code      uint32      `json:"code"`
	Codespace string      `json:"codespace"`
	Log       string      `json:"log"`
	GasWanted string      `json:"gas_wanted"`
	GasUsed   string      `json:"gas_used"`
	Events    []abciEvent `json:"events"`
}

type abciEvent struct {
	Type       string            `json:"type"`
	Attributes []json.RawMessage `json:"attributes"`
}
