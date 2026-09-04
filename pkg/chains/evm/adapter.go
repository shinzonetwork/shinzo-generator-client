package evm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/rpc"
	"github.com/sourcenetwork/defradb/node"
)

// Adapter-specific constants. Fetch-related constants (rpcErrorRetryBaseDelay,
// maxRPCRetries) live in fetcher.go; maxRPCRetries is shared via the same package.
const (
	// transactionConflictRetryBaseDelay is the base delay for retrying
	// transaction conflicts.
	transactionConflictRetryBaseDelay = 50 * time.Millisecond

	// signingQueueSize is the buffer size for the background block signing
	// channel.
	signingQueueSize = 64

	// defaultChainName is the fallback chain name when config is empty.
	defaultChainName = "Ethereum"

	// defaultNetwork is the fallback network name when config is empty.
	defaultNetwork = "Mainnet"
)

// signingJob holds the data needed to sign an existing block in the background.
type signingJob struct {
	blockNum  int64
	blockHash string
	result    chains.ConversionResult
}

// Adapter is a Chain implementation backed by an EVM JSON-RPC endpoint and a
// DefraDB BlockHandler.
//
// Lifecycle:
//   - NewAdapter dials the RPC endpoint and builds the collection names from
//     the configured chain prefix.
//   - Init creates the BlockHandler from a running DefraDB node, sets the batch
//     sizes and starts the background signing goroutine.
//   - Close cancels the internal context, drains the signing goroutine and
//     closes the RPC client.
//
// Before Init only GetSchema and GetCollections are valid; every other method
// returns ErrAdapterNotInitialized.
type Adapter struct {
	*Fetcher

	blockHandler *defra.BlockHandler
	converter    *Converter
	collections  *CollectionNames
	signingChan  chan signingJob
	node         *node.Node
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	cfg          *config.Config
}

// Compile-time guarantee that Adapter implements Chain.
var _ chains.Chain = (*Adapter)(nil)

// NewAdapter dials the configured RPC endpoint and returns an Adapter
// ready to be initialised. Init must still be called before DefraDB-backed
// methods can be used.
func NewAdapter(cfg *config.Config) (*Adapter, error) {
	if cfg == nil {
		return nil, errors.NewConfigurationError("chain", "NewAdapter", "config is nil", "", nil)
	}
	client, err := rpc.NewEthereumClient(cfg.Geth.NodeURL, cfg.Geth.WsURL, cfg.Geth.APIKey, cfg.Geth.APIKeyType)
	if err != nil {
		return nil, fmt.Errorf("create ethereum client: %w", err)
	}
	return newAdapter(cfg, client), nil
}

// newAdapter builds an Adapter with an injected rpcClient. It is used by
// the exported NewAdapter (production) and by tests (with a fake client).
func newAdapter(cfg *config.Config, client rpcClient) *Adapter {
	prefix := chainPrefixFromConfig(cfg)
	collections := NewCollectionNames(prefix)
	converter := NewConverter(cfg)

	receiptWorkers := 0
	if cfg != nil {
		receiptWorkers = cfg.Indexer.ReceiptWorkers
	}
	if receiptWorkers <= 0 {
		receiptWorkers = 16 //nolint:mnd
	}

	return &Adapter{
		Fetcher:     NewFetcher(client, receiptWorkers),
		converter:   converter,
		collections: collections,
		signingChan: make(chan signingJob, signingQueueSize),
		cfg:         cfg,
	}
}

// chainPrefixFromConfig derives the collection prefix (e.g. "Ethereum__Mainnet")
// from the chain config, applying the same defaults as the existing indexer.
func chainPrefixFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return DefaultCollectionPrefix
	}
	name := cfg.Chain.Name
	network := cfg.Chain.Network
	if name == "" {
		name = defaultChainName
	}
	if network == "" {
		network = defaultNetwork
	}
	return fmt.Sprintf("%s__%s", name, network)
}

// Init wires the adapter to a running DefraDB node. After Init returns the
// BlockHandler-backed methods are usable and the background signing goroutine
// is running.
func (a *Adapter) Init(ctx context.Context, defraNode *node.Node) error {
	if a.cfg == nil {
		return errors.NewConfigurationError("chain", "Init", "config is nil", "", nil)
	}
	blockHandler, err := defra.NewBlockHandler(defraNode, a.cfg.Indexer.MaxDocsPerTxn)
	if err != nil {
		return fmt.Errorf("create block handler: %w", err)
	}

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
func (a *Adapter) signBlocks(ctx context.Context) {
	defer a.wg.Done()
	for job := range a.signingChan {
		if ctx.Err() != nil {
			continue
		}
		if _, err := a.blockHandler.SignExisting(
			ctx, job.result, job.blockHash, job.blockNum,
		); err != nil {
			logger.Sugar.Warnf("Block %d: failed to create block signature for existing block: %v", job.blockNum, err)
		}
	}
}

// Close cancels the internal lifecycle, drains the signing goroutine and
// closes the RPC client. It is idempotent.
func (a *Adapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.signingChan != nil {
		close(a.signingChan)
		a.signingChan = nil
	}
	a.wg.Wait()
	return a.Fetcher.Close()
}

// FetchAndStoreBlock implements Chain.
func (a *Adapter) FetchAndStoreBlock(ctx context.Context, height int64) (string, error) {
	if a.blockHandler == nil {
		return "", chains.ErrAdapterNotInitialized
	}
	raw, err := a.FetchBlock(ctx, height)
	if err != nil {
		return "", err
	}
	result, err := a.converter.Convert(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("convert block: %w", err)
	}
	blockHash := extractBlockHashFromGroups(result.Groups, a.collections)
	return a.storeBlockWithRetry(ctx, result, blockHash, height)
}

// storeBlockWithRetry persists document groups via BlockHandler.Store. When
// the block already exists a signing job is enqueued and nil is returned.
// Transaction conflicts are retried up to maxRPCRetries times.
func (a *Adapter) storeBlockWithRetry(ctx context.Context, result chains.ConversionResult, blockHash string, blockNum int64) (string, error) {
	for attempt := range maxRPCRetries {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		res, err := a.blockHandler.Store(ctx, result)
		if err == nil {
			return res.BlockID, nil
		}

		if errors.IsErrAlreadyExists(err) {
			select {
			case a.signingChan <- signingJob{
				blockNum:  blockNum,
				blockHash: blockHash,
				result:    result,
			}:
			default:
				logger.Sugar.Warnf("Block %d: signing queue full, skipping block signature", blockNum)
			}
			return "", nil
		}

		if errors.IsErrTransactionConflict(err) && attempt < maxRPCRetries-1 {
			logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, maxRPCRetries)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt+1) * transactionConflictRetryBaseDelay):
			}
			continue
		}

		return "", fmt.Errorf("failed to store block: %w", err)
	}
	return "", nil
}

// extractBlockHashFromGroups finds the block group and returns its "hash" value.
func extractBlockHashFromGroups(groups []chains.DocumentGroup, cols *CollectionNames) string {
	for _, g := range groups {
		if g.Collection == cols.Block && len(g.Docs) > 0 {
			if hash, ok := g.Docs[0][constants.HashKeyValue].(string); ok {
				return hash
			}
		}
	}
	return ""
}

// GetHighestStoredBlockNumber implements Chain.
func (a *Adapter) GetHighestStoredBlockNumber(ctx context.Context) (int64, error) {
	if a.blockHandler == nil {
		return 0, chains.ErrAdapterNotInitialized
	}
	return a.converter.GetHighestStoredBlockNumber(ctx, a.node)
}

// GetLowestStoredBlockNumber implements Chain.
func (a *Adapter) GetLowestStoredBlockNumber(ctx context.Context) (int64, error) {
	if a.blockHandler == nil {
		return 0, chains.ErrAdapterNotInitialized
	}
	return a.converter.GetLowestStoredBlockNumber(ctx, a.node)
}

// GetDocIDsByBlockRange implements Chain. It returns document IDs for every
// collection whose block-number field falls in [from, to] inclusive.
// SnapshotSignature is excluded; BlockSignature is included.
//
// Delegates to BlockHandler, which uses chunked _geq/_leq GraphQL range
// filters on the indexer's local DefraDB instance — the same approach the
// snapshotter uses (pkg/snapshot/kv_snapshot.go).
func (a *Adapter) GetDocIDsByBlockRange(ctx context.Context, from, to int64) (map[string][]string, error) {
	if a.blockHandler == nil {
		return nil, chains.ErrAdapterNotInitialized
	}
	return a.converter.GetDocIDsByBlockRange(ctx, a.node, from, to)
}

// GetSchema implements Chain.
func (a *Adapter) GetSchema() (string, error) {
	return a.converter.GetSchema()
}

// GetCollections implements Chain.
func (a *Adapter) GetCollections() []string {
	return a.converter.GetCollections()
}

// Collections returns the Collections interface for the configured chain.
// This is a concrete (non-interface) method used by the indexer's lifecycleAdapter
// interface — it provides typed access to collection names, prefix, and schema metadata.
func (a *Adapter) Collections() chains.Collections {
	return a.collections
}

// SetDocIDTracker wires a docID tracker to the underlying BlockHandler. This is
// a concrete (non-interface) method: the indexer uses it to glue the adapter to
// the indexing queue.
func (a *Adapter) SetDocIDTracker(tracker defra.DocIDTrackerInterface) {
	if a.blockHandler != nil {
		a.blockHandler.SetDocIDTracker(tracker)
	}
}

func init() {
	chains.RegisterAdapter("evm", func(cfg *config.Config) (chains.Adapter, error) {
		return NewAdapter(cfg)
	})
}
