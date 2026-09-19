package solana

import (
	"fmt"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// init registers the Solana chain family's factories (Fetcher, Converter, and
// Collections) in the unified chainFactoryRegistry via a single RegisterChain
// call, mirroring pkg/chains/evm/factory.go. The binary blank-imports
// pkg/chains/solana so this init() runs before any dispatch via
// chains.NewFetcher/NewConverter/NewCollections.
//
// The Fetcher is fully implemented (transport + classification); the
// Converter remains a fail-fast stub until document conversion and link
// resolution land in a later phase of this integration.
func init() {
	chains.RegisterChain(AdapterName, chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg)
		},
		Converter: func(_ *config.Config) (chains.Converter, error) {
			return nil, fmt.Errorf("solana converter not yet implemented (planned Phase 3 of the Solana adapter integration)")
		},
		Collections: func(cfg *config.Config) (chains.Collections, error) {
			return NewCollectionNames(chainPrefixFromConfig(cfg)), nil
		},
	})
}
