package solana

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// init registers the Solana chain family's factories (Fetcher, Converter, and
// Collections) in the unified chainFactoryRegistry via a single RegisterChain
// call, mirroring pkg/chains/evm/factory.go. The binary blank-imports
// pkg/chains/solana so this init() runs before any dispatch via
// chains.NewFetcher/NewConverter/NewCollections.
func init() {
	chains.RegisterChain(AdapterName, chains.ChainFactories{
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
