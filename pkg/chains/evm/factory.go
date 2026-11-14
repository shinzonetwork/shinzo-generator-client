package evm

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// Factories returns the EVM chain family's factory functions (Fetcher,
// Converter, and Collections) as one bundle. Per-chain mains construct it and
// inject it into the app, so every dispatch path uses the same three
// factories by construction and no partial wiring is possible.
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
