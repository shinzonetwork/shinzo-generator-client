// Package chain defines the chain-agnostic abstraction used by the indexer,
// pruner, snapshotter, and schema applier to interact with a specific chain
// backend (e.g. EVM-based chains) without depending on concrete RPC or DefraDB
// implementations.
//
// The Chain interface centralises every operation the rest of the system needs:
//   - fetching and persisting a block at a given height,
//   - querying the on-chain tip and the stored block-number bounds,
//   - listing the docIDs that belong to a block range (used by pruning and
//     snapshotting),
//   - and exposing the schema/collection names for the configured chain.
//
// Step 1 of the chain-abstraction refactor introduces the interface and a
// minimal EVMAdapter implementation. The adapter is additive dead-code at this
// stage: nothing in the hot path is wired to it yet (Steps 2-5 perform the
// rewiring).
package chain

import (
	"context"
	stderrors "errors"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/sourcenetwork/defradb/node"
)

// ErrAdapterNotInitialized is returned by methods that require a DefraDB-backed
// BlockHandler when the adapter has not been initialised via Init.
//
// Per the lifecycle contract only GetSchema and GetCollections are valid before
// Init is called; every other method enforces this guard.
var ErrAdapterNotInitialized = stderrors.New("chain adapter not initialized: Init must be called before use")

// Chain is the chain-agnostic interface implemented by each chain backend
// adapter.
//
// All methods are safe to call concurrently after Init has completed.
type Chain interface {
	// FetchAndStoreBlock fetches the block and its receipts at the given height
	// from the chain and persists them via the configured BlockHandler.
	//
	// It returns the DefraDB docID of the newly created block document. When the
	// block already exists in DefraDB a background signing job is enqueued, the
	// method returns nil, and the returned docID is empty (no new document was
	// created).
	//
	// When the block is not yet available on chain the returned error matches
	// errors.IsErrNotFound so the caller (e.g. the block processor) can retry.
	FetchAndStoreBlock(ctx context.Context, height int64) (string, error)

	// FetchHighestBlockNumber returns the latest block number known to the
	// chain (the on-chain tip), querying the RPC endpoint directly.
	FetchHighestBlockNumber(ctx context.Context) (int64, error)

	// GetHighestStoredBlockNumber returns the highest block number currently
	// persisted in DefraDB.
	GetHighestStoredBlockNumber(ctx context.Context) (int64, error)

	// GetLowestStoredBlockNumber returns the lowest block number currently
	// persisted in DefraDB. Useful for pruning windows.
	GetLowestStoredBlockNumber(ctx context.Context) (int64, error)

	// GetDocIDsByBlockRange returns the DefraDB docIDs for every relevant
	// collection whose block-number field falls within [from, to] inclusive.
	//
	// The returned map is keyed by collection name. SnapshotSignature docIDs are
	// intentionally excluded (the snapshotter owns those); BlockSignature
	// docIDs are included.
	GetDocIDsByBlockRange(ctx context.Context, from, to int64) (map[string][]string, error)

	// GetSchema returns the GraphQL SDL for the configured chain, with
	// collection names adapted to the chain's prefix.
	GetSchema() (string, error)

	// GetCollections returns the names of all collections for the configured
	// chain in dependency-safe order.
	GetCollections() []string
}

// Adapter extends Chain with the lifecycle methods needed only by the indexer
// orchestrator. Pruner, snapshotter, and the concurrent block processor depend
// solely on Chain — their interfaces stay narrow per the Interface Segregation
// Principle.
//
// *EVMAdapter satisfies Adapter via structural typing.
type Adapter interface {
	Chain

	// Init connects to the chain RPC endpoint and prepares the adapter for
	// block processing. Must be called exactly once after DefraDB is started
	// and before any fetch/store methods are invoked.
	Init(ctx context.Context, node *node.Node) error

	// Close releases the adapter's RPC connection and background resources.
	// Safe to call multiple times.
	Close() error

	// SetDocIDTracker wires the pruner's DocIDTracker so the adapter can
	// enqueue docIDs for pruning as blocks are stored.
	SetDocIDTracker(tracker defra.DocIDTrackerInterface)
}
