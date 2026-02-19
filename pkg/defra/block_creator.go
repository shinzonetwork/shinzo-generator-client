package defra

import (
	"context"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

// BlockCreator defines the interface for creating blocks in DefraDB.
// Both BlockHandler (Go embedded) and FFIBlockHandler (Rust FFI) implement this.
type BlockCreator interface {
	CreateBlockBatch(ctx context.Context, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) (string, error)
	CreateBatchSignatureForExistingBlock(ctx context.Context, blockNumber int64, blockHash string, block *types.Block, transactions []*types.Transaction, receipts []*types.TransactionReceipt) (string, error)
	GetHighestBlockNumber(ctx context.Context) (int64, error)
}
