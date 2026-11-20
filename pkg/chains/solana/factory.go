package solana

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// Factories returns the Solana chain family's factory functions (Fetcher,
// Converter, and Collections) as one bundle. Per-chain mains construct it and
// inject it into the app, so every dispatch path uses the same three
// factories by construction and no partial wiring is possible.
func Factories() chains.ChainFactories {
	return chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg)
		},
		Converter: func(cfg *config.Config) (chains.Converter, error) {
			return NewConverter(cfg), nil
		},
		Collections: NewCollectionsFromConfig,
	}
}
