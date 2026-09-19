package solana

import (
	"context"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewConverter_NilConfig(t *testing.T) {
	t.Parallel()
	c := NewConverter(nil)
	assert.Equal(t, "Solana__Mainnet", c.collections.Prefix())
}

func TestNewConverter_DefaultPrefix(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())
	assert.Equal(t, "Solana__Mainnet", c.collections.Prefix())
}

func TestNewConverter_CustomPrefix(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{Name: "Solana", Network: "Devnet"}
	c := NewConverter(cfg)
	assert.Equal(t, "Solana__Devnet", c.collections.Prefix())
}

// ---------------------------------------------------------------------------
// GetSchema / GetCollections / Collections / SignatureCollection
// ---------------------------------------------------------------------------

func TestConverter_GetSchema_Delegates(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "Solana__Mainnet__Block")
	assert.Contains(t, sdl, "Solana__Mainnet__Transaction")
	assert.Contains(t, sdl, "Solana__Mainnet__Instruction")
	assert.Contains(t, sdl, "Solana__Mainnet__TokenBalanceChange")
	assert.Contains(t, sdl, "Solana__Mainnet__Reward")
	assert.NotContains(t, sdl, "Ethereum__Mainnet", "EVM SDL must not leak into Solana schema")
}

func TestConverter_GetSchema_CustomPrefix(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{Name: "Solana", Network: "Devnet"}
	c := NewConverter(cfg)

	sdl, err := c.GetSchema()
	require.NoError(t, err)
	assert.Contains(t, sdl, "Solana__Devnet__Block")
	assert.NotContains(t, sdl, DefaultCollectionPrefix, "embedded prefix must be fully swapped")
}

func TestConverter_GetCollections(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	cols := c.GetCollections()
	assert.Len(t, cols, 7)
	assert.Equal(t, CollectionBlock, cols[0])
	assert.Equal(t, CollectionBlockSignature, cols[1])
	assert.Equal(t, CollectionSnapshotSignature, cols[2])
	assert.Equal(t, CollectionTransaction, cols[3])
	assert.Equal(t, CollectionInstruction, cols[4])
	assert.Equal(t, CollectionTokenBalanceChange, cols[5])
	assert.Equal(t, CollectionReward, cols[6])
}

func TestConverter_Collections(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	cols := c.Collections()
	require.NotNil(t, cols)
	assert.Equal(t, "Solana__Mainnet", cols.Prefix())

	name, err := cols.GetCollection(chains.TypeBlock)
	require.NoError(t, err)
	assert.Equal(t, CollectionBlock, name)
}

func TestConverter_SignatureCollection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		chain   config.ChainConfig
		wantCol string
	}{
		{
			name:    "DefaultPrefix",
			chain:   config.ChainConfig{Name: "Solana", Network: "Mainnet"},
			wantCol: "Solana__Mainnet__BlockSignature",
		},
		{
			name:    "CustomPrefix",
			chain:   config.ChainConfig{Name: "Solana", Network: "Devnet"},
			wantCol: "Solana__Devnet__BlockSignature",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			cfg.Chain = tc.chain
			c := NewConverter(cfg)
			assert.Equal(t, tc.wantCol, c.SignatureCollection())
		})
	}
}

// ---------------------------------------------------------------------------
// Convert — error paths
// ---------------------------------------------------------------------------

func TestConvert_TypeMismatch(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	result, err := c.Convert(context.Background(), "not a block")
	assert.Error(t, err)
	assert.Nil(t, result.Groups)
	assert.Empty(t, result.SignatureCollection)
}

func TestConvert_NilBlock(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	result, err := c.Convert(context.Background(), (*Block)(nil))
	assert.Error(t, err)
	assert.Nil(t, result.Groups)
}

// ---------------------------------------------------------------------------
// Convert — group structure
// ---------------------------------------------------------------------------

func TestConvert_EmptyBlock(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	block := fakeBlock(42)
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)

	// Only the block group: no transactions, instructions, balances, rewards.
	require.Len(t, result.Groups, 1)
	assert.Equal(t, CollectionBlock, result.Groups[0].Collection)
	require.Len(t, result.Groups[0].Docs, 1)
	assert.Equal(t, int64(42), result.Groups[0].Docs[0][SlotFieldName])
	assert.Equal(t, CollectionBlockSignature, result.SignatureCollection)
	assert.Equal(t, BlockhashFieldName, result.Groups[0].BlockHashField,
		"block group should have BlockHashField set to BlockhashFieldName")
	assert.NotNil(t, result.LinkStamper)
}

func TestConvert_FullBundle_GroupOrder(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(397234560)
	successTx := fakeTransaction(slot, 0, "tx-success")
	failedTx := fakeFailedTransaction(slot, 1, "tx-failed")
	block := fakeBlockWithTxs(slot, successTx, failedTx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	assert.Equal(t, CollectionBlockSignature, result.SignatureCollection)

	// Fixed order: block → transaction → outer instructions → inner
	// instructions → token balance changes → rewards. Both instruction
	// groups share the Instruction collection.
	require.Len(t, result.Groups, 6)
	assert.Equal(t, CollectionBlock, result.Groups[0].Collection)
	assert.Equal(t, CollectionTransaction, result.Groups[1].Collection)
	assert.Equal(t, CollectionInstruction, result.Groups[2].Collection)
	assert.Equal(t, CollectionInstruction, result.Groups[3].Collection)
	assert.Equal(t, CollectionTokenBalanceChange, result.Groups[4].Collection)
	assert.Equal(t, CollectionReward, result.Groups[5].Collection)

	// Counts: 2 txs, 2 outer (one per tx), 2 inner (one group per tx),
	// 2 token balance changes (one per tx), 2 rewards.
	assert.Len(t, result.Groups[1].Docs, 2)
	assert.Len(t, result.Groups[2].Docs, 2)
	assert.Len(t, result.Groups[3].Docs, 2)
	assert.Len(t, result.Groups[4].Docs, 2)
	assert.Len(t, result.Groups[5].Docs, 2)
}

// TestConvert_GroupFieldContract pins the DocumentGroup contract the generic
// BlockHandler relies on: every group carries the non-empty BlockNumField
// "slot", and the block group (Groups[0]) carries a non-empty BlockHashField.
// Non-block groups keep BlockHashField empty (see chains.DocumentGroup).
func TestConvert_GroupFieldContract(t *testing.T) {
	t.Parallel()

	slot := uint64(7)
	block := fakeBlockWithTxs(slot, fakeTransaction(slot, 0, "cf"))

	c := NewConverter(testConfig())
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.NotEmpty(t, result.Groups)

	for i, g := range result.Groups {
		assert.Equal(t, SlotFieldName, g.BlockNumField,
			"group %d (%s) must use slot as BlockNumField", i, g.Collection)
		assert.Positive(t, g.BatchSize, "group %d must carry a batch size", i)
	}
	assert.Equal(t, BlockhashFieldName, result.Groups[0].BlockHashField,
		"block group must populate BlockHashField")
	for _, g := range result.Groups[1:] {
		assert.Empty(t, g.BlockHashField,
			"non-block groups should have empty BlockHashField")
	}
}

// TestConvert_NoLinkFieldsInDocs pins that Convert never sets link fields —
// BlockHandler.Store resolves them via the LinkStamper.
func TestConvert_NoLinkFieldsInDocs(t *testing.T) {
	t.Parallel()

	slot := uint64(16)
	block := fakeBlockWithTxs(slot, fakeTransaction(slot, 0, "links"))
	c := NewConverter(testConfig())

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)

	for i, g := range result.Groups {
		for j, doc := range g.Docs {
			for _, link := range []string{"_blockID", "_transactionID", "_parentInstructionID"} {
				_, has := doc[link]
				assert.False(t, has, "group %d doc %d must not carry %s", i, j, link)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Convert — field mapping
// ---------------------------------------------------------------------------

func TestConvert_BlockData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(100)
	block := fakeBlock(slot)
	block.BlockHeight = new(uint64(99))
	block.BlockTime = new(int64(1774267845))

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[0].Docs, 1)

	doc := result.Groups[0].Docs[0]
	assert.Equal(t, int64(100), doc[SlotFieldName])
	assert.Equal(t, block.Blockhash, doc[BlockhashFieldName])
	assert.Equal(t, int64(99), doc[ParentSlotFieldName])
	assert.Equal(t, int64(99), doc[BlockHeightFieldName])
	assert.Equal(t, int64(1774267845), doc[BlockTimeFieldName])
	assert.Equal(t, 0, doc[TransactionCountFieldName])
	assert.Equal(t, 0, doc[RewardCountFieldName])
	assert.NotContains(t, doc, "blockVersion", "blockVersion was dropped from the schema (no RPC source)")
}

func TestConvert_BlockData_Nullables(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	block := fakeBlock(101)
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)

	doc := result.Groups[0].Docs[0]
	assert.Nil(t, doc[BlockHeightFieldName], "nil blockHeight must stay nil")
	assert.Nil(t, doc[BlockTimeFieldName], "nil blockTime must stay nil")
}

func TestConvert_TransactionData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(200)
	successTx := fakeTransaction(slot, 0, "txmap-success")
	failedTx := fakeFailedTransaction(slot, 1, "txmap-failed")
	block := fakeBlockWithTxs(slot, successTx, failedTx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[1].Docs, 2)

	success := result.Groups[1].Docs[0]
	assert.Equal(t, successTx.Signature, success[SignatureFieldName])
	assert.Equal(t, int64(slot), success[SlotFieldName])
	assert.Equal(t, 0, success[TransactionIndexFieldName])
	assert.Equal(t, "legacy", success[VersionFieldName])
	assert.Equal(t, false, success[FailedFieldName])
	assert.Empty(t, success[ErrFieldName])
	assert.Equal(t, int64(5000), success[FeeFieldName])
	assert.Nil(t, success[ComputeUnitsConsumedFieldName])
	assert.Equal(t, successTx.LogMessages, success[LogMessagesFieldName])
	assert.Equal(t, []int64{100000, 200000}, success[PreBalancesFieldName])
	assert.Equal(t, []int64{95000, 200000}, success[PostBalancesFieldName])
	assert.Equal(t, successTx.RecentBlockhash, success[RecentBlockhashFieldName])
	assert.Equal(t, successTx.AccountKeys, success[AccountKeysFieldName])

	failed := result.Groups[1].Docs[1]
	assert.Equal(t, true, failed[FailedFieldName])
	assert.Equal(t, failedTx.Err, failed[ErrFieldName])
}

func TestConvert_InstructionData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(300)
	tx := fakeTransaction(slot, 0, "instr")
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[2].Docs, 1)
	require.Len(t, result.Groups[3].Docs, 1)

	outer := result.Groups[2].Docs[0]
	assert.Equal(t, tx.Instructions[0].ProgramID, outer[ProgramIDFieldName])
	assert.Equal(t, tx.Instructions[0].Accounts, outer[AccountsFieldName])
	assert.Equal(t, tx.Instructions[0].Data, outer[DataFieldName])
	assert.Equal(t, 0, outer[InstructionIndexFieldName])
	assert.Equal(t, 0, outer[InnerIndexFieldName])
	assert.Equal(t, int64(slot), outer[SlotFieldName])
	_, hasStackHeight := outer[StackHeightFieldName]
	assert.False(t, hasStackHeight, "outer docs must omit the stackHeight key (stamper inner/outer discriminator)")

	inner := result.Groups[3].Docs[0]
	assert.Equal(t, tx.InnerInstructions[0].Instructions[0].ProgramID, inner[ProgramIDFieldName])
	assert.Equal(t, 0, inner[InstructionIndexFieldName], "inner instructionIndex is the parent outer's index")
	assert.Equal(t, 0, inner[InnerIndexFieldName])
	assert.Equal(t, int64(2), inner[StackHeightFieldName], "inner docs always carry the stackHeight key")
}

// TestConvert_InstructionStackHeightInvariant pins the key-presence
// invariant the LinkStamper relies on to route outer vs inner docs when both
// groups share the Instruction collection.
func TestConvert_InstructionStackHeightInvariant(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(310)
	tx := fakeTransaction(slot, 0, "invariant")
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)

	outerGroup, innerGroup := result.Groups[2], result.Groups[3]
	require.NotEmpty(t, outerGroup.Docs)
	require.NotEmpty(t, innerGroup.Docs)

	for _, doc := range outerGroup.Docs {
		_, has := doc[StackHeightFieldName]
		assert.False(t, has, "outer doc must never carry the stackHeight key")
	}
	for _, doc := range innerGroup.Docs {
		_, has := doc[StackHeightFieldName]
		assert.True(t, has, "inner doc must always carry the stackHeight key")
	}
}

func TestConvert_InnerInstructionWireStackHeightAbsent(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(320)
	tx := fakeTransaction(slot, 0, "nostack")
	tx.InnerInstructions[0].Instructions[0].StackHeight = nil // node omitted it on the wire
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[3].Docs, 1)

	inner := result.Groups[3].Docs[0]
	assert.Equal(t, nil, inner[StackHeightFieldName],
		"key must still be present with nil value so the stamper sees an inner doc")
	_, has := inner[StackHeightFieldName]
	assert.True(t, has)
}

func TestConvert_TokenBalanceChangeData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(400)
	tx := fakeTransaction(slot, 0, "tbc")
	// ALT-loaded keys extend the committed list: accountIndex 2 resolves
	// against the writable-loaded address, not the static keys.
	tx.LoadedAddressesWritable = []string{fakePubkey("loaded-w")}
	tx.PreTokenBalances[0].AccountIndex = 2
	tx.PostTokenBalances[0].AccountIndex = 2
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[4].Docs, 1)

	doc := result.Groups[4].Docs[0]
	assert.Equal(t, tx.PostTokenBalances[0].Mint, doc[MintFieldName])
	assert.Equal(t, tx.PostTokenBalances[0].Owner, doc[OwnerFieldName])
	assert.Equal(t, tx.LoadedAddressesWritable[0], doc[TokenAccountFieldName],
		"tokenAccount must resolve against static + ALT-loaded keys")
	assert.Equal(t, "100", doc[PreAmountFieldName])
	assert.Equal(t, "150", doc[PostAmountFieldName])
	assert.Equal(t, tx.PostTokenBalances[0].ProgramID, doc[ProgramIDFieldName])
	assert.Equal(t, int64(slot), doc[SlotFieldName])
}

// TestConvert_TokenBalanceChange_OneSided pins the union semantics: an
// account present on only one side (opened or closed mid-transaction) gets
// nil on the absent side, and its identity fields come from the whole
// present entry — post when one exists, pre otherwise (no per-field mixing).
func TestConvert_TokenBalanceChange_OneSided(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(410)
	tx := fakeTransaction(slot, 0, "onesided")
	closedMint := fakePubkey("mint-closed")

	// Account 1: pre only (token account closed mid-transaction).
	// Account 0: pre + post (ordinary transfer), post without programId to
	// pin the whole-identity semantics (post wins, no mixing).
	tx.PreTokenBalances = []TokenBalance{
		{AccountIndex: 1, Mint: closedMint, Owner: fakePubkey("owner-closed"), Amount: "77"},
		{AccountIndex: 0, Mint: fakePubkey("mint-open"), Owner: fakePubkey("owner-open"), Amount: "0"},
	}
	tx.PostTokenBalances = []TokenBalance{
		{AccountIndex: 0, Mint: fakePubkey("mint-open"), Owner: fakePubkey("owner-open"), Amount: "55"},
	}
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[4].Docs, 2)

	// Sorted by account index: 0 (transferred) then 1 (closed, pre-only).
	opened := result.Groups[4].Docs[0]
	assert.Equal(t, "0", opened[PreAmountFieldName])
	assert.Equal(t, "55", opened[PostAmountFieldName])
	assert.Equal(t, fakePubkey("owner-open"), opened[OwnerFieldName],
		"post entry's owner wins")
	assert.Empty(t, opened[ProgramIDFieldName],
		"post entry omits programId, so the doc stores empty (no pre-side mixing)")

	closed := result.Groups[4].Docs[1]
	assert.Equal(t, closedMint, closed[MintFieldName],
		"identity falls back to the pre entry when the post side is absent")
	assert.Equal(t, "77", closed[PreAmountFieldName])
	assert.Nil(t, closed[PostAmountFieldName], "absent side must be nil")
}

func TestConvert_RewardData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(500)
	block := fakeBlockWithTxs(slot, fakeTransaction(slot, 0, "reward"))

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[5].Docs, 2)

	fee := result.Groups[5].Docs[0]
	assert.Equal(t, block.Rewards[0].Pubkey, fee[PubkeyFieldName])
	assert.Equal(t, int64(-25000), fee[LamportsFieldName], "lamports are signed")
	assert.Equal(t, "987654321", fee[PostBalanceFieldName], "postBalance stored as string (uint64)")
	assert.Equal(t, "Fee", fee[RewardTypeFieldName])
	assert.Nil(t, fee[CommissionFieldName], "fee rewards carry no commission")

	vote := result.Groups[5].Docs[1]
	assert.Equal(t, int64(405), vote[LamportsFieldName])
	assert.Equal(t, "Voting", vote[RewardTypeFieldName])
}

func TestConvert_RewardCommission(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(510)
	block := fakeBlockWithTxs(slot, fakeTransaction(slot, 0, "commission"))

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[5].Docs, 2)

	vote := result.Groups[5].Docs[1]
	assert.Equal(t, "5", vote[CommissionFieldName], "commission stored as string when present")
	assert.Equal(t, "Voting", vote[RewardTypeFieldName])
}

// ---------------------------------------------------------------------------
// Progress queries (require real DefraDB)
// ---------------------------------------------------------------------------

func setupSolanaDefraDB(t *testing.T) *testutils.TestDefraDB {
	t.Helper()
	c := NewConverter(testConfig())
	sdl, err := c.GetSchema()
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchema(t, sdl)
}

func TestGetHighestStoredBlockNumber_NoBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	td := setupSolanaDefraDB(t)
	_, err := c.GetHighestStoredBlockNumber(context.Background(), td.Node)
	assert.Error(t, err)
}

func TestGetLowestStoredBlockNumber_NoBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	td := setupSolanaDefraDB(t)
	_, err := c.GetLowestStoredBlockNumber(context.Background(), td.Node)
	assert.Error(t, err)
}

func TestGetDocIDsByBlockRange_Empty(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	td := setupSolanaDefraDB(t)
	result, err := c.GetDocIDsByBlockRange(context.Background(), td.Node, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestGetStoredBlockNumbers_WithBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())
	td := setupSolanaDefraDB(t)
	ctx := context.Background()

	for _, slot := range []int64{100, 101, 102} {
		storeTestBlockDoc(ctx, t, td, c, slot)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	highest, err := c.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(102), highest)
}

// TestGetLowestStoredBlockNumber_AfterPurge mirrors the EVM regression test:
// after purging the lowest blocks, the ASC lowest-slot query must return the
// next stored block without reporting corruption from purge residue.
func TestGetLowestStoredBlockNumber_AfterPurge(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())
	td := setupSolanaDefraDB(t)
	ctx := context.Background()

	for _, slot := range []int64{100, 101, 102, 103, 104} {
		storeTestBlockDoc(ctx, t, td, c, slot)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	docIDs, err := c.queryCollectionDocIDs(ctx, td.Node, c.collections.Block, SlotFieldName, 100, 101)
	require.NoError(t, err)
	require.Len(t, docIDs, 2)

	col, err := td.Node.DB.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	purgeIDs := make([]client.DocID, 0, len(docIDs))
	for _, id := range docIDs {
		docID, err := client.NewDocIDFromString(id)
		require.NoError(t, err)
		purgeIDs = append(purgeIDs, docID)
	}
	require.NoError(t, col.PurgeByDocIDs(ctx, purgeIDs, true))

	lowest, err = c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(102), lowest)
}

func TestGetDocIDsByBlockRange_WithBlocks(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())
	td := setupSolanaDefraDB(t)
	ctx := context.Background()

	storeTestBlockDoc(ctx, t, td, c, 100)
	storeTestBlockDoc(ctx, t, td, c, 105)

	result, err := c.GetDocIDsByBlockRange(ctx, td.Node, 100, 102)
	require.NoError(t, err)
	require.Len(t, result, 1, "only the Block collection has docs in range")
	assert.Len(t, result[c.collections.Block], 1)

	result, err = c.GetDocIDsByBlockRange(ctx, td.Node, 100, 105)
	require.NoError(t, err)
	assert.Len(t, result[c.collections.Block], 2)
}

// storeTestBlockDoc writes a single block document through the same write
// path used in production (buildBlockData → NewDocFromMap → AddDocument).
func storeTestBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, slot int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	doc, err := client.NewDocFromMap(ctx, c.buildBlockData(fakeBlock(uint64(slot))), col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}
