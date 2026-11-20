package evm

import (
	"encoding/json"
	"strings"
)

// ClientAdapter converts a raw eth_getBlockByNumber response (full txs)
// into the package's Block representation. Implementations own the whole
// conversion: envelope decode, identity fields, and chain-unique fields.
// Receipts stay on the client: their RPC shape is chain-agnostic.
//
// The adapter covers the EVM-level path only: Cosmos-level messages
// (ShinzoHub) live outside the EVM adapter. Every chain is served by the
// default passthrough implementation (ConvertRawBlock); a chain whose
// conversion genuinely cannot be passthrough registers an override in
// chainAdapters, which is the entire per-chain surface. The interface must
// not grow a second method.
type ClientAdapter interface {
	ConvertBlock(rawBlock json.RawMessage) (*Block, error)
}

// defaultClientAdapter is the tolerant passthrough conversion shared by
// every chain. It is stateless, so a single shared instance is safe.
type defaultClientAdapter struct{}

// defaultAdapter is the singleton every unregistered chain name resolves to.
var defaultAdapter ClientAdapter = defaultClientAdapter{} //nolint:gochecknoglobals // stateless singleton, safe to share

// ConvertBlock delegates to the package-level ConvertRawBlock helper so a
// future registered adapter can reuse the same conversion instead of
// duplicating it.
func (defaultClientAdapter) ConvertBlock(rawBlock json.RawMessage) (*Block, error) {
	return ConvertRawBlock(rawBlock)
}

// chainAdapters maps normalized chain names to their ClientAdapter. It
// starts empty: every chain name falls back to defaultAdapter until a chain
// whose conversion cannot be passthrough registers here.
var chainAdapters = map[string]ClientAdapter{} //nolint:gochecknoglobals // registry populated at drop-in time and read on every construction

// adapterForChain resolves the ClientAdapter for a chain name. Lookup is
// case-insensitive and whitespace-tolerant; unknown or empty names fall
// back to the default adapter.
func adapterForChain(chainName string) ClientAdapter {
	if adapter, ok := chainAdapters[normalizeChainName(chainName)]; ok {
		return adapter
	}
	return defaultAdapter
}

// normalizeChainName lowercases and trims a chain name. It is the single
// normalization shared by the adapter registry, so "Ethereum" and
// "  ethereum  " resolve to the same entry.
func normalizeChainName(chainName string) string {
	return strings.ToLower(strings.TrimSpace(chainName))
}
