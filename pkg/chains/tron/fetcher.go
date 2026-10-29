package tron

import (
	"context"
	"fmt"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
)

// tronClient abstracts the subset of *Client methods used by the
// fetcher. It exists so tests can inject a lightweight fake without serving
// a real wallet API. *Client satisfies this interface.
type tronClient interface {
	GetBlockByNum(ctx context.Context, num int64) (*Block, error)
	GetBlockByID(ctx context.Context, blockID string) (*Block, error)
	GetNowBlock(ctx context.Context) (*Block, error)
	GetSolidifiedNowBlock(ctx context.Context) (*Block, error)
	GetTransactionInfoByBlockNum(ctx context.Context, num int64) ([]*TransactionInfo, error)
	Close() error
}

// Fetcher is the RPC I/O layer for Tron chains. It fetches raw block data
// (block + transactions + receipts) from a java-tron wallet API endpoint and
// queries the chain tip. It implements chains.Fetcher.
//
// Fetcher is safe for concurrent use across different block heights; the
// orchestration layer is responsible for parallel fan-out.
type Fetcher struct {
	client tronClient

	// Connection-config fields populated by NewFetcherFromConfig. When the
	// node URL is non-empty, Connect(ctx) builds the HTTP client from these
	// values; the nodeURL empty check surfaces a mis-deployed Tron deploy
	// (validateConfig already guards this at config load). The low-level
	// NewFetcher constructor sets client directly and leaves these blank,
	// making Connect a no-op.
	nodeURL     string
	solidityURL string
	apiKey      string
	dialTimeout time.Duration
}

// Compile-time guarantee that Fetcher implements chains.Fetcher.
var _ chains.Fetcher = (*Fetcher)(nil)

// NewFetcher creates a Fetcher wrapping the given wallet-API client. The
// client is treated as pre-connected, so Connect is a no-op.
func NewFetcher(client tronClient) *Fetcher {
	return &Fetcher{client: client}
}

// NewFetcherFromConfig creates a Fetcher from the given config without
// dialing the wallet API. Call Connect(ctx) to build the HTTP client before
// using the fetch methods.
func NewFetcherFromConfig(cfg *config.Config) (*Fetcher, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError("chain", "NewFetcherFromConfig", "config is nil", "", nil)
	}
	dialSeconds := cfg.Tron.DialTimeoutSeconds
	if dialSeconds <= 0 {
		dialSeconds = config.DefaultTronDialTimeoutSeconds
	}
	return &Fetcher{
		nodeURL:     cfg.Tron.NodeURL,
		solidityURL: cfg.Tron.SolidityURL,
		apiKey:      cfg.Tron.APIKey,
		dialTimeout: time.Duration(dialSeconds) * time.Second,
	}, nil
}

// Connect builds the wallet-API HTTP client from the connection-config
// fields. The provided context is checked for cancellation before the
// client is built (the wallet endpoints are served lazily) and is NOT
// retained. If the fetcher was built via NewFetcher (pre-connected
// client), Connect is a no-op.
func (f *Fetcher) Connect(ctx context.Context) error {
	if f.client != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("connect context cancelled before connecting: %w", err)
	}
	if f.nodeURL == "" {
		return errors.NewRPCConnectionFailed("rpc", "Connect", "tron endpoints",
			fmt.Errorf("empty tron.node_url: set config tron.node_url or the TRON_RPC_URL env variable"))
	}
	f.client = NewClient(f.nodeURL, f.solidityURL, f.apiKey, f.dialTimeout)
	return nil
}

// FetchBlock implements chains.Fetcher. It retrieves the block at the given
// height plus its receipts, returning them bundled in a *BlockBundle (typed
// as any per the interface). Both reads are by height, and Tron receipts
// carry no block hash, so the bundle is pinned by verifying every receipt's
// blockNumber and blockTimeStamp against the block: a mismatch fails the
// fetch and the block processor retries.
func (f *Fetcher) FetchBlock(ctx context.Context, height int64) (any, error) {
	if f.client == nil {
		return nil, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlock")
	}
	block, err := f.fetchBlock(ctx, height)
	if err != nil {
		return nil, err
	}
	transactions, receipts, err := f.fetchReceipts(ctx, block, height)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch block %d receipts: %w", height, err)
	}
	return &BlockBundle{
		Block:        block,
		Transactions: transactions,
		Receipts:     receipts,
	}, nil
}

// FetchHighestBlockNumber implements chains.Fetcher. It returns the
// FullNode's latest block number, which may be non-solidified.
func (f *Fetcher) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchHighestBlockNumber")
	}
	block, err := f.client.GetNowBlock(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get tron head block: %w", err)
	}
	return block.BlockHeader.RawData.Number, nil
}

// FetchFinalizedBlockNumber implements chains.Fetcher. It returns the
// SolidityNode's latest block number, the solidified tip that serves as the
// reorg walk-back floor.
func (f *Fetcher) FetchFinalizedBlockNumber(ctx context.Context) (int64, error) {
	if f.client == nil {
		return 0, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchFinalizedBlockNumber")
	}
	block, err := f.client.GetSolidifiedNowBlock(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get solidified tron head block: %w", err)
	}
	return block.BlockHeader.RawData.Number, nil
}

// FetchBlockHeaderByHash implements chains.Fetcher. It retrieves only the
// header fields (height, content hash, parent hash) of the block with the
// given blockID, so the reorg walk-back follows parent hashes down the new
// chain without paying for bundle data. The returned hash echoes the
// requested hash, matching EVM semantics: the walk-back continues by parent
// hash either way. A hash the node does not know surfaces as a "not found"
// error so pkg/errors.IsErrNotFound classifies it like a by-height fetch.
func (f *Fetcher) FetchBlockHeaderByHash(ctx context.Context, hash string) (chains.BlockHeader, error) {
	if f.client == nil {
		return chains.BlockHeader{}, fmt.Errorf("fetcher not connected: call Connect(ctx) before FetchBlockHeaderByHash")
	}
	block, err := f.client.GetBlockByID(ctx, hash)
	if err != nil {
		return chains.BlockHeader{}, err
	}
	return chains.BlockHeader{
		Number:     block.BlockHeader.RawData.Number,
		Hash:       hash,
		ParentHash: block.BlockHeader.RawData.ParentHash,
	}, nil
}

// Close implements chains.Fetcher. It releases the wallet-API client.
func (f *Fetcher) Close() error {
	if f.client != nil {
		return f.client.Close()
	}
	return nil
}

// fetchBlock fetches a block by number, returning errors as-is so the
// caller (block processor) can apply its own retry policy.
func (f *Fetcher) fetchBlock(ctx context.Context, blockNum int64) (*Block, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return f.client.GetBlockByNum(ctx, blockNum)
}

// fetchReceipts fetches the receipts of the given block and verifies the
// bundle pin. Tron receipts carry a block number and timestamp but no block
// hash, so number and timestamp are the ties that hold a receipt to this
// block; the reorg engine's parentHash continuity check plus by-hash
// walk-back bound the residual cross-branch risk beyond that.
func (f *Fetcher) fetchReceipts(ctx context.Context, block *Block, blockNum int64) ([]*Transaction, []*TransactionInfo, error) {
	receipts, err := f.client.GetTransactionInfoByBlockNum(ctx, blockNum)
	if err != nil {
		return nil, nil, err
	}
	if err := verifyReceiptPin(block, receipts); err != nil {
		return nil, nil, err
	}
	transactions := make([]*Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}
	return transactions, receipts, nil
}

// verifyReceiptPin reports whether every receipt belongs to the fetched
// block. The pin must contain no "not found": a mismatch means content the
// fetch must not bundle, not a benign missing block.
func verifyReceiptPin(block *Block, receipts []*TransactionInfo) error {
	num := block.BlockHeader.RawData.Number
	ts := block.BlockHeader.RawData.Timestamp
	for i, receipt := range receipts {
		if receipt == nil {
			continue
		}
		if receipt.BlockNumber != num || receipt.BlockTimeStamp != ts {
			return fmt.Errorf("receipt %d (tx %s) block number/timestamp mismatch: "+
				"receipt block number %d, timestamp %d, but block has number %d, timestamp %d",
				i, receipt.ID, receipt.BlockNumber, receipt.BlockTimeStamp, num, ts)
		}
	}
	return nil
}
