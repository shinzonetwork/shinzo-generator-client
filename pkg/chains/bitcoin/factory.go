package bitcoin

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// init registers the Bitcoin chain family's factories (Fetcher, Converter,
// and Collections) in the unified chainFactoryRegistry via a single
// RegisterChain call, preventing partial registration by construction: all
// three factories are either registered together or not at all. The binary
// blank-imports pkg/chains/bitcoin so this init() runs before any dispatch
// via chains.NewFetcher/NewConverter/NewCollections.
//
// The registry key is the config.BitcoinAdapter constant rather than a
// literal, so the adapter-name vocabulary stays owned by the config layer.
// The registration body lives in a named helper instead of init itself, so
// the factory test can restore the shared registry after its
// unregister/restore cycle.
func init() {
	registerChainFactories()
}

// registerChainFactories performs the complete Bitcoin registration in one
// RegisterChain call. Calling it twice is safe: RegisterChain overwrites the
// previous entry wholesale, so a test-time restore can never leave the
// registry half-populated.
func registerChainFactories() {
	chains.RegisterChain(config.BitcoinAdapter, chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg)
		},
		Converter: func(cfg *config.Config) (chains.Converter, error) {
			return NewConverter(cfg), nil
		},
		Collections: func(cfg *config.Config) (chains.Collections, error) {
			return NewCollectionNames(chainPrefixFromConfig(cfg)), nil
		},
	})
}
