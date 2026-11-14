package tron

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// init registers the Tron chain family's factories (Fetcher, Converter, and
// Collections) in the unified chainFactoryRegistry via a single
// RegisterChain call, mirroring the EVM package. Registering all three in
// one place prevents partial registration by construction: the factories
// are either registered together or not at all. A binary blank-imports
// pkg/chains/tron so this init runs before any dispatch via
// chains.NewFetcher/NewConverter/NewCollections.
func init() {
	chains.RegisterChain("tron", chains.ChainFactories{
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
