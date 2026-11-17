package tron

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// Factories returns the Tron chain family's factory functions (Fetcher,
// Converter, and Collections) as one bundle. The tron main constructs it and
// injects it into the app, so every dispatch path uses the same three
// factories by construction and no partial wiring is possible.
func Factories() chains.ChainFactories {
	return chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg)
		},
		Converter: func(cfg *config.Config) (chains.Converter, error) {
			// NewConverter is lenient by design: a nil or empty config falls
			// back to the Tron__Mainnet prefix, so no nil-check dance here.
			return NewConverter(cfg), nil
		},
		Collections: NewCollectionsFromConfig,
	}
}
