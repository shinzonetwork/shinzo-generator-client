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
// Phase 1 registers the full factory set but only Collections is functional:
// Fetcher and Converter are implemented in Phases 2-3 of the Solana adapter
// integration (specs/features/SOLANA-ADAPTER-INTEGRATION.md) and fail fast
// with an explanatory error until then.
func init() {
	chains.RegisterChain(AdapterName, chains.ChainFactories{
		Fetcher: func(_ *config.Config) (chains.Fetcher, error) {
			return nil, fmt.Errorf("solana fetcher not yet implemented (planned Phase 2 of the Solana adapter integration)")
		},
		Converter: func(_ *config.Config) (chains.Converter, error) {
			return nil, fmt.Errorf("solana converter not yet implemented (planned Phase 3 of the Solana adapter integration)")
		},
		Collections: func(cfg *config.Config) (chains.Collections, error) {
			return NewCollectionNames(chainPrefixFromConfig(cfg)), nil
		},
	})
}
