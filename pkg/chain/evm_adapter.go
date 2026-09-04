package chain

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
	"github.com/sourcenetwork/defradb/node"
)

// Retry / signing constants. These mirror pkg/indexer/concurrent_processor.go
// so the adapter's behaviour is identical to the existing processor while the
// hot path still uses the processor (Steps 2-5 will rewire the callers).
const (
	// rpcErrorRetryBaseDelay is the base delay for retrying RPC errors
	// (multiplied by attempt number).
	rpcErrorRetryBaseDelay = 500 * time.Millisecond

	// maxRPCRetries is the maximum number of retries for non-"not found" RPC
	// errors.
	maxRPCRetries = 3

	// transactionConflictRetryBaseDelay is the base delay for retrying
	// transaction conflicts.
	transactionConflictRetryBaseDelay = 50 * time.Millisecond

	// signingQueueSize is the buffer size for the background block signing
	// channel.
	signingQueueSize = 64
)

// rpcClient abstracts the subset of *rpc.EthereumClient methods used by the
// adapter. It exists so tests can inject a lightweight fake without dialing a
// real RPC endpoint. *rpc.EthereumClient satisfies this interface.
type rpcClient interface {
	GetLatestBlockNumber(ctx context.Context) (*big.Int, error)
	GetBlockByNumber(ctx context.Context, blockNumber *big.Int) (*types.Block, error)
	GetBlockReceipts(ctx context.Context, blockNumber *big.Int) ([]*types.TransactionReceipt, error)
	GetTransactionReceipt(ctx context.Context, txHash string) (*types.TransactionReceipt, error)
	Close() error
}

// signingJob holds the data needed to sign an existing block in the background.
type signingJob struct {
	blockNum     int64
	blockHash    string
	block        *types.Block
	transactions []*types.Transaction
	receipts     []*types.TransactionReceipt
}

// EVMAdapter is a Chain implementation backed by an EVM JSON-RPC endpoint and a
// DefraDB BlockHandler.
//
// Lifecycle:
//   - NewEVMAdapter dials the RPC endpoint and builds the collection names from
//     the configured chain prefix.
//   - Init creates the BlockHandler from a running DefraDB node, sets the batch
//     sizes and starts the background signing goroutine.
//   - Close cancels the internal context, drains the signing goroutine and
//     closes the RPC client.
//
// Before Init only GetSchema and GetCollections are valid; every other method
// returns ErrAdapterNotInitialized.
type EVMAdapter struct {
	client         rpcClient
	blockHandler   *defra.BlockHandler
	collections    *constants.CollectionNames
	receiptWorkers int
	signingChan    chan signingJob
	node           *node.Node
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	cfg            *config.Config
}

// Compile-time guarantee that EVMAdapter implements Chain.
var _ Chain = (*EVMAdapter)(nil)

// NewEVMAdapter dials the configured RPC endpoint and returns an EVMAdapter
// ready to be initialised. Init must still be called before DefraDB-backed
// methods can be used.
func NewEVMAdapter(cfg *config.Config) (*EVMAdapter, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError("chain", "NewEVMAdapter", "config is nil", "", nil)
	}
	client, err := rpc.NewEthereumClient(cfg.Geth.NodeURL, cfg.Geth.WsURL, cfg.Geth.APIKey, cfg.Geth.APIKeyType)
	if err != nil {
		return nil, fmt.Errorf("create ethereum client: %w", err)
	}
	return newEVMAdapter(cfg, client), nil
}

// newEVMAdapter builds an EVMAdapter with an injected rpcClient. It is used by
// the exported NewEVMAdapter (production) and by tests (with a fake client).
func newEVMAdapter(cfg *config.Config, client rpcClient) *EVMAdapter {
	prefix := chainPrefixFromConfig(cfg)
	collections := constants.NewCollectionNames(prefix)

	receiptWorkers := 0
	if cfg != nil {
		receiptWorkers = cfg.Indexer.ReceiptWorkers
	}
	if receiptWorkers <= 0 {
		receiptWorkers = 16 //nolint:mnd
	}

	return &EVMAdapter{
		client:         client,
		collections:    collections,
		receiptWorkers: receiptWorkers,
		signingChan:    make(chan signingJob, signingQueueSize),
		cfg:            cfg,
	}
}

// chainPrefixFromConfig derives the collection prefix (e.g. "Ethereum__Mainnet")
// from the chain config, applying the same defaults as the existing indexer.
func chainPrefixFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return constants.DefaultCollectionPrefix
	}
	name := cfg.Chain.Name
	network := cfg.Chain.Network
	if name == "" {
		name = "Ethereum"
	}
	if network == "" {
		network = "Mainnet"
	}
	return fmt.Sprintf("%s__%s", name, network)
}

// Init wires the adapter to a running DefraDB node. After Init returns the
// BlockHandler-backed methods are usable and the background signing goroutine
// is running.
func (a *EVMAdapter) Init(ctx context.Context, defraNode *node.Node) error {
	if a.cfg == nil {
		return errors.NewConfigurationError("chain", "Init", "config is nil", "", nil)
	}
	blockHandler, err := defra.NewBlockHandler(defraNode, a.cfg.Indexer.MaxDocsPerTxn, a.collections)
	if err != nil {
		return fmt.Errorf("create block handler: %w", err)
	}
	blockHandler.SetBatchSizes(
		a.cfg.Indexer.MaxTxDocsPerBatch,
		a.cfg.Indexer.MaxLogDocsPerBatch,
		a.cfg.Indexer.MaxALEDocsPerBatch,
	)

	a.blockHandler = blockHandler
	a.node = defraNode
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.wg.Add(1)
	go a.signBlocks(ctx)
	return nil
}

// signBlocks drains the signing channel, signing existing blocks in the
// background. It mirrors the signing goroutine in concurrent_processor.go.
func (a *EVMAdapter) signBlocks(ctx context.Context) {
	defer a.wg.Done()
	for job := range a.signingChan {
		if ctx.Err() != nil {
			continue
		}
		if _, err := a.blockHandler.CreateBlockSignatureForExistingBlock(
			ctx, job.blockNum, job.blockHash, job.block, job.transactions, job.receipts,
		); err != nil {
			logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", job.blockNum, err)
		}
	}
}

// Close cancels the internal lifecycle, drains the signing goroutine and
// closes the RPC client. It is idempotent.
func (a *EVMAdapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.signingChan != nil {
		close(a.signingChan)
		a.signingChan = nil
	}
	a.wg.Wait()
	if a.client != nil {
		return a.client.Close()
	}
	return nil
}

// FetchAndStoreBlock implements Chain.
func (a *EVMAdapter) FetchAndStoreBlock(ctx context.Context, height int64) error {
	if a.blockHandler == nil {
		return ErrAdapterNotInitialized
	}
	block, err := a.fetchBlockWithRetry(ctx, height)
	if err != nil {
		return err
	}
	transactions, receipts := a.fetchTransactionsAndReceipts(ctx, block, height)
	return a.createBlockBatchWithRetry(ctx, block, height, transactions, receipts)
}

// fetchBlockWithRetry fetches a block by number. When the block is not yet
// available on chain the error is returned as-is (it matches errors.IsErrNotFound)
// so the caller can decide to retry. Other RPC errors are retried up to
// maxRPCRetries times with linear backoff.
func (a *EVMAdapter) fetchBlockWithRetry(ctx context.Context, blockNum int64) (*types.Block, error) {
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		block, err := a.client.GetBlockByNumber(ctx, big.NewInt(blockNum))
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
func (a *EVMAdapter) fetchTransactionsAndReceipts(ctx context.Context, block *types.Block, blockNum int64) ([]*types.Transaction, []*types.TransactionReceipt) {
	transactions := make([]*types.Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}

	batchReceipts, batchErr := a.client.GetBlockReceipts(ctx, big.NewInt(blockNum))
	if batchErr == nil {
		return transactions, batchReceipts
	}

	if ctx.Err() == nil {
		logger.Sugar.Debugf("Block %d: eth_getBlockReceipts not available, falling back to individual fetches: %v", blockNum, batchErr)
	}

	receipts := make([]*types.TransactionReceipt, len(block.Transactions))
	var wg sync.WaitGroup
	sem := make(chan struct{}, a.receiptWorkers)

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
			receipt, err := a.client.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				if ctx.Err() == nil {
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

// createBlockBatchWithRetry persists the block batch via the BlockHandler. When
// the block already exists a signing job is enqueued and nil is returned.
// Transaction conflicts are retried up to maxRPCRetries times.
func (a *EVMAdapter) createBlockBatchWithRetry(ctx context.Context, block *types.Block, blockNum int64, transactions []*types.Transaction, receipts []*types.TransactionReceipt) error {
	for attempt := range maxRPCRetries {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, err := a.blockHandler.CreateBlockBatch(ctx, block, transactions, receipts)
		if err == nil {
			return nil
		}

		if errors.IsErrAlreadyExists(err) {
			select {
			case a.signingChan <- signingJob{
				blockNum:     blockNum,
				blockHash:    block.Hash,
				block:        block,
				transactions: transactions,
				receipts:     receipts,
			}:
			default:
				logger.Sugar.Warnf("Block %d: signing queue full, skipping block signature", blockNum)
			}
			return nil
		}

		if errors.IsErrTransactionConflict(err) && attempt < maxRPCRetries-1 {
			logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, maxRPCRetries)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * transactionConflictRetryBaseDelay):
			}
			continue
		}

		return fmt.Errorf("failed to create block batch: %w", err)
	}
	return nil
}

// FetchHighestBlockNumber implements Chain.
func (a *EVMAdapter) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	n, err := a.client.GetLatestBlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get latest block number: %w", err)
	}
	return n.Int64(), nil
}

// GetHighestStoredBlockNumber implements Chain.
func (a *EVMAdapter) GetHighestStoredBlockNumber(ctx context.Context) (int64, error) {
	if a.blockHandler == nil {
		return 0, ErrAdapterNotInitialized
	}
	return a.blockHandler.GetHighestBlockNumber(ctx)
}

// GetLowestStoredBlockNumber implements Chain.
func (a *EVMAdapter) GetLowestStoredBlockNumber(ctx context.Context) (int64, error) {
	if a.blockHandler == nil {
		return 0, ErrAdapterNotInitialized
	}
	return a.blockHandler.GetLowestBlockNumber(ctx)
}

// GetDocIDsByBlockRange implements Chain. It returns document IDs for every
// collection whose block-number field falls in [from, to] inclusive.
// SnapshotSignature is excluded; BlockSignature is included.
//
// Delegates to BlockHandler, which uses chunked _geq/_leq GraphQL range
// filters on the indexer's local DefraDB instance — the same approach the
// snapshotter uses (pkg/snapshot/kv_snapshot.go).
func (a *EVMAdapter) GetDocIDsByBlockRange(ctx context.Context, from, to int64) (map[string][]string, error) {
	if a.blockHandler == nil {
		return nil, ErrAdapterNotInitialized
	}
	return a.blockHandler.GetDocIDsByBlockRange(ctx, from, to)
}

// GetSchema implements Chain.
func (a *EVMAdapter) GetSchema() (string, error) {
	return schema.GetSchemaForChain(chainPrefixFromConfig(a.cfg))
}

// GetCollections implements Chain.
func (a *EVMAdapter) GetCollections() []string {
	return a.collections.AllCollections()
}

// SetDocIDTracker wires a docID tracker to the underlying BlockHandler. This is
// a concrete (non-interface) method: the indexer uses it to glue the adapter to
// the indexing queue.
func (a *EVMAdapter) SetDocIDTracker(tracker defra.DocIDTrackerInterface) {
	if a.blockHandler != nil {
		a.blockHandler.SetDocIDTracker(tracker)
	}
}
