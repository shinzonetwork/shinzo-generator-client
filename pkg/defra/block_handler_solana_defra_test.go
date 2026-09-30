package defra

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// ---------------------------------------------------------------------------
// Embedded integration tests for the Solana adapter: the full
// Convert → Store → SignExisting pipeline against a real DefraDB node using
// the Solana schema. The mock bundle mirrors the EVM adapter's helpers in
// block_handler_defra_test.go; tests here are exempt from the chain-boundary
// depguard rule (tests of pkg/defra may import the adapter package).
// ---------------------------------------------------------------------------

func setupSolanaDefra(t *testing.T) *testutils.TestDefraDB {
	t.Helper()

	conv := solana.NewConverter(nil)
	sdl, err := conv.GetSchema()
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchema(t, sdl)
}

// solanaCollection resolves a collection name by role, failing the test on
// unknown roles (programmer error).
func solanaCollection(t *testing.T, role string) string {
	t.Helper()
	name, err := solana.NewConverter(nil).Collections().GetCollection(role)
	require.NoError(t, err, "role %q", role)
	return name
}

// solanaRows runs a GQL query and returns the target collection's rows as
// maps, failing the test on query errors.
func solanaRows(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, query, collection string) []map[string]any {
	t.Helper()

	result := td.Node.DB.ExecRequest(ctx, query)
	require.Empty(t, result.GQL.Errors, "query: %s", query)

	data, ok := result.GQL.Data.(map[string]any)
	require.True(t, ok)
	raw, ok := data[collection]
	require.True(t, ok && raw != nil, "collection %s missing from response: %#v", collection, data[collection])

	var rawRows []any
	switch typed := raw.(type) {
	case []any:
		rawRows = typed
	case []map[string]any:
		rawRows = make([]any, len(typed))
		for i, row := range typed {
			rawRows[i] = row
		}
	default:
		t.Fatalf("collection %s returned unsupported shape %T", collection, data[collection])
	}

	rows := make([]map[string]any, 0, len(rawRows))
	for _, rowAny := range rawRows {
		row, ok := rowAny.(map[string]any)
		require.True(t, ok)
		rows = append(rows, row)
	}
	return rows
}

// solanaLinkedDocID extracts the linked document's docID from a relation
// field.
func solanaLinkedDocID(t *testing.T, row map[string]any, linkField string) string {
	t.Helper()
	linked, ok := row[linkField].(map[string]any)
	require.True(t, ok, "%s: expected linked object on %q, got %#v", row, linkField, row[linkField])
	docID, ok := linked["_docID"].(string)
	require.True(t, ok)
	return docID
}

// solanaMockBlock builds a confirmed-block mock with two transactions: one
// successful (with an inner instruction and a pre/post token balance pair)
// and one failed (with only an outer instruction).
func solanaMockBlock() *solana.Block {
	slot := uint64(3600)
	successTx := solana.Transaction{
		Signature:        "sig-success",
		Slot:             slot,
		TransactionIndex: 0,
		Version:          "legacy",
		Failed:           false,
		Fee:              5000,
		LogMessages:      []string{"Program log: entrypoint exited"},
		PreBalances:      []uint64{100000, 200000},
		PostBalances:     []uint64{95000, 200000},
		RecentBlockhash:  "recent-blockhash-success",
		AccountKeys:      []string{"acct-success-0", "acct-success-1"},
		Instructions: []solana.Instruction{
			{
				ProgramID:        "prog-success",
				Accounts:         []string{"acct-success-1"},
				Data:             "outer-data-success",
				InstructionIndex: 0,
				InnerIndex:       0,
			},
		},
		InnerInstructions: []solana.InnerInstructionGroup{
			{
				Index: 0,
				Instructions: []solana.Instruction{
					{
						ProgramID:        "prog-inner",
						Accounts:         []string{"acct-success-0"},
						Data:             "inner-data-success",
						InstructionIndex: 0,
						InnerIndex:       0,
						StackHeight:      new(uint16(2)),
					},
				},
			},
		},
		PreTokenBalances: []solana.TokenBalance{
			{AccountIndex: 1, Mint: "mint-success", Owner: "owner-success", Amount: "0"},
		},
		PostTokenBalances: []solana.TokenBalance{
			{AccountIndex: 1, Mint: "mint-success", Owner: "owner-success", ProgramID: "tokenprog-success", Amount: "42"},
		},
	}
	failedTx := solana.Transaction{
		Signature:        "sig-failed",
		Slot:             slot,
		TransactionIndex: 1,
		Version:          "0",
		Failed:           true,
		Err:              `{"InstructionError":[0,{"Custom":1}]}`,
		Fee:              9000,
		PreBalances:      []uint64{300000},
		PostBalances:     []uint64{291000},
		RecentBlockhash:  "recent-blockhash-failed",
		AccountKeys:      []string{"acct-failed-0"},
		Instructions: []solana.Instruction{
			{
				ProgramID:        "prog-failed",
				Accounts:         []string{"acct-failed-0"},
				Data:             "outer-data-failed",
				InstructionIndex: 0,
				InnerIndex:       0,
			},
		},
	}
	commission := uint8(5)
	return &solana.Block{
		Slot:              slot,
		Blockhash:         "blockhash-3600",
		PreviousBlockhash: "prev-3600",
		ParentSlot:        slot - 1,
		BlockHeight:       new(uint64(3599)),
		BlockTime:         new(int64(1774267845)),
		Transactions:      []solana.Transaction{successTx, failedTx},
		Rewards: []solana.Reward{
			{Pubkey: "reward-vote", Lamports: 405, PostBalance: 1000, RewardType: "Voting", Commission: &commission},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSolanaBlockHandler_Store_FullPipeline exercises the Solana adapter end
// to end: a mock confirmed block is converted and stored; every
// cross-document link must resolve inside DefraDB.
func TestSolanaBlockHandler_Store_FullPipeline(t *testing.T) {
	t.Parallel()

	td := setupSolanaDefra(t)
	ctx := context.Background()

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	conv := solana.NewConverter(nil)
	result, err := conv.Convert(ctx, solanaMockBlock())
	require.NoError(t, err)

	// No signing identity on ctx: the signature step is skipped, but every
	// document group must still be written through the uniform stamper
	// protocol.
	res, err := handler.Store(ctx, result)
	require.NoError(t, err)
	assert.NotEmpty(t, res.BlockID)
	assert.Equal(t, result.SignatureCollection, res.BlockSignatureCollection)

	// Per-collection doc counts: 2 txs, 3 instruction docs combined (the
	// two instruction groups share one collection and accumulate), 1 token
	// balance change, 1 reward, plus the block.
	txCol := solanaCollection(t, chains.TypeTransaction)
	instrCol := solanaCollection(t, chains.TypeInstruction)
	tbcCol := solanaCollection(t, chains.TypeTokenBalanceChange)
	rewardCol := solanaCollection(t, chains.TypeReward)
	assert.Len(t, res.OtherDocIDs[txCol], 2)
	assert.Len(t, res.OtherDocIDs[instrCol], 3, "outer + inner instruction docs, accumulated under one collection key")
	assert.Len(t, res.OtherDocIDs[tbcCol], 1)
	assert.Len(t, res.OtherDocIDs[result.Groups[5].Collection], 1)

	blockCol := solanaCollection(t, chains.TypeBlock)
	blockRows := solanaRows(ctx, t, td, `query { `+blockCol+` { _docID } }`, blockCol)
	require.Len(t, blockRows, 1)
	blockID := blockRows[0]["_docID"].(string)

	// Both transactions link to the one block document.
	txRows := solanaRows(ctx, t, td,
		`query { `+txCol+` { _docID block { _docID } } }`, txCol)
	require.Len(t, txRows, 2)
	for _, row := range txRows {
		assert.Equal(t, blockID, solanaLinkedDocID(t, row, "block"))
	}

	// Rewards link to the block.
	rewardRows := solanaRows(ctx, t, td,
		`query { `+rewardCol+` { _docID block { _docID } } }`, rewardCol)
	require.Len(t, rewardRows, 1)
	assert.Equal(t, blockID, solanaLinkedDocID(t, rewardRows[0], "block"))

	// Instructions link to their parent transactions, in one pass with the
	// stackHeight discriminator separating outer from inner docs. Inner
	// instructions exist only for the successful transaction, whose tx doc
	// is resolved by its signature.
	instrRows := solanaRows(ctx, t, td,
		`query { `+instrCol+` { _docID stackHeight parentInstruction { _docID } transaction { _docID } } }`, instrCol)
	require.Len(t, instrRows, 3)

	var innerRows []map[string]any
	for _, row := range instrRows {
		if row["stackHeight"] == nil {
			assert.Nil(t, row["parentInstruction"], "outer instruction has no parent instruction")
		} else {
			innerRows = append(innerRows, row)
		}
	}
	require.Len(t, innerRows, 1)
	successTxDocID := txDocIDFor(ctx, t, td, txCol, "sig-success")
	assert.Equal(t, successTxDocID, solanaLinkedDocID(t, innerRows[0], "transaction"))
}

// TestSolanaBlockHandler_InnerInstructionParentLink pins the self-relation:
// the inner instruction links to its parent outer instruction document.
func TestSolanaBlockHandler_InnerInstructionParentLink(t *testing.T) {
	t.Parallel()

	td := setupSolanaDefra(t)
	ctx := context.Background()
	conv := solana.NewConverter(nil)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	result, err := conv.Convert(ctx, solanaMockBlock())
	require.NoError(t, err)
	_, err = handler.Store(ctx, result)
	require.NoError(t, err)

	instrCol := solanaCollection(t, chains.TypeInstruction)
	instrRows := solanaRows(ctx, t, td,
		`query { `+instrCol+` { _docID stackHeight parentInstruction { _docID } } }`, instrCol)
	require.Len(t, instrRows, 3)

	var outerDocIDs []string
	var innerRows []map[string]any
	for _, row := range instrRows {
		if row["stackHeight"] == nil {
			outerDocIDs = append(outerDocIDs, row["_docID"].(string))
		} else {
			innerRows = append(innerRows, row)
		}
	}
	require.Len(t, outerDocIDs, 2)
	require.Len(t, innerRows, 1)
	assert.Contains(t, outerDocIDs, solanaLinkedDocID(t, innerRows[0], "parentInstruction"),
		"inner instruction must link to one of the stored outer instruction docs")
}

// TestSolanaBlockHandler_StoreIdempotentAndSignExisting covers the
// processor's already-exists path: storing the same block again must fail
// with "already exists" semantics, and SignExisting over the stored groups
// must produce a signature document.
func TestSolanaBlockHandler_StoreIdempotentAndSignExisting(t *testing.T) {
	t.Parallel()

	td := setupSolanaDefra(t)
	ctx := context.Background()
	conv := solana.NewConverter(nil)

	handler, err := NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	block := solanaMockBlock()
	result, err := conv.Convert(ctx, block)
	require.NoError(t, err)

	res, err := handler.Store(ctx, result)
	require.NoError(t, err)

	// Re-store: the block document already exists; the processor maps this
	// to a compound SignExisting.
	_, err = handler.Store(ctx, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	bh := int64(block.Slot)
	sigDocID, err := handler.SignExisting(ctxWithIdentity(t), result, block.Blockhash, bh)
	require.NoError(t, err)
	assert.NotEmpty(t, sigDocID)
	assert.Empty(t, res.BlockSignatureID, "first Store had no identity, so no signature then")
}

// txDocIDFor resolves a transaction document's docID by its signature.
func txDocIDFor(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, txCol, signature string) string {
	t.Helper()

	rows := solanaRows(ctx, t, td,
		`query { `+txCol+`(filter: {signature: {_eq: "`+signature+`"}}) { _docID } }`, txCol)
	require.Len(t, rows, 1)
	docID, _ := rows[0]["_docID"].(string)
	require.NotEmpty(t, docID)
	return docID
}
