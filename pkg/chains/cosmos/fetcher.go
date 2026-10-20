package cosmos

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

// blockSource is the native Cosmos block reader (Client in production).
type blockSource interface {
	FetchBlock(ctx context.Context, height int64) (*Block, error)
}

// Fetcher implements chains.Fetcher for cosmos-evm chains: each height is
// fetched from the EVM JSON-RPC and the native Cosmos APIs concurrently and
// returned as one *Bundle. The chain tip comes from the EVM side.
type Fetcher struct {
	evm    chains.Fetcher
	cosmos blockSource
}

// Compile-time guarantee that Fetcher implements chains.Fetcher.
var _ chains.Fetcher = (*Fetcher)(nil)

// NewFetcher composes an EVM fetcher and a Cosmos block source.
func NewFetcher(evmFetcher chains.Fetcher, cosmos blockSource) *Fetcher {
	return &Fetcher{evm: evmFetcher, cosmos: cosmos}
}

// NewFetcherFromConfig builds the EVM fetcher from the geth section and the
// Cosmos client from the cosmos section.
func NewFetcherFromConfig(cfg *config.Config) (*Fetcher, error) {
	evmFetcher, err := evm.NewFetcherFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.Cosmos.RequestTimeoutSeconds) * time.Second
	return NewFetcher(evmFetcher, NewClient(cfg.Cosmos.RPCURL, cfg.Cosmos.LCDURL, timeout)), nil
}

// Connect implements chains.Fetcher. The Cosmos client is plain HTTP and
// needs no connection.
func (f *Fetcher) Connect(ctx context.Context) error {
	return f.evm.Connect(ctx)
}

// FetchBlock implements chains.Fetcher. EVM errors (including not-found,
// which drives the processor's retry policy) are returned unwrapped.
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	var (
		wg              sync.WaitGroup
		evmRaw          any
		cosmosBlock     *Block
		evmErr, cosmErr error
	)
	wg.Add(2) //nolint:mnd // two concurrent fetches
	go func() {
		defer wg.Done()
		evmRaw, evmErr = f.evm.FetchBlock(ctx, height)
	}()
	go func() {
		defer wg.Done()
		cosmosBlock, cosmErr = f.cosmos.FetchBlock(ctx, height)
	}()
	wg.Wait()
	if evmErr != nil {
		return nil, evmErr
	}
	if cosmErr != nil {
		return nil, cosmErr
	}
	evmBundle, ok := evmRaw.(*evm.BlockBundle)
	if !ok {
		return nil, fmt.Errorf("cosmos fetcher: EVM fetcher returned %T, want *evm.BlockBundle", evmRaw)
	}
	return &Bundle{EVM: evmBundle, Cosmos: cosmosBlock}, nil
}

// FetchHighestBlockNumber implements chains.Fetcher.
func (f *Fetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	return f.evm.FetchHighestBlockNumber(ctx)
}

// Close implements chains.Fetcher.
func (f *Fetcher) Close() error {
	return f.evm.Close()
}
