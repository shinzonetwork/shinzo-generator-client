package defra

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/pruner"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// assertCollectionEmptyForRange asserts that a collection holds zero
// default-query documents for [from, to]. Default queries exclude
// soft-deleted docs, so this doubles as the deleted-docs-hidden assertion.
func assertCollectionEmptyForRange(ctx context.Context, t *testing.T, handler *BlockHandler, colName, field string, from, to int64) {
	t.Helper()
	ids, err := handler.queryCollectionDocIDs(ctx, colName, field, from, to)
	require.NoError(t, err)
	assert.Empty(t, ids, "collection %s must hold no queryable docs for [%d, %d]", colName, from, to)
}

// TestReorgRoller_RollbackBlocks_RemovesAllFiveCollections stores three
// blocks (the middle one full: block + transaction + log + access-list
// entry + signature), rolls back the middle block, and asserts that all
// five collections answer zero docs for it while the untouched neighbors
// remain queryable. Re-indexing at the rolled-back height then stores
// cleanly — impossible without the rollback, which would fail with
// "already exists" — and a second rollback over the re-indexed height
// clears it again.
//
// The re-index uses different content than the rolled-back block: DefraDB
// docIDs are content-derived and a soft-deleted docID is never re-addable
// ("a document with the given ID has been deleted"), which is why the
// rollback flow re-indexes the canonical replacement, not the orphan —
// every document of the replacement carries the canonical blockHash (and,
// for access-list entries, the canonical transaction docID link), so no
// content-identical collision can occur.
func TestReorgRoller_RollbackBlocks_RemovesAllFiveCollections(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	converter := evm.NewConverter(nil)
	roller, err := NewReorgRoller(td.Node, converter)
	require.NoError(t, err)

	cols := evm.NewCollectionNames("Ethereum__Mainnet")
	blockCol := extractCollection(cols, chains.TypeBlock)
	txCol := extractCollection(cols, chains.TypeTransaction)
	logCol := extractCollection(cols, chains.TypeLog)
	aleCol := extractCollection(cols, chains.TypeAccessListEntry)
	sigCol := extractCollection(cols, chains.TypeBlockSignature)

	ctx := ctxWithIdentity(t)

	_, err = handler.Store(ctx, buildGroups(t, mockBlock("0x834"), nil, nil)) // 2100
	require.NoError(t, err)

	orphanBlock := mockBlock("0x835") // 2101
	orphanTxHash := "0xrollback000000000000000000000000000000000000000000000000000001"
	orphanTx := mockTransaction(orphanTxHash, "2101")
	orphanTx.AccessList = []evm.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000070",
			StorageKeys: []string{"0x0000000000000000000000000000000000000000000000000000000000000009"},
		},
	}
	orphanReceipt := mockReceipt(orphanTxHash, "0x835")
	_, err = handler.Store(ctx, buildGroups(t, orphanBlock, []*evm.Transaction{orphanTx}, []*evm.TransactionReceipt{orphanReceipt}))
	require.NoError(t, err)

	_, err = handler.Store(ctx, buildGroups(t, mockBlock("0x836"), nil, nil)) // 2102
	require.NoError(t, err)

	pre, err := converter.GetDocIDsByBlockRange(ctx, td.Node, 2100, 2102)
	require.NoError(t, err)
	for _, colName := range []string{blockCol, txCol, logCol, aleCol, sigCol} {
		require.Contains(t, pre, colName, "collection %s must hold docs for the full range before rollback", colName)
	}

	rollbackCtx := rollbackCtxWithOrphan(ctx, 2101)
	require.NoError(t, roller.RollbackBlocks(rollbackCtx, 2101, 2101))

	assertCollectionEmptyForRange(ctx, t, handler, blockCol, constants.NumberFieldName, 2101, 2101)
	assertCollectionEmptyForRange(ctx, t, handler, txCol, constants.BlockNumberFieldName, 2101, 2101)
	assertCollectionEmptyForRange(ctx, t, handler, logCol, constants.BlockNumberFieldName, 2101, 2101)
	assertCollectionEmptyForRange(ctx, t, handler, aleCol, constants.BlockNumberFieldName, 2101, 2101)
	assertCollectionEmptyForRange(ctx, t, handler, sigCol, constants.BlockNumberFieldName, 2101, 2101)

	rolledBack, err := converter.GetDocIDsByBlockRange(ctx, td.Node, 2101, 2101)
	require.NoError(t, err)
	assert.Empty(t, rolledBack, "no collection may answer a default query over the rolled-back range")

	survivors, err := converter.GetDocIDsByBlockRange(ctx, td.Node, 2100, 2102)
	require.NoError(t, err)
	assert.Contains(t, survivors, blockCol, "the untouched neighbor blocks must remain queryable")
	assert.NotContains(t, survivors, txCol, "the rolled-back block's tx docs must stay hidden")

	canonicalBlock := mockBlock("0x835")
	canonicalBlock.Hash = deterministicHash("canonical-2101")
	canonicalTxHash := "0xcanonical000000000000000000000000000000000000000000000000000001"
	canonicalTx := mockTransaction(canonicalTxHash, "2101")
	canonicalTx.AccessList = []evm.AccessListEntry{
		{
			Address:     "0x0000000000000000000000000000000000000080",
			StorageKeys: []string{"0x000000000000000000000000000000000000000000000000000000000000000a"},
		},
	}
	canonicalReceipt := mockReceipt(canonicalTxHash, "0x835")

	_, err = handler.Store(ctx, buildGroups(t, canonicalBlock, []*evm.Transaction{canonicalTx}, []*evm.TransactionReceipt{canonicalReceipt}))
	require.NoError(t, err, "re-indexing a rolled-back height stores cleanly")

	reindexed, err := converter.GetDocIDsByBlockRange(ctx, td.Node, 2101, 2101)
	require.NoError(t, err)
	for _, colName := range []string{blockCol, txCol, logCol, aleCol, sigCol} {
		assert.Contains(t, reindexed, colName, "the re-indexed block must be queryable in %s", colName)
	}

	require.NoError(t, roller.RollbackBlocks(rollbackCtx, 2101, 2101),
		"the rollback is repeatable: re-running it over the re-indexed height soft-deletes again without error")

	assertCollectionEmptyForRange(ctx, t, handler, blockCol, constants.NumberFieldName, 2101, 2101)
}

// TestReorgRoller_GetStoredBlockHash covers the stored-hash query: a stored
// height returns its hash, an unstored height returns empty, and a
// rolled-back height reports as not stored (default queries exclude
// soft-deleted docs).
func TestReorgRoller_GetStoredBlockHash(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	converter := evm.NewConverter(nil)
	roller, err := NewReorgRoller(td.Node, converter)
	require.NoError(t, err)

	ctx := context.Background()

	block := mockBlock("0x3E8") // 1000
	_, err = handler.Store(ctx, buildGroups(t, block, nil, nil))
	require.NoError(t, err)

	stored, err := roller.GetStoredBlockHash(ctx, 1000)
	require.NoError(t, err)
	assert.Equal(t, block.Hash, stored)

	missing, err := roller.GetStoredBlockHash(ctx, 1001)
	require.NoError(t, err)
	assert.Empty(t, missing, "a height with no stored block reports an empty hash")

	require.NoError(t, roller.RollbackBlocks(rollbackCtxWithOrphan(ctx, 1000), 1000, 1000))

	purged, err := roller.GetStoredBlockHash(ctx, 1000)
	require.NoError(t, err)
	assert.Empty(t, purged, "a rolled-back height reports as not stored")
}

// TestNewReorgRoller_RejectsNilArgs covers the constructor validation.
func TestNewReorgRoller_RejectsNilArgs(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	_, err := NewReorgRoller(nil, evm.NewConverter(nil))
	require.Error(t, err)

	_, err = NewReorgRoller(td.Node, nil)
	require.Error(t, err)
}

// rollbackCtxWithOrphan marks every given height as a verified orphan in
// the rollback context: the new chain carries a hash no stored content can
// produce, so the verify-by-hash contract deletes exactly those heights
// (the direct roller tests provide the verification the processor would).
func rollbackCtxWithOrphan(ctx context.Context, heights ...int64) context.Context {
	chainHashes := make(map[int64]string, len(heights))
	for _, h := range heights {
		chainHashes[h] = deterministicHash(fmt.Sprintf("rollback-orphan-%d", h))
	}
	return WithRollbackChainHashes(ctx, chainHashes)
}

// queueRollbackTracker mirrors the indexer's queue wiring for tests: TrackBlock
// feeds the real pruner queue; the rollback retains every entry by design.
type queueRollbackTracker struct {
	queue *pruner.IndexerQueue
}

func (t *queueRollbackTracker) TrackBlock(_ context.Context, blockNumber int64, result *BlockCreationResult) error {
	return t.queue.TrackBlockDocIDs(blockNumber, result.BlockID, result.OtherDocIDs, result.BlockSignatureID)
}

// TestReorgRoller_RollbackBlocks_RestoringDeletedContentIsRefused pins the
// DefraDB constraint the reorg design is built around (dvncan's ask): docIDs
// are content-derived and a soft-deleted docID is never re-addable, so Storing
// byte-identical block content after a RollbackBlocks over it must be refused,
// leaving every collection still hidden for the rolled-back height. This is
// why the reorg flow must never store a block before the continuity check
// (check-before-store) and must delete only orphans by height and hash: a
// swept-then-re-fetched identical block would be silently lost.
func TestReorgRoller_RollbackBlocks_RestoringDeletedContentIsRefused(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	converter := evm.NewConverter(nil)
	roller, err := NewReorgRoller(td.Node, converter)
	require.NoError(t, err)

	cols := evm.NewCollectionNames("Ethereum__Mainnet")
	blockCol := extractCollection(cols, chains.TypeBlock)
	txCol := extractCollection(cols, chains.TypeTransaction)
	sigCol := extractCollection(cols, chains.TypeBlockSignature)

	ctx := ctxWithIdentity(t)

	// A full block so the rollback covers block, transaction and signature
	// docs: every one of them becomes a soft-deleted docID.
	block := mockBlock("0x835") // 2101
	txHash := "0xrst000000000000000000000000000000000000000000000000000000000001"
	tx := mockTransaction(txHash, "2101")
	receipt := mockReceipt(txHash, "0x835")
	groups := buildGroups(t, block, []*evm.Transaction{tx}, []*evm.TransactionReceipt{receipt})

	_, err = handler.Store(ctx, groups)
	require.NoError(t, err)

	require.NoError(t, roller.RollbackBlocks(rollbackCtxWithOrphan(ctx, 2101), 2101, 2101))

	// Re-store the byte-identical content: the docIDs were content-derived
	// and are now soft-deleted, so DefraDB refuses the re-creation instead
	// of resurrecting the deleted documents.
	_, err = handler.Store(ctx, buildGroups(t, block, []*evm.Transaction{tx}, []*evm.TransactionReceipt{receipt}))
	require.Error(t, err, "re-adding a soft-deleted content-derived dockey must be refused")
	assert.Contains(t, strings.ToLower(err.Error()), "deleted",
		"the refusal must be the deleted-dockey conflict, not a fresh store")

	// The refusal must not have resurrected anything: the rolled-back
	// height stays hidden in every collection.
	assertCollectionEmptyForRange(ctx, t, handler, blockCol, constants.NumberFieldName, 2101, 2101)
	assertCollectionEmptyForRange(ctx, t, handler, txCol, constants.BlockNumberFieldName, 2101, 2101)
	assertCollectionEmptyForRange(ctx, t, handler, sigCol, constants.BlockNumberFieldName, 2101, 2101)
}

// TestReorgRoller_RollbackBlocks_RetainsQueueEntries pins the defect-7 fix
// (prune-queue retention): after a rollback soft-deletes the orphan docs,
// their prune-queue entries must SURVIVE, because the pruner is the only
// mechanism that hard-purges them locally. Today the roller drops the
// rolled-back range from the tracker via RemoveBlockRange, so the soft-deleted
// docs lose their only purge driver and leak in storage forever.
func TestReorgRoller_RollbackBlocks_RetainsQueueEntries(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	converter := evm.NewConverter(nil)
	roller, err := NewReorgRoller(td.Node, converter)
	require.NoError(t, err)

	queue := pruner.NewIndexerQueue()
	tracker := &queueRollbackTracker{queue: queue}
	handler.SetDocIDTracker(tracker)
	roller.SetDocIDTracker(tracker)

	ctx := ctxWithIdentity(t)

	block := mockBlock("0x835") // 2101
	txHash := "0xqqq000000000000000000000000000000000000000000000000000000000001"
	tx := mockTransaction(txHash, "2101")
	receipt := mockReceipt(txHash, "0x835")

	_, err = handler.Store(ctx, buildGroups(t, block, []*evm.Transaction{tx}, []*evm.TransactionReceipt{receipt}))
	require.NoError(t, err)
	require.Equal(t, 1, queue.Len(), "the stored orphan must be tracked in the prune queue")

	require.NoError(t, roller.RollbackBlocks(rollbackCtxWithOrphan(ctx, 2101), 2101, 2101))

	// The docs are now soft-deleted (invisible to queries) and their queue
	// entries must be retained: the pruner later hard-purges via exactly
	// these docIDs, and the cleanup path that used to drop them is gone.
	assert.Equal(t, 1, queue.Len(),
		"the rollback must retain the prune-queue entry so the soft-deleted orphan docs stay purgeable")
}
