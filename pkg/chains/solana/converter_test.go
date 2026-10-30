package solana

import (
	"context"
	"maps"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
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

func TestNewConverter_EmptyChainFields(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Chain = config.ChainConfig{}
	c := NewConverter(cfg)
	assert.Equal(t, "Solana__Mainnet", c.collections.Prefix(), "empty name/network fall back to defaults")
}

// ---------------------------------------------------------------------------
// GetSchema / GetCollections / Collections
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
	assert.NotContains(t, sdl, "Ethereum__", "EVM SDL must not leak into Solana schema")
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
	assert.Equal(t, DefaultCollectionBlock, cols[0])
	assert.Equal(t, DefaultCollectionBlockSignature, cols[1])
	assert.Equal(t, DefaultCollectionSnapshotSignature, cols[2])
	assert.Equal(t, DefaultCollectionTransaction, cols[3])
	assert.Equal(t, DefaultCollectionInstruction, cols[4])
	assert.Equal(t, DefaultCollectionTokenBalanceChange, cols[5])
	assert.Equal(t, DefaultCollectionReward, cols[6])
}

func TestConverter_Collections(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	cols := c.Collections()
	require.NotNil(t, cols)
	assert.Equal(t, "Solana__Mainnet", cols.Prefix())
	assert.Equal(t, DefaultCollectionBlock, cols.BlockCollection())
	assert.Equal(t, DefaultCollectionBlockSignature, cols.BlockSignatureCollection())
	assert.Equal(t, DefaultCollectionSnapshotSignature, cols.SnapshotSignatureCollection())
}

// ---------------------------------------------------------------------------
// Convert: error paths
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
// Convert: group structure
// ---------------------------------------------------------------------------

func TestConvert_EmptyBlock(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	block := fakeBlock(42)
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)

	// Only the block group: no transactions, instructions, balances, rewards.
	require.Len(t, result.Groups, 1)
	assert.Equal(t, DefaultCollectionBlock, result.Groups[0].Collection)
	require.Len(t, result.Groups[0].Docs, 1)
	assert.Equal(t, int64(42), result.Groups[0].Docs[0][constants.NumberFieldName])
	assert.Equal(t, DefaultCollectionBlockSignature, result.SignatureCollection)
	assert.Equal(t, constants.HashFieldName, result.Groups[0].BlockHashField,
		"block group should have BlockHashField set to the host contract hash field")
	assert.NotNil(t, result.LinkStamper)
}

func TestConvert_FullBundle_GroupOrder(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(397234560)
	successTx := fakeTransaction("tx-success")
	failedTx := fakeFailedTransaction("tx-failed")
	block := fakeBlockWithTxs(slot, successTx, failedTx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	assert.Equal(t, DefaultCollectionBlockSignature, result.SignatureCollection)

	// Fixed order: block -> transaction -> outer instructions -> inner
	// instructions -> token balance changes -> rewards. Both instruction
	// groups share the Instruction collection.
	require.Len(t, result.Groups, 6)
	assert.Equal(t, DefaultCollectionBlock, result.Groups[0].Collection)
	assert.Equal(t, DefaultCollectionTransaction, result.Groups[1].Collection)
	assert.Equal(t, DefaultCollectionInstruction, result.Groups[2].Collection)
	assert.Equal(t, DefaultCollectionInstruction, result.Groups[3].Collection)
	assert.Equal(t, DefaultCollectionTokenBalanceChange, result.Groups[4].Collection)
	assert.Equal(t, DefaultCollectionReward, result.Groups[5].Collection)

	// Counts: 2 txs, 2 outer (one per tx), 2 inner (one group per tx),
	// 2 token balance changes (one per tx), 2 rewards.
	assert.Len(t, result.Groups[1].Docs, 2)
	assert.Len(t, result.Groups[2].Docs, 2)
	assert.Len(t, result.Groups[3].Docs, 2)
	assert.Len(t, result.Groups[4].Docs, 2)
	assert.Len(t, result.Groups[5].Docs, 2)
}

// TestConvert_GroupFieldContract pins the DocumentGroup contract the generic
// BlockHandler relies on: the block group (Groups[0]) carries the host
// contract number/hash fields plus the parent hash, every derived group
// carries blockNumber, and non-block groups keep BlockHashField/ParentHashField
// empty (see chains.DocumentGroup).
func TestConvert_GroupFieldContract(t *testing.T) {
	t.Parallel()

	slot := uint64(7)
	block := fakeBlockWithTxs(slot, fakeTransaction("cf"))

	c := NewConverter(testConfig())
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.NotEmpty(t, result.Groups)

	head := result.Groups[0]
	assert.Equal(t, constants.NumberFieldName, head.BlockNumField)
	assert.Equal(t, constants.HashFieldName, head.BlockHashField)
	assert.Equal(t, ParentHashFieldName, head.ParentHashField,
		"the generic continuity check relies on the block group's ParentHashField")
	for _, g := range result.Groups[1:] {
		assert.Equal(t, constants.BlockNumberFieldName, g.BlockNumField,
			"derived group %s must use blockNumber as BlockNumField", g.Collection)
		assert.Empty(t, g.BlockHashField)
		assert.Empty(t, g.ParentHashField)
	}
	for i, g := range result.Groups {
		assert.Positive(t, g.BatchSize, "group %d must carry a batch size", i)
	}
}

// TestConvert_BatchSizes pins the knob mapping: transaction rides
// max_tx_docs_per_batch, instruction and token-balance-change groups share
// the log knob, block and reward groups ride the default.
func TestConvert_BatchSizes(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Indexer.MaxTxDocsPerBatch = 21
	cfg.Indexer.MaxLogDocsPerBatch = 33
	cfg.Indexer.MaxDocsPerTxn = 1000

	block := fakeBlockWithTxs(8, fakeTransaction("batches"))
	c := NewConverter(cfg)
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups, 6)

	assert.Equal(t, 1000, result.Groups[0].BatchSize, "block group rides the default batch")
	assert.Equal(t, 21, result.Groups[1].BatchSize, "transaction group rides max_tx_docs_per_batch")
	assert.Equal(t, 33, result.Groups[2].BatchSize, "outer instructions share the log knob")
	assert.Equal(t, 33, result.Groups[3].BatchSize, "inner instructions share the log knob")
	assert.Equal(t, 33, result.Groups[4].BatchSize, "token balance changes share the log knob")
	assert.Equal(t, 1000, result.Groups[5].BatchSize, "rewards ride the default batch")
}

// TestConvert_NoLinkFieldsInDocs pins that Convert never sets link fields:
// BlockHandler.Store resolves them via the LinkStamper.
func TestConvert_NoLinkFieldsInDocs(t *testing.T) {
	t.Parallel()

	slot := uint64(16)
	block := fakeBlockWithTxs(slot, fakeTransaction("links"))
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
// Convert: field mapping
// ---------------------------------------------------------------------------

func TestConvert_BlockData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(100)
	block := fakeBlock(slot)
	blockTime := int64(1774267845)
	block.BlockTime = &blockTime

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[0].Docs, 1)

	doc := result.Groups[0].Docs[0]
	assert.Equal(t, int64(100), doc[constants.NumberFieldName], "number holds the slot value")
	assert.Equal(t, block.Blockhash, doc[constants.HashFieldName], "hash holds the blockhash")
	assert.Equal(t, block.PreviousBlockhash, doc[ParentHashFieldName])
	assert.Equal(t, int64(1774267845), doc[BlockTimeFieldName])
	assert.Equal(t, 0, doc[TransactionCountFieldName])
	assert.Equal(t, 0, doc[RewardCountFieldName])
	assert.NotContains(t, doc, "slot", "spike field slot was renamed to number")
	assert.NotContains(t, doc, "blockhash", "spike field blockhash was renamed to hash")
}

func TestConvert_BlockData_Nullables(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	block := fakeBlock(101)
	result, err := c.Convert(context.Background(), block)
	assert.NoError(t, err)

	doc := result.Groups[0].Docs[0]
	assert.Nil(t, doc[BlockTimeFieldName], "nil blockTime must stay nil")
}

func TestConvert_TransactionData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(200)
	successTx := fakeTransaction("txmap-success")
	failedTx := fakeFailedTransaction("txmap-failed")
	block := fakeBlockWithTxs(slot, successTx, failedTx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[1].Docs, 2)

	success := result.Groups[1].Docs[0]
	assert.Equal(t, successTx.Signature, success[SignatureFieldName])
	assert.Equal(t, int64(slot), success[constants.BlockNumberFieldName])
	assert.Equal(t, block.Blockhash, success[constants.BlockHashFieldName])
	assert.Equal(t, "legacy", success[VersionFieldName])
	assert.Empty(t, success[ErrFieldName])
	assert.Equal(t, int64(5000), success[FeeFieldName])
	assert.Nil(t, success[ComputeUnitsConsumedFieldName])
	assert.Equal(t, successTx.LogMessages, success[LogMessagesFieldName])
	assert.Equal(t, []int64{100000, 200000}, success[PreBalancesFieldName])
	assert.Equal(t, []int64{95000, 200000}, success[PostBalancesFieldName])
	assert.Equal(t, successTx.RecentBlockhash, success[RecentBlockhashFieldName])
	assert.Equal(t, successTx.AccountKeys, success[AccountKeysFieldName])

	failed := result.Groups[1].Docs[1]
	assert.Equal(t, failedTx.Err, failed[ErrFieldName], "failure is expressed by the err key")
}

func TestConvert_InstructionData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(300)
	tx := fakeTransaction("instr")
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[2].Docs, 1)
	require.Len(t, result.Groups[3].Docs, 1)

	outer := result.Groups[2].Docs[0]
	assert.Equal(t, tx.Instructions[0].ProgramID, outer[ProgramIDFieldName])
	assert.Equal(t, tx.Instructions[0].Accounts, outer[AccountsFieldName])
	assert.Equal(t, tx.Instructions[0].Data, outer[DataFieldName])
	assert.Equal(t, tx.Signature, outer[TransactionSignatureFieldName],
		"outer docs carry the parent tx signature (duplicate-content join field)")
	assert.Equal(t, int64(slot), outer[constants.BlockNumberFieldName])
	assert.Equal(t, block.Blockhash, outer[constants.BlockHashFieldName])
	_, hasStackHeight := outer[StackHeightFieldName]
	assert.False(t, hasStackHeight, "outer docs must omit the stackHeight key (stamper inner/outer discriminator)")

	inner := result.Groups[3].Docs[0]
	assert.Equal(t, tx.InnerInstructions[0].Instructions[0].ProgramID, inner[ProgramIDFieldName])
	assert.Equal(t, int64(2), inner[StackHeightFieldName], "inner docs always carry the stackHeight key")
	assert.Equal(t, tx.Signature, inner[TransactionSignatureFieldName],
		"inner docs carry the parent tx signature (duplicate-content join field)")
}

// TestConvert_InstructionStackHeightInvariant pins the key-presence
// invariant the LinkStamper relies on to route outer vs inner docs when both
// groups share the Instruction collection.
func TestConvert_InstructionStackHeightInvariant(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(310)
	tx := fakeTransaction("invariant")
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	assert.NoError(t, err)

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
	tx := fakeTransaction("nostack")
	tx.InnerInstructions[0].Instructions[0].StackHeight = nil // node omitted it on the wire
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[3].Docs, 1)

	inner := result.Groups[3].Docs[0]
	_, has := inner[StackHeightFieldName]
	assert.True(t, has, "key must still be present so the stamper sees an inner doc")
	assert.Equal(t, nil, inner[StackHeightFieldName],
		"an absent wire value stays null inside the key")
}

func TestConvert_TokenBalanceChangeData(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(400)
	tx := fakeTransaction("tbc")
	// ALT-loaded keys extend the committed list: accountIndex 2 resolves
	// against the loaded writable address, not the static keys.
	tx.LoadedAddresses = []string{fakePubkey("loaded-w")}
	tx.PreTokenBalances[0].AccountIndex = 2
	tx.PostTokenBalances[0].AccountIndex = 2
	block := fakeBlockWithTxs(slot, tx)

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[4].Docs, 1)

	doc := result.Groups[4].Docs[0]
	assert.Equal(t, tx.PostTokenBalances[0].Mint, doc[MintFieldName])
	assert.Equal(t, tx.PostTokenBalances[0].Owner, doc[OwnerFieldName])
	assert.Equal(t, tx.LoadedAddresses[0], doc[TokenAccountFieldName],
		"tokenAccount must resolve against static + ALT-loaded keys")
	assert.Equal(t, "100", doc[PreAmountFieldName])
	assert.Equal(t, "150", doc[PostAmountFieldName])
	assert.Equal(t, tx.PostTokenBalances[0].ProgramID, doc[ProgramIDFieldName])
	assert.Equal(t, tx.Signature, doc[TransactionSignatureFieldName],
		"token balance change docs carry the parent tx signature (duplicate-content join field)")
	assert.Equal(t, int64(slot), doc[constants.BlockNumberFieldName])
	assert.Equal(t, block.Blockhash, doc[constants.BlockHashFieldName])
}

// TestConvert_TokenBalanceChange_OneSided pins the union semantics: an
// account present on only one side (opened or closed mid-transaction) gets
// nil on the absent side, and its identity fields come from the whole
// present entry, post when one exists, pre otherwise (no per-field mixing).
func TestConvert_TokenBalanceChange_OneSided(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(410)
	tx := fakeTransaction("onesided")
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
	block := fakeBlockWithTxs(slot, fakeTransaction("reward"))

	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)
	require.Len(t, result.Groups[5].Docs, 2)

	fee := result.Groups[5].Docs[0]
	assert.Equal(t, block.Rewards[0].Pubkey, fee[PubkeyFieldName])
	assert.Equal(t, int64(-25000), fee[LamportsFieldName], "lamports are signed")
	assert.Equal(t, "987654321", fee[PostBalanceFieldName], "postBalance stored as string (uint64)")
	assert.Equal(t, "Fee", fee[RewardTypeFieldName])
	assert.Nil(t, fee[CommissionFieldName], "fee rewards carry no commission")
	assert.Equal(t, int64(slot), fee[constants.BlockNumberFieldName])

	vote := result.Groups[5].Docs[1]
	assert.Equal(t, int64(405), vote[LamportsFieldName])
	assert.Equal(t, "Voting", vote[RewardTypeFieldName])
	assert.Equal(t, "5", vote[CommissionFieldName], "commission stored as string when present")
}

// TestConvert_DuplicateInstructionContent_UniqueDocs pins the risk-#7 join
// field against DefraDB content-hash docIDs: two transactions in one slot
// carrying byte-identical instruction data (identical memo transfers) and
// identical token balances must produce instruction and token-balance-change
// docs that differ, otherwise AddManyDocuments collides them on one docID
// and the block never signs.
func TestConvert_DuplicateInstructionContent_UniqueDocs(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	slot := uint64(515)
	sharedOuter := fakeInstruction("dup-content")
	sharedInner := fakeInnerInstruction("dup-content", 2)

	mkTx := func(seed string) Transaction {
		tx := fakeTransaction(seed)
		tx.Instructions = []Instruction{sharedOuter}
		tx.InnerInstructions = []InnerInstructionGroup{{Index: 0, Instructions: []Instruction{sharedInner}}}
		tx.PreTokenBalances = []TokenBalance{{AccountIndex: 1, Mint: "mint", Owner: "owner", Amount: "1"}}
		tx.PostTokenBalances = []TokenBalance{{AccountIndex: 1, Mint: "mint", Owner: "owner", Amount: "2"}}
		return tx
	}

	block := fakeBlockWithTxs(slot, mkTx("dup-a"), mkTx("dup-b"))
	result, err := c.Convert(context.Background(), block)
	require.NoError(t, err)

	outerGroup, innerGroup, tbcGroup := result.Groups[2], result.Groups[3], result.Groups[4]
	require.Len(t, outerGroup.Docs, 2)
	require.Len(t, innerGroup.Docs, 2)
	require.Len(t, tbcGroup.Docs, 2)

	sigA := fakeSignature("dup-a")
	sigB := fakeSignature("dup-b")
	assert.NotEqual(t, sigA, sigB)
	for name, group := range map[string]chains.DocumentGroup{
		"outer": outerGroup, "inner": innerGroup, "tokenBalance": tbcGroup,
	} {
		assert.Equal(t, sigA, group.Docs[0][TransactionSignatureFieldName], "%s doc 0 join field", name)
		assert.Equal(t, sigB, group.Docs[1][TransactionSignatureFieldName], "%s doc 1 join field", name)
		assert.NotEqual(t, group.Docs[0], group.Docs[1],
			"%s docs with identical payload content must not be byte-identical", name)
	}

	// Without the join field the two outer maps would be identical: strip the
	// join field and compare. This pins that the join field is the ONLY
	// difference, so the duplicate coverage is real.
	strippedA, strippedB := maps.Clone(outerGroup.Docs[0]), maps.Clone(outerGroup.Docs[1])
	delete(strippedA, TransactionSignatureFieldName)
	delete(strippedB, TransactionSignatureFieldName)
	assert.Equal(t, strippedA, strippedB, "payloads must be genuinely identical; only the join field differentiates")
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

	docIDs, err := c.queryCollectionDocIDs(ctx, td.Node, c.collections.Block, constants.NumberFieldName, 100, 101)
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

// TestGetLowestStoredBlockNumber_ResidueCorrupt pins the corruption path:
// when block documents exist but no row has a usable number (pure residue),
// the query must hard-fail with chains.ErrBlockNumberCorrupt so the pruner
// maps it to ErrNoValidBlocks instead of a benign skip.
func TestGetLowestStoredBlockNumber_ResidueCorrupt(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())
	td := setupSolanaDefraDB(t)
	ctx := context.Background()

	storeResidueBlockDoc(ctx, t, td, c, 5)
	storeResidueBlockDoc(ctx, t, td, c, 6)

	_, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.Error(t, err)
	assert.ErrorIs(t, err, chains.ErrBlockNumberCorrupt)
}

// TestGetLowestStoredBlockNumber_Window pins the
// converter.lowest_block_query_limit plumbing: a small custom window still
// returns the true lowest stored number (residue rows are excluded by the
// _geq: 0 filter, not skipped by the window).
func TestGetLowestStoredBlockNumber_Window(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.Converter.LowestBlockQueryLimit = 1
	c := NewConverter(cfg)
	td := setupSolanaDefraDB(t)
	ctx := context.Background()

	storeResidueBlockDoc(ctx, t, td, c, 7)
	for _, slot := range []int64{100, 101, 102} {
		storeTestBlockDoc(ctx, t, td, c, slot)
	}

	lowest, err := c.GetLowestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest, "the residue row is filter-excluded, so a window of 1 still finds it")
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
// path used in production (buildBlockData -> NewDocFromMap -> AddDocument).
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

// storeResidueBlockDoc writes a block document without a number field,
// simulating the numberless rows left behind by purge residue or P2P
// replication that the block-number queries must exclude. The seed keeps the
// content unique: DefraDB derives docIDs from document content, so identical
// residue documents would collide on the same docID.
func storeResidueBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, seed int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	data := c.buildBlockData(fakeBlock(uint64(seed)))
	delete(data, constants.NumberFieldName)

	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}
