package evm

import (
	"encoding/json"
	"strings"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// ClientAdapter is the per-chain extension surface of EthereumClient.
type ClientAdapter interface {
	// NeedsRawBlock declares that the chain reads the raw
	// eth_getBlockByNumber response (tolerant decode, RPC-provided
	// from/yParity, chain-unique header fields). False keeps the existing
	// ethclient typed decode (Ethereum, BSC, Linea, Monad, Polygon).
	NeedsRawBlock() bool

	// DecodeTransactions turns the raw tx objects of the block (standard
	// envelopes decoded directly, non-standard envelopes such as Arbitrum
	// 100-106, OP/Base 0x7e and ZKsync 0x71 mapped onto the closest
	// standard envelope so the existing gas-price switch keeps working)
	// into go-ethereum transactions. Implementations must return one
	// transaction per element of rawTxs, in order.
	DecodeTransactions(rawTxs []json.RawMessage) ([]*ethtypes.Transaction, error)

	// ResolveFrom overrides the sender for one tx; ok=false keeps the
	// current behavior (GetFromAddress recovery, ZeroAddress fallback).
	// Explicit-from chains: Polygon's unsigned Bor deposits, OP/Base
	// deposits, Arbitrum L1 messages, ZKsync 0x71.
	ResolveFrom(tx *ethtypes.Transaction, rawTx json.RawMessage) (from string, ok bool)

	// YParity returns the tx's yParity, "" when the chain does not index
	// it (Ethereum, BSC, ZKsync). Linea/Monad derive from v, Polygon from
	// v + type, Arbitrum/OP/Base read it from the raw tx, Avalanche from
	// the coreth raw tx.
	YParity(tx *ethtypes.Transaction, rawTx json.RawMessage) string
}

// defaultClientAdapter reproduces today's Ethereum and BSC behaviour
// exactly: no raw block fetch, sender recovery via GetFromAddress with
// the zero address fallback, and no yParity indexing. Transaction
// decoding happens solely on the client's typed path. It is stateless,
// so a single shared instance is safe.
type defaultClientAdapter struct{}

// defaultAdapter is the singleton every unregistered chain name resolves to.
var defaultAdapter ClientAdapter = defaultClientAdapter{} //nolint:gochecknoglobals // stateless singleton, safe to share

func (defaultClientAdapter) NeedsRawBlock() bool {
	return false
}

// DecodeTransactions returns empty because this adapter is never reached
// through the client's block path: NeedsRawBlock reports false, so the
// client decodes blocks on its typed ethclient path instead, and an empty
// return is the honest result for a method with no caller on this adapter.
func (defaultClientAdapter) DecodeTransactions(_ []json.RawMessage) ([]*ethtypes.Transaction, error) {
	return nil, nil //nolint:nilnil // unreachable stub: the client never fetches raw blocks for this adapter
}

func (defaultClientAdapter) ResolveFrom(_ *ethtypes.Transaction, _ json.RawMessage) (string, bool) {
	return "", false
}

func (defaultClientAdapter) YParity(_ *ethtypes.Transaction, _ json.RawMessage) string {
	return ""
}

// chainAdapters maps normalized chain names to their ClientAdapter. It
// starts empty: every chain name falls back to defaultAdapter until a
// chain-specific adapter registers here, which is the intended drop-in
// point for Arbitrum, OP/Base, Polygon, ZKsync and similar chains.
var chainAdapters = map[string]ClientAdapter{} //nolint:gochecknoglobals // registry populated at drop-in time and read on every construction

// adapterForChain resolves the ClientAdapter for a chain name. Lookup is
// case-insensitive and whitespace-tolerant; unknown or empty names fall
// back to the default adapter, keeping Ethereum and BSC behaviour
// byte-identical.
func adapterForChain(chainName string) ClientAdapter {
	key := strings.ToLower(strings.TrimSpace(chainName))
	if adapter, ok := chainAdapters[key]; ok {
		return adapter
	}
	return defaultAdapter
}
