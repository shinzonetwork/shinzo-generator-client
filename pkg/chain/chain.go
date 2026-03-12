package chain

import (
	"context"

	"github.com/sourcenetwork/defradb/node"
)

// Chain defines chain-specific operations needed by generalized indexing logic.
type Chain interface {
	// Init is used to initialize chain adapter. defraNode should be saved for further use.
	Init(ctx context.Context, defraNode *node.Node) error
	Close()

	// SaveSchema should create chain-specific schema in DefraDB.
	SaveSchema(ctx context.Context) error

	// FetchAndStoreBlock retrieves block data at given height using RPC and stores it in DefraDB.
	FetchAndStoreBlock(ctx context.Context, height int64) *BlockResult

	// FetchHighestBlockNumber retreives height of latest block using RPC.
	FetchHighestBlockNumber(ctx context.Context) (int64, error)

	// GetHighestStoredBlockNumber returns the maximum block height found in DefraDB.
	GetHighestStoredBlockNumber(ctx context.Context) (int64, error)

	// GetLowestStoredBlockNumber returns the minimum block height found in DefraDB.
	GetLowestStoredBlockNumber(ctx context.Context) (int64, error)
}

// BlockResult holds the result of processing a block
type BlockResult struct {
	BlockNum int64
	BlockID  string
	Success  bool
	Error    error
}
