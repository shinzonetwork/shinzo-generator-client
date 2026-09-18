package evm

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// init registers the EVM chain family's factories (Fetcher, Converter, and
// Collections) in the unified chainFactoryRegistry via a single RegisterChain
// call. This replaces the three separate init() functions that previously lived
// in fetcher.go, converter.go, and collections.go, preventing partial
// registration by construction — all three factories are either registered
// together or not at all. The binary blank-imports pkg/chains/evm so this
// init() runs before any dispatch via chains.NewFetcher/NewConverter/NewCollections.
func init() {
	chains.RegisterChain("evm", chains.ChainFactories{
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
