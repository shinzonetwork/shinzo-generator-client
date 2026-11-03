package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	solana "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// solanaSecKey is the signing key type used by the seeded identity.
var solanaSecKey = crypto.KeyTypeSecp256k1 //nolint:gochecknoglobals

// Solana snapshot round-trip: slots stored through the real production path
// (solana converter -> LinkStamper -> BlockHandler) are snapshotted, re-
// imported into a fresh node, and remain fully queryable. This pins the
// chain-agnostic snapshot machinery against the solana collection model,
// whose Block/derived collections and rename mappings differ from the EVM
// shape the default tests use.

// solanaSnapshotFixture bundles the pieces the round-trip needs: the solana
// converter, its default collection list, and a production block handler
// for seeding. The DefraDB node is returned separately so each test drives
// its own store explicitly.
type solanaSnapshotFixture struct {
	cols    []string
	conv    *solana.Converter
	handler *defra.BlockHandler
}

func newSolanaSnapshotFixture(t *testing.T) (*solanaSnapshotFixture, *testutils.TestDefraDB) {
	t.Helper()

	conv := solana.NewConverter(nil)
	sdl, err := conv.GetSchema()
	require.NoError(t, err)
	td := testutils.SetupTestDefraDBWithSchema(t, sdl)

	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	return &solanaSnapshotFixture{
		cols:    solana.DefaultCollections(),
		conv:    conv,
		handler: handler,
	}, td
}

// solanaTestBlock builds one full-fidelity block for slot: block + one
// transaction (one outer and one inner instruction) + a token balance pair
// + two rewards. Field shapes mirror the solana package's own fixtures; the
// strings need no base58 realism because the converter treats them as
// opaque values. The positional ordinals live only in the document schema:
// the converter derives them from loop positions.
func solanaTestBlock(slot int64) *solana.Block {
	blockTime := slot
	stackHeight := uint16(2)
	computeUnits := uint64(15000)
	return &solana.Block{
		Slot:              uint64(slot),
		Blockhash:         fmt.Sprintf("blockhash-%d", slot),
		PreviousBlockhash: fmt.Sprintf("prev-%d", slot),
		BlockTime:         &blockTime,
		Transactions: []solana.Transaction{{
			Signature:            fmt.Sprintf("sig-%d-0", slot),
			Version:              "legacy",
			Fee:                  5000,
			ComputeUnitsConsumed: &computeUnits,
			LogMessages:          []string{"Program log: entrypoint exited"},
			PreBalances:          []uint64{100000, 200000},
			PostBalances:         []uint64{95000, 200000},
			RecentBlockhash:      fmt.Sprintf("recent-%d", slot),
			AccountKeys:          []string{fmt.Sprintf("acct0-%d", slot), fmt.Sprintf("acct1-%d", slot)},
			Instructions: []solana.Instruction{{
				ProgramID: "prog-vote",
				Accounts:  []string{fmt.Sprintf("acct0-%d", slot)},
				Data:      fmt.Sprintf("data-%d", slot),
			}},
			InnerInstructions: []solana.InnerInstructionGroup{{
				Index: 0,
				Instructions: []solana.Instruction{{
					ProgramID:   "prog-swap",
					Accounts:    []string{fmt.Sprintf("acct1-%d", slot)},
					Data:        fmt.Sprintf("idata-%d", slot),
					StackHeight: &stackHeight,
				}},
			}},
			PreTokenBalances: []solana.TokenBalance{{
				AccountIndex: 1, Mint: fmt.Sprintf("mint-%d", slot), Owner: fmt.Sprintf("owner-%d", slot),
				ProgramID: "tokenprog", Amount: "100",
			}},
			PostTokenBalances: []solana.TokenBalance{{
				AccountIndex: 1, Mint: fmt.Sprintf("mint-%d", slot), Owner: fmt.Sprintf("owner-%d", slot),
				ProgramID: "tokenprog", Amount: "150",
			}},
		}},
		Rewards: []solana.Reward{
			{Pubkey: fmt.Sprintf("fee-%d", slot), Lamports: -25000, PostBalance: 10000000, RewardType: "Fee"},
			{
				Pubkey: fmt.Sprintf("vote-%d", slot), Lamports: 405, PostBalance: 500000000,
				RewardType: "Voting", Commission: &[]uint8{5}[0],
			},
		},
	}
}

// assertSolanaSignatureDocCount requires the given signature collection to
// hold exactly `want` documents. It goes through QuerySnapshotSignatures:
// the production read path.
func assertSolanaSignatureDocCount(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, cols []string, suffix string, want int) {
	t.Helper()

	colName := solanaCollectionForSuffix(cols, suffix)
	require.NotEmpty(t, colName, "collection %s must resolve", suffix)

	sigs, err := QuerySnapshotSignatures(ctx, td.Node, colName)
	require.NoError(t, err)
	assert.Len(t, sigs, want)
}

// seedSolanaBlocks stores slots [start, end] through the production path
// and requires each block to have signed.
func (f *solanaSnapshotFixture) seedSolanaBlocks(ctx context.Context, t *testing.T, start, end int64) {
	t.Helper()
	for slot := start; slot <= end; slot++ {
		result, err := f.conv.Convert(ctx, solanaTestBlock(slot))
		require.NoError(t, err)
		creation, err := f.handler.Store(ctx, result)
		require.NoError(t, err, "store slot %d", slot)
		require.NotEmpty(t, creation.BlockSignatureID, "slot %d must sign", slot)
	}
}

// solanaCollectionForSuffix resolves a collection name by suffix so a
// rename of the default prefix fails loudly instead of silently breaking
// the tests.
func solanaCollectionForSuffix(cols []string, suffix string) string {
	for _, col := range cols {
		if len(col) > len(suffix) && col[len(col)-len(suffix):] == suffix {
			return col
		}
	}
	return ""
}

// TestSolanaSnapshotRoundtrip stores solana slots, snapshots an aligned
// range, verifies the on-disk header carries the slot window, confirms the
// SnapshotSignature doc landed, re-imports into a fresh DefraDB node, and
// confirms every collection is preserved and queryable.
func TestSolanaSnapshotRoundtrip(t *testing.T) {
	fixture, td := newSolanaSnapshotFixture(t)
	ctx, _ := newIdentityCtx(t, solanaSecKey)

	start, end := int64(100), int64(104)
	fixture.seedSolanaBlocks(ctx, t, start, end)

	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 5}, td.Node, fixture.conv)
	s.ctx = ctx

	require.NoError(t, s.createSnapshot(ctx, start, end))

	file := fmt.Sprintf("snapshot_%d_%d.kvsnap.gz", start, end)
	header := readSnapshotHeader(t, filepath.Join(s.cfg.Dir, file))
	assert.Equal(t, start, header.StartBlock)
	assert.Equal(t, end, header.EndBlock)
	assert.Len(t, header.BlockSigMerkleRoots, int(end-start+1),
		"each stored block's signature merkle root must be embedded in the header")

	assertSolanaSignatureDocCount(ctx, t, td, fixture.cols, "SnapshotSignature", 1)

	// Restore into a fresh node sharing the solana schema.
	_, td2 := newSolanaSnapshotFixture(t)
	restoreCtx, _ := newIdentityCtx(t, solanaSecKey)
	result, err := ImportKV(restoreCtx, td2.Node, filepath.Join(s.cfg.Dir, file))
	require.NoError(t, err)
	assert.Equal(t, start, result.StartBlock)
	assert.Equal(t, end, result.EndBlock)
	require.NoError(t, RebuildAllIndexes(restoreCtx, td2.Node, fixture.cols))

	conv2 := solana.NewConverter(nil)
	lowest, err := conv2.GetLowestStoredBlockNumber(restoreCtx, td2.Node)
	require.NoError(t, err)
	assert.Equal(t, start, lowest)
	highest, err := conv2.GetHighestStoredBlockNumber(restoreCtx, td2.Node)
	require.NoError(t, err)
	assert.Equal(t, end, highest)

	// Every collection holds one document per slot for the range
	// (instructions and rewards: two per slot, outer+inner and fee+vote).
	docIDs, err := conv2.GetDocIDsByBlockRange(restoreCtx, td2.Node, start, end)
	require.NoError(t, err)
	perSlot := int(end - start + 1)
	assert.Len(t, docIDs[solanaCollectionForSuffix(fixture.cols, "Block")], perSlot)
	assert.Len(t, docIDs[solanaCollectionForSuffix(fixture.cols, "Transaction")], perSlot)
	assert.Len(t, docIDs[solanaCollectionForSuffix(fixture.cols, "Instruction")], 2*perSlot)
	assert.Len(t, docIDs[solanaCollectionForSuffix(fixture.cols, "TokenBalanceChange")], perSlot)
	assert.Len(t, docIDs[solanaCollectionForSuffix(fixture.cols, "Reward")], 2*perSlot)
	assert.NotEmpty(t, docIDs[solanaCollectionForSuffix(fixture.cols, "BlockSignature")],
		"restored store must retain the block signature docs the snapshot signed over")
}

// TestSolanaSnapshotSkipsPurgedRanges pins the prune/snapshot interaction:
// when slots below an aligned-range boundary are gone, the snapshotter must
// cover only the retained range, never fabricate a snapshot over missing
// slots. The pruner itself is exercised in pkg/pruner's solana round-trip;
// what is pinned here is the snapshotter's behaviour given the resulting DB
// state, so the purge is applied directly.
func TestSolanaSnapshotSkipsPurgedRanges(t *testing.T) {
	fixture, td := newSolanaSnapshotFixture(t)
	ctx, _ := newIdentityCtx(t, solanaSecKey)

	start := int64(200)
	end := start + 14 // 200..214, three aligned 5-slot ranges
	fixture.seedSolanaBlocks(ctx, t, start, end)

	for slot := start; slot < start+10; slot++ {
		docIDs, err := fixture.conv.GetDocIDsByBlockRange(ctx, td.Node, slot, slot)
		require.NoError(t, err)
		require.NotEmpty(t, docIDs)
		for _, col := range fixture.cols {
			ids := docIDs[col]
			if len(ids) == 0 {
				continue
			}
			purgeIDs := make([]client.DocID, 0, len(ids))
			for _, id := range ids {
				docID, err := client.NewDocIDFromString(id)
				require.NoError(t, err)
				purgeIDs = append(purgeIDs, docID)
			}
			colObj, err := td.Node.DB.GetCollectionByName(ctx, col)
			require.NoError(t, err)
			require.NoError(t, colObj.PurgeByDocIDs(ctx, purgeIDs, true))
		}
	}

	lowest, err := fixture.conv.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	require.Equal(t, start+10, lowest, "purge must remove the ten oldest slots")

	s := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 5}, td.Node, fixture.conv)
	s.ctx = ctx
	require.NoError(t, s.checkAndSnapshot(ctx))

	files, err := filepath.Glob(filepath.Join(s.cfg.Dir, "snapshot_*.kvsnap.gz"))
	require.NoError(t, err)
	require.Len(t, files, 1, "exactly the retained aligned range must be snapshotted")
	assert.Contains(t, files[0], fmt.Sprintf("snapshot_%d_%d", start+10, end))
}
