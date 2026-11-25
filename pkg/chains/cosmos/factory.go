// Package cosmos implements the cosmos-evm chain family: Cosmos SDK chains
// with an EVM module (e.g. ShinzoHub). Each block is indexed from both sides
// into one set of collections under the chain prefix — the EVM view through
// pkg/chains/evm (Block, Transaction, Log, AccessListEntry) and the native
// view through CometBFT and the Cosmos REST API (CosmosTransaction,
// CosmosMessage, CosmosEvent) — sharing one Block doc and one block signature.
//
// The EVM half is built from the EVM chain family's factories, which the
// cosmos-evm binary (cmd/block_poster/cosmos) injects together with this
// package's Factories into the runner; the package itself depends only on the
// pkg/chains interfaces.
package cosmos

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// Factories returns the cosmos-evm chain family's factory functions as one
// bundle for injection into the runner. evmFactories builds the EVM half of
// every block (pass evm.Factories()).
func Factories(evmFactories chains.ChainFactories) chains.ChainFactories {
	return chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg, evmFactories)
		},
		Converter: func(cfg *config.Config) (chains.Converter, error) {
			// A nil *Converter returned as chains.Converter would not equal nil.
			c, err := NewConverter(cfg, evmFactories)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Collections: func(cfg *config.Config) (chains.Collections, error) {
			evmCollections, err := evmFactories.Collections(evmConfig(cfg))
			if err != nil {
				return nil, err
			}
			return NewCollectionNames(evmCollections), nil
		},
	}
}
