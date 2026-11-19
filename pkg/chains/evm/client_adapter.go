package evm

import (
	"encoding/json"
	"strings"

	shinzoerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// ClientAdapter converts a raw eth_getBlockByNumber response (full txs)
// into the package's Block representation. Implementations own the whole
// conversion: envelope decode, identity fields, and chain-unique fields.
// Receipts stay on the client: their RPC shape is chain-agnostic.
//
// The adapter covers the EVM-level path only: Cosmos-level messages
// (ShinzoHub) live outside the EVM adapter. Every chain in the vetted
// allowlist is served by the default passthrough implementation
// (ConvertRawBlock); a chain whose conversion genuinely cannot be
// passthrough registers an override in chainAdapters, which is the entire
// per-chain surface. The interface must not grow a second method.
type ClientAdapter interface {
	ConvertBlock(rawBlock json.RawMessage) (*Block, error)
}

// defaultClientAdapter is the tolerant passthrough conversion shared by
// every vetted chain. It is stateless, so a single shared instance is safe.
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
// starts empty: every vetted chain name falls back to defaultAdapter until
// a chain whose conversion cannot be passthrough registers here.
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

// vettedChains is the allowlist of chain names whose passthrough
// correctness is pinned by a checked-in golden fixture under golden/.
// Each entry is the vetting artifact: a chain goes live only together with
// its fixture. Ethereum, BSC, Polygon, Arbitrum One, OP Mainnet, Base and
// ZKsync Era are the confirmed pre-Rust scope; Linea, Monad and Avalanche
// C-Chain are the researched followers that follow the same pattern.
var vettedChains = map[string]struct{}{ //nolint:gochecknoglobals // static allowlist, read on every construction
	"ethereum":          {},
	"bsc":               {},
	"polygon":           {},
	"arbitrum one":      {},
	"op mainnet":        {},
	"base":              {},
	"zksync era":        {},
	"linea":             {},
	"monad":             {},
	"avalanche c-chain": {},
}

// normalizeChainName lowercases and trims a chain name. It is the single
// normalization shared by the allowlist lookup and the adapter registry,
// so "Arbitrum One" and "  arbitrum one  " resolve to the same entry.
func normalizeChainName(chainName string) string {
	return strings.ToLower(strings.TrimSpace(chainName))
}

// validateChainName fails construction for a chain name outside the
// vetted allowlist. An empty name keeps the Ethereum default so existing
// construction paths are unchanged. A chain name with a registered
// adapter bypasses the allowlist: the registration is that chain's own
// vetting artifact. Any other non-empty unknown name is a
// misconfiguration that must fail startup rather than silently index an
// unvetted chain, and the error names the fixture requirement so the
// operator knows what vetting the chain asks for.
func validateChainName(chainName string) error {
	normalized := normalizeChainName(chainName)
	if normalized == "" {
		return nil
	}
	if _, ok := vettedChains[normalized]; ok {
		return nil
	}
	if _, ok := chainAdapters[normalized]; ok {
		return nil
	}
	return shinzoerrors.NewConfigurationError("chain", "NewEthereumClientForChain",
		"unknown chain name: not on the vetted chain allowlist; a new chain needs a captured golden fixture under golden/<chain>/testdata before it goes live",
		chainName, nil)
}
