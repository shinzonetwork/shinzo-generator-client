package evm

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

// Fetch-related retry constants. maxRPCRetries is also referenced by
// adapter.go (createBlockBatchWithRetry) since it lives in the same package.
const (
	// rpcErrorRetryBaseDelay is the base delay for retrying RPC errors
	// (multiplied by attempt number).
	rpcErrorRetryBaseDelay = 500 * time.Millisecond

	// maxRPCRetries is the maximum number of retries for non-"not found" RPC
	// errors. Also used by adapter.createBlockBatchWithRetry for transaction
	// conflict retries.
	maxRPCRetries = 3
)

// rpcClient abstracts the subset of *rpc.EthereumClient methods used by the
// fetcher. It exists so tests can inject a lightweight fake without dialing a
// real RPC endpoint. *rpc.EthereumClient satisfies this interface.
type rpcClient interface {
	GetLatestBlockNumber(ctx context.Context) (*big.Int, error)
	GetBlockByNumber(ctx context.Context, blockNumber *big.Int) (*types.Block, error)
	GetBlockReceipts(ctx context.Context, blockNumber *big.Int) ([]*types.TransactionReceipt, error)
	GetTransactionReceipt(ctx context.Context, txHash string) (*types.TransactionReceipt, error)
	Close() error
}

// BlockBundle is the concrete return type of Fetcher.FetchBlock. It bundles
// the raw block data with its transactions and receipts. The type is
// unexported because it is only referenced within the evm package: the adapter
// type-asserts it now, the converter will type-assert it in Phase C, and the
// indexer always works with the `any` return value.
type BlockBundle struct {
	Block        *types.Block
	Transactions []*types.Transaction
	Receipts     []*types.TransactionReceipt
}

// Fetcher is the RPC I/O layer for EVM chains. It fetches raw block data
// (block + transactions + receipts) from the chain node and queries the
// on-chain tip. It implements chains.Fetcher.
//
// Fetcher is safe for concurrent use across different block heights; the
// orchestration layer is responsible for parallel fan-out.
type Fetcher struct {
	client         rpcClient
	receiptWorkers int

	// Connection-config fields populated by NewFetcherFromConfig. When
	// non-empty, Connect(ctx) dials the RPC endpoint using these values.
	// The low-level NewFetcher constructor sets client directly and leaves
	// these blank, making Connect a no-op.
	nodeURL    string
	wsURL      string
	apiKey     string
	apiKeyType string
}

// Compile-time guarantee that Fetcher implements chains.Fetcher.
var _ chains.Fetcher = (*Fetcher)(nil)

// NewFetcher creates an Fetcher wrapping the given RPC client. The
// receiptWorkers parameter controls concurrency for individual receipt fetches
// when the batch call is unavailable (fallback path).
func NewFetcher(client rpcClient, receiptWorkers int) *Fetcher {
	return &Fetcher{
		client:         client,
		receiptWorkers: receiptWorkers,
	}
}

// NewFetcherFromConfig creates a Fetcher from the given config without dialing
// the RPC endpoint. Call Connect(ctx) to establish the connection before using
// FetchBlock/FetchHighestBlockNumber.
func NewFetcherFromConfig(cfg *config.Config) (*Fetcher, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError("chain", "NewFetcherFromConfig", "config is nil", "", nil)
	}
	receiptWorkers := cfg.Indexer.ReceiptWorkers
	if receiptWorkers <= 0 {
		receiptWorkers = 16 //nolint:mnd
	}
	return &Fetcher{
		nodeURL:        cfg.Geth.NodeURL,
		wsURL:          cfg.Geth.WsURL,
		apiKey:         cfg.Geth.APIKey,
		apiKeyType:     cfg.Geth.APIKeyType,
		receiptWorkers: receiptWorkers,
	}, nil
}

// Connect dials the RPC endpoint using the connection-config fields. If the
// fetcher was built via NewFetcher (pre-connected client), Connect is a no-op.
func (f *Fetcher) Connect(_ context.Context) error {
	if f.client != nil {
		return nil
	}
	client, err := rpc.NewEthereumClient(f.nodeURL, f.wsURL, f.apiKey, f.apiKeyType) //nolint:contextcheck // rpc.NewEthereumClient does not accept a context yet
	if err != nil {
		return fmt.Errorf("create ethereum client: %w", err)
	}
	f.client = client
	return nil
}

// FetchBlock implements chains.Fetcher. It retrieves the block at the given
// height along with its transactions and receipts, returning them bundled in a
// *BlockBundle (typed as any per the interface).
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	if f.client == nil {
		return nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlock")
	}
	block, err := f.fetchBlockWithRetry(ctx, height)
	if err != nil {
		return nil, err
	}
	transactions, receipts := f.fetchTransactionsAndReceipts(ctx, block, height)
	return &BlockBundle{
		Block:        block,
		Transactions: transactions,
		Receipts:     receipts,
	}, nil
}

// FetchHighestBlockNumber implements chains.Fetcher. It queries the RPC
// endpoint for the latest block number.
func (f *Fetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchHighestBlockNumber")
	}
	n, err := f.client.GetLatestBlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest block number: %w", err)
	}
	return n.Int64(), nil
}

// Close implements chains.Fetcher. It closes the underlying RPC client.
func (f *Fetcher) Close() error {
	if f.client != nil {
		return f.client.Close()
	}
	return nil
}

// fetchBlockWithRetry fetches a block by number. When the block is not yet
// available on chain the error is returned as-is (it matches
// errors.IsErrNotFound) so the caller can decide to retry. Other RPC errors
// are retried up to maxRPCRetries times with linear backoff.
func (f *Fetcher) fetchBlockWithRetry(ctx context.Context, blockNum int64) (*types.Block, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		block, err := f.client.GetBlockByNumber(ctx, big.NewInt(blockNum))
		if err == nil {
			return block, nil
		}

		if errors.IsErrNotFound(err) {
			// Block not yet available; surface the not-found error so the
			// caller (block processor) can apply its own retry policy.
			return nil, err
		}

		otherErrors++
		if otherErrors >= maxRPCRetries {
			return nil, fmt.Errorf("failed to fetch block: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(otherErrors) * rpcErrorRetryBaseDelay):
		}
	}
}

// fetchTransactionsAndReceipts builds the transaction pointer slice and fetches
// receipts, falling back to individual fetches when the batch call fails.
func (f *Fetcher) fetchTransactionsAndReceipts(ctx context.Context, block *types.Block, blockNum int64) ([]*types.Transaction, []*types.TransactionReceipt) {
	transactions := make([]*types.Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}

	batchReceipts, batchErr := f.client.GetBlockReceipts(ctx, big.NewInt(blockNum))
	if batchErr == nil {
		return transactions, batchReceipts
	}

	if ctx.Err() == nil {
		logger.Sugar.Debugf("Block %d: eth_getBlockReceipts not available, falling back to individual fetches: %v", blockNum, batchErr)
	}

	receipts := make([]*types.TransactionReceipt, len(block.Transactions))
	var wg sync.WaitGroup
	sem := make(chan struct{}, f.receiptWorkers)

	for i, tx := range block.Transactions {
		wg.Add(1)
		go func(idx int, txHash string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			receipt, err := f.client.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				if ctx.Err() == nil && logger.Sugar != nil {
					logger.Sugar.Warnf("Failed to fetch receipt for tx %s: %v", txHash, err)
				}
				return
			}
			receipts[idx] = receipt
		}(i, tx.Hash)
	}
	wg.Wait()

	validReceipts := make([]*types.TransactionReceipt, 0, len(receipts))
	for _, r := range receipts {
		if r != nil {
			validReceipts = append(validReceipts, r)
		}
	}

	return transactions, validReceipts
}

func init() {
	chains.RegisterFetcherFactory("evm", func(cfg *config.Config) (chains.Fetcher, error) {
		return NewFetcherFromConfig(cfg)
	})
}
