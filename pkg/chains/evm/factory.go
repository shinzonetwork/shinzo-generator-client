package evm

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// AdapterName is the name under which the EVM chain family's factories are
// registered in the chain factory registry. It matches the "adapter" value in
// config files, so dispatch by cfg.Chain.Adapter resolves to it.
const AdapterName = "evm"

// Factories returns the EVM chain family's factory functions (Fetcher,
// Converter, and Collections) as one bundle. Register persists it in the chain
// factory registry, and per-chain mains construct it directly, so both paths
// use the same three factories by construction — no partial registration is
// possible.
func Factories() chains.ChainFactories {
	return chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg)
		},
		Converter: func(cfg *config.Config) (chains.Converter, error) {
			// A nil *Converter returned as chains.Converter would not equal nil.
			c, err := NewConverter(cfg)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Collections: NewCollectionsFromConfig,
	}
}

// Register registers the EVM chain family's factories in the unified
// chainFactoryRegistry under AdapterName. Mains and test binaries call it
// explicitly instead of relying on an init() side effect, so a blank import
// never turns registration on by accident. While the registry exists, every
// binary that dispatches through chains.NewFetcher/NewConverter/NewCollections
// must call this before its first dispatch.
func Register() {
	chains.RegisterChain(AdapterName, Factories())
}
