package evm

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/sourcenetwork/defradb/node"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/chain"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/rpc"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

type EvmChain struct {
	cfg            *config.Config
	receiptWorkers int
	collections    *constants.CollectionNames
	blockHandler   *defra.BlockHandler
	client         *rpc.EthereumClient
}

var _ chain.Chain = &EvmChain{}

func NewEvmChain(cfg *config.Config, collections *constants.CollectionNames) *EvmChain {
	return &EvmChain{
		cfg:         cfg,
		collections: collections,
	}
}

// Init is used to initialize chain adapter. defraNode should be saved for further use.
func (e *EvmChain) Init(ctx context.Context, defraNode *node.Node) error {
	var err error
	e.blockHandler, err = defra.NewBlockHandler(defraNode, e.cfg.Indexer.MaxDocsPerTxn, e.collections)
	if err != nil {
		return fmt.Errorf("failed to create block handler: %v", err)
	}
	logger.Sugar.Infof("Using direct DB access for embedded DefraDB (maxDocsPerTxn=%d)", e.cfg.Indexer.MaxDocsPerTxn)

	// Connect to Ethereum client early — needed for latest block query and indexing
	e.client, err = rpc.NewEthereumClient(e.cfg.Geth.NodeURL, e.cfg.Geth.WsURL, e.cfg.Geth.APIKey)
	if err != nil {
		logCtx := errors.LogContext(err)
		logger.Sugar.With(logCtx).Fatalf("Failed to connect to Ethereum client: %v", err)
	}
	panic("not implemented") // TODO: Implement
}

func (e *EvmChain) Close() {
	e.client.Close()
}

// SaveSchema should create chain-specific schema in DefraDB.
func (e *EvmChain) SaveSchema(ctx context.Context) error {
	panic("not implemented") // TODO: Implement
}

const (
	// Default configuration constants - can be made configurable via config file
	DefaultBlocksToIndexAtOnce = 10
	DefaultRetryAttempts       = 3
	DefaultSchemaWaitTimeout   = 15 * time.Second
	DefaultDefraReadyTimeout   = 30 * time.Second
	// DefaultBlockOffset is the number of blocks behind the latest block to process
	// This prevents "transaction type not supported" errors from very recent blocks
	DefaultBlockOffset = 3
)

// FetchAndStoreBlock retrieves block data at given height using RPC and stores it in DefraDB.
func (e *EvmChain) FetchAndStoreBlock(ctx context.Context, height int64) *chain.BlockResult {
	return e.fetchAndProcessBlock(ctx, height)
}

// FetchHighestBlockNumber retreives height of latest block using RPC.
func (e *EvmChain) FetchHighestBlockNumber(ctx context.Context) (int64, error) {
	latest, err := e.client.GetLatestBlockNumber(ctx)
	return latest.Int64(), err
}

// GetHighestStoredBlockNumber returns the maximum block height found in DefraDB.
func (e *EvmChain) GetHighestStoredBlockNumber(ctx context.Context) (int64, error) {
	return e.blockHandler.GetHighestBlockNumber(ctx)
}

// GetLowestStoredBlockNumber returns the minimum block height found in DefraDB.
func (e *EvmChain) GetLowestStoredBlockNumber(ctx context.Context) (int64, error) {
	panic("not implemented") // TODO: Implement
}

// fetchAndProcessBlock fetches a block with receipts and processes it
func (e *EvmChain) fetchAndProcessBlock(ctx context.Context, blockNum int64) *chain.BlockResult {
	result := &chain.BlockResult{BlockNum: blockNum}

	var block *types.Block
	var err error

	// For "not found" (block not on chain yet), retry indefinitely with backoff.
	// For other RPC errors, retry up to 3 times.
	otherErrors := 0
	for {
		if ctx.Err() != nil {
			result.Error = ctx.Err()
			return result
		}

		block, err = e.client.GetBlockByNumber(ctx, big.NewInt(blockNum))
		if err == nil {
			break
		}
		if errors.IsErrNotFound(err) {
			logger.Sugar.Infof("Block %d not available yet, waiting...", blockNum)
			select {
			case <-ctx.Done():
				result.Error = ctx.Err()
				return result
			case <-time.After(3 * time.Second):
			}
			continue
		}
		otherErrors++
		if otherErrors >= 3 {
			break
		}
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			return result
		case <-time.After(time.Duration(otherErrors) * 500 * time.Millisecond):
		}
	}
	if err != nil {
		result.Error = fmt.Errorf("failed to fetch block: %w", err)
		return result
	}

	transactions := make([]*types.Transaction, len(block.Transactions))
	for i := range block.Transactions {
		transactions[i] = &block.Transactions[i]
	}

	var validReceipts []*types.TransactionReceipt
	batchReceipts, batchErr := e.client.GetBlockReceipts(ctx, big.NewInt(blockNum))
	if batchErr == nil {
		validReceipts = batchReceipts
	} else {
		if ctx.Err() == nil {
			logger.Sugar.Debugf("Block %d: eth_getBlockReceipts not available, falling back to individual fetches: %v", blockNum, batchErr)
		}

		receipts := make([]*types.TransactionReceipt, len(block.Transactions))
		var wg sync.WaitGroup
		sem := make(chan struct{}, e.receiptWorkers)

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

				receipt, err := e.client.GetTransactionReceipt(ctx, txHash)
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

		validReceipts = make([]*types.TransactionReceipt, 0, len(receipts))
		for _, r := range receipts {
			if r != nil {
				validReceipts = append(validReceipts, r)
			}
		}
	}

	const maxRetries = 3
	for attempt := range maxRetries {
		if ctx.Err() != nil {
			result.Error = ctx.Err()
			return result
		}

		blockID, err := e.blockHandler.CreateBlockBatch(ctx, block, transactions, validReceipts)
		if err == nil {
			result.Success = true
			result.BlockID = blockID
			return result
		}

		if errors.IsErrAlreadyExists(err) {
			// TODO(tzdybal): signing
			/*
				// Block exists via P2P — enqueue signing in background so indexing isn't blocked
				select {
				case p.signingChan <- signingJob{
					blockNum:     blockNum,
					blockHash:    block.Hash,
					block:        block,
					transactions: transactions,
					receipts:     validReceipts,
				}:
				default:
					logger.Sugar.Warnf("Block %d: signing queue full, skipping block signature", blockNum)
				}
				result.Success = true
				result.BlockID = "existing"
				return result
			*/
		}

		if errors.IsErrTransactionConflict(err) {
			if attempt < maxRetries-1 {
				logger.Sugar.Infof("Block %d transaction conflict, retrying (attempt %d/%d)", blockNum, attempt+1, maxRetries)
				select {
				case <-ctx.Done():
					result.Error = ctx.Err()
					return result
				case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
				}
				continue
			}
		}

		result.Error = fmt.Errorf("failed to create block batch: %w", err)
		return result
	}

	return result
}
