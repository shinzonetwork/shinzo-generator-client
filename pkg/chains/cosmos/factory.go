// Package cosmos implements the cosmos-evm chain family: Cosmos SDK chains
// with an EVM module (e.g. ShinzoHub). Each block is indexed from both sides
// into one set of collections under the chain prefix — the EVM view through
// pkg/chains/evm (Block, Transaction, Log, AccessListEntry) and the native
// view through CometBFT and the Cosmos REST API (CosmosTransaction,
// CosmosMessage, CosmosEvent) — sharing one Block doc and one block signature.
package cosmos

import (
	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// init registers the cosmos-evm chain family. The binary blank-imports this
// package so the registration runs.
func init() {
	chains.RegisterChain(config.CosmosEVMChainAdapter, chains.ChainFactories{
		Fetcher: func(cfg *config.Config) (chains.Fetcher, error) {
			return NewFetcherFromConfig(cfg)
		},
		Converter: func(cfg *config.Config) (chains.Converter, error) {
			return NewConverter(cfg), nil
		},
		Collections: func(cfg *config.Config) (chains.Collections, error) {
			return NewConverter(cfg).Collections(), nil
		},
	})
}
