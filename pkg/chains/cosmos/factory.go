// Package cosmos implements the cosmos-evm chain family: Cosmos SDK chains
// with an EVM module (e.g. ShinzoHub). Each block is indexed from both sides
// into one set of collections under the chain prefix — the EVM view through
// pkg/chains/evm (Block, Transaction, Log, AccessListEntry) and the native
// view through CometBFT and the Cosmos REST API (CosmosTransaction,
// CosmosMessage, CosmosEvent) — sharing one Block doc and one block signature.
//
// The EVM half is resolved through the chains registry (adapter "evm"), so
// this package depends only on the pkg/chains interfaces; binaries must
// blank-import pkg/chains/evm alongside it.
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
			return NewConverter(cfg)
		},
		Collections: func(cfg *config.Config) (chains.Collections, error) {
			evmCollections, err := chains.NewCollections(evmConfig(cfg))
			if err != nil {
				return nil, err
			}
			return NewCollectionNames(evmCollections), nil
		},
	})
}
