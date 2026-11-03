package solana

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// StampBeforeWrite: live link stamping into docs
// ---------------------------------------------------------------------------

func TestStampBeforeWrite_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)
	s.blockID = "block-doc-id-1"

	txDocs := []map[string]any{
		{SignatureFieldName: "sig1"},
		{SignatureFieldName: "sig2"},
	}

	err := s.StampBeforeWrite(cols.Transaction, txDocs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", txDocs[0]["_blockID"])
	assert.Equal(t, "block-doc-id-1", txDocs[1]["_blockID"])
}

func TestStampBeforeWrite_Transaction_BeforeBlockRegistered(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	err := s.StampBeforeWrite(cols.Transaction, []map[string]any{{SignatureFieldName: "sig1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block docID registered")
}

func TestStampBeforeWrite_Transaction_MissingSignature(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)
	s.blockID = "block-doc-id-1"

	for _, doc := range []map[string]any{
		{},                       // missing
		{SignatureFieldName: 42}, // non-string
		{SignatureFieldName: ""}, // empty
	} {
		err := s.StampBeforeWrite(cols.Transaction, []map[string]any{doc})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing, non-string, or empty")
	}
}

func TestStampBeforeWrite_Reward(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)
	s.blockID = "block-doc-id-1"

	rewardDocs := []map[string]any{{PubkeyFieldName: "p1"}}
	err := s.StampBeforeWrite(cols.Reward, rewardDocs)
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", rewardDocs[0]["_blockID"])
}

func TestStampBeforeWrite_Reward_BeforeBlockRegistered(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	err := s.StampBeforeWrite(cols.Reward, []map[string]any{{PubkeyFieldName: "p1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block docID registered")
}

// Every derived Solana collection declares the block relation, so stamping
// any of them before the block docID exists must fail.
func TestStampBeforeWrite_DerivedDocsBeforeBlockRegistered(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")

	t.Run("OuterInstruction", func(t *testing.T) {
		t.Parallel()
		s := newSolanaLinkStamper(cols, []instructionKey{{txSignature: "sigA", outerIndex: 0}}, nil)
		err := s.StampBeforeWrite(cols.Instruction, []map[string]any{
			{TransactionSignatureFieldName: "sigA"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no block docID registered")
	})

	t.Run("InnerInstruction", func(t *testing.T) {
		t.Parallel()
		s := newSolanaLinkStamper(cols, nil, []innerParentRef{{txSignature: "sigA", outerIndex: 0}})
		err := s.StampBeforeWrite(cols.Instruction, []map[string]any{
			{TransactionSignatureFieldName: "sigA", StackHeightFieldName: 2},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no block docID registered")
	})

	t.Run("TokenBalanceChange", func(t *testing.T) {
		t.Parallel()
		s := newSolanaLinkStamper(cols, nil, nil)
		err := s.StampBeforeWrite(cols.TokenBalanceChange, []map[string]any{
			{TransactionSignatureFieldName: "sigB"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no block docID registered")
	})
}

// Instruction and token-balance-change docs must carry the join field: the
// stamper resolves _transactionID from the doc itself, and DefraDB
// content-hash docIDs collide without it.
func TestStampBeforeWrite_MissingJoinField(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")

	instrDocs := []map[string]any{{ProgramIDFieldName: "p1"}}
	innerWithKey := []map[string]any{{StackHeightFieldName: 2}}
	tbcDocs := []map[string]any{{MintFieldName: "m1"}}

	s := newSolanaLinkStamper(cols, []instructionKey{{txSignature: "sigA", outerIndex: 0}}, nil)
	err := s.StampBeforeWrite(cols.Instruction, instrDocs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing, non-string, or empty")

	s = newSolanaLinkStamper(cols, nil, []innerParentRef{{txSignature: "sigA", outerIndex: 0}})
	err = s.StampBeforeWrite(cols.Instruction, innerWithKey)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing, non-string, or empty")

	s = newSolanaLinkStamper(cols, nil, nil)
	err = s.StampBeforeWrite(cols.TokenBalanceChange, tbcDocs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing, non-string, or empty")
}

func TestStampBeforeWrite_OuterInstruction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []instructionKey{{txSignature: "sigA", outerIndex: 0}}, nil)
	s.blockID = "block-doc-id-1"
	s.sigToTxID["sigA"] = "tx-doc-id-1"

	outerDocs := []map[string]any{
		{ProgramIDFieldName: "p1", TransactionSignatureFieldName: "sigA"},
	}

	err := s.StampBeforeWrite(cols.Instruction, outerDocs)
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", outerDocs[0]["_blockID"])
	assert.Equal(t, "tx-doc-id-1", outerDocs[0]["_transactionID"])
	assert.False(t, hasParentLink(outerDocs[0]), "outer docs must never gain _parentInstructionID")
}

func TestStampBeforeWrite_OuterInstruction_UnregisteredTx(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []instructionKey{{txSignature: "sigUnknown", outerIndex: 0}}, nil)
	s.blockID = "block-doc-id-1"

	outerDocs := []map[string]any{{TransactionSignatureFieldName: "sigUnknown"}}
	err := s.StampBeforeWrite(cols.Instruction, outerDocs)
	require.NoError(t, err, "unregistered but well-formed join field is tolerated with absent link")

	assert.Equal(t, "block-doc-id-1", outerDocs[0]["_blockID"])
	_, hasTxID := outerDocs[0]["_transactionID"]
	assert.False(t, hasTxID)
}

func TestStampBeforeWrite_OuterInstruction_MoreDocsThanRefs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	// Refs cover only the first outer instruction doc.
	s := newSolanaLinkStamper(cols, []instructionKey{{txSignature: "sigA", outerIndex: 0}}, nil)
	s.blockID = "block-doc-id-1"
	s.sigToTxID["sigA"] = "tx-doc-id-1"

	outerDocs := []map[string]any{
		{TransactionSignatureFieldName: "sigA"},
		{TransactionSignatureFieldName: "sigB"},
	}
	err := s.StampBeforeWrite(cols.Instruction, outerDocs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", outerDocs[1]["_blockID"],
		"the block link still stamps without a parent ref")
	assert.Equal(t, "tx-doc-id-1", outerDocs[0]["_transactionID"])
	_, hasTxID := outerDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "second doc should not get _transactionID when refs are shorter")
}

func TestStampBeforeWrite_InnerInstruction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(
		cols,
		[]instructionKey{{txSignature: "sigA", outerIndex: 0}},
		[]innerParentRef{{txSignature: "sigA", outerIndex: 0}},
	)
	s.blockID = "block-doc-id-1"
	s.sigToTxID["sigA"] = "tx-doc-id-1"
	s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}] = "outer-doc-id-1"

	innerDocs := []map[string]any{
		{ProgramIDFieldName: "p1", StackHeightFieldName: 2, TransactionSignatureFieldName: "sigA"},
	}

	err := s.StampBeforeWrite(cols.Instruction, innerDocs)
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", innerDocs[0]["_blockID"])
	assert.Equal(t, "tx-doc-id-1", innerDocs[0]["_transactionID"])
	assert.Equal(t, "outer-doc-id-1", innerDocs[0]["_parentInstructionID"])
}

func TestStampBeforeWrite_InnerInstruction_UnregisteredOuter(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(
		cols,
		nil,
		[]innerParentRef{{txSignature: "sigA", outerIndex: 3}},
	)
	s.blockID = "block-doc-id-1"
	s.sigToTxID["sigA"] = "tx-doc-id-1"

	innerDocs := []map[string]any{
		{StackHeightFieldName: 2, TransactionSignatureFieldName: "sigA"},
	}
	err := s.StampBeforeWrite(cols.Instruction, innerDocs)
	require.NoError(t, err, "well-formed but unregistered outer is a tolerated absence")

	assert.Equal(t, "tx-doc-id-1", innerDocs[0]["_transactionID"], "tx link still resolves")
	_, hasParent := innerDocs[0]["_parentInstructionID"]
	assert.False(t, hasParent)
}

func TestStampBeforeWrite_TokenBalanceChange(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)
	s.blockID = "block-doc-id-1"
	s.sigToTxID["sigB"] = "tx-doc-id-2"

	tbcDocs := []map[string]any{{MintFieldName: "m1", TransactionSignatureFieldName: "sigB"}}

	err := s.StampBeforeWrite(cols.TokenBalanceChange, tbcDocs)
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", tbcDocs[0]["_blockID"])
	assert.Equal(t, "tx-doc-id-2", tbcDocs[0]["_transactionID"])
}

func TestStampBeforeWrite_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	err := s.StampBeforeWrite("Unknown__Collection", []map[string]any{{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown collection")
}

// TestStampBeforeWrite_MixedInstructionDocs pins the discriminator: within
// the shared Instruction collection case, docs without the stackHeight key
// route as outer and docs with it route as inner. Stamping is called once
// per group (as BlockHandler.Store does), so outer and inner docs arrive in
// separate calls sharing the stamper's recorded state.
func TestStampBeforeWrite_MixedInstructionDocs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(
		cols,
		[]instructionKey{{txSignature: "sigA", outerIndex: 0}},
		[]innerParentRef{{txSignature: "sigA", outerIndex: 0}},
	)
	s.blockID = "block-doc-id-1"
	s.sigToTxID["sigA"] = "tx-doc-id-1"
	s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}] = "outer-doc-id-1"

	outerDocs := []map[string]any{
		{ProgramIDFieldName: "p1", TransactionSignatureFieldName: "sigA"}, // outer: no stackHeight key
	}
	innerDocs := []map[string]any{
		{ProgramIDFieldName: "p1", TransactionSignatureFieldName: "sigA", StackHeightFieldName: 2}, // inner
	}

	require.NoError(t, s.StampBeforeWrite(cols.Instruction, outerDocs))
	require.NoError(t, s.StampBeforeWrite(cols.Instruction, innerDocs))

	assert.Equal(t, "tx-doc-id-1", outerDocs[0]["_transactionID"])
	assert.NotContains(t, outerDocs[0], "_parentInstructionID")
	assert.Equal(t, "tx-doc-id-1", innerDocs[0]["_transactionID"])
	assert.Equal(t, "outer-doc-id-1", innerDocs[0]["_parentInstructionID"])
}

// ---------------------------------------------------------------------------
// RecordDocIDs: lookup-state registration
// ---------------------------------------------------------------------------

func TestRecordDocIDs_Block(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	docs := []map[string]any{{SignatureFieldName: "sig1", FeeFieldName: int64(1)}}
	err := s.RecordDocIDs(cols.Block, docs, []string{"block-doc-id-1"})
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", s.blockID)
}

func TestRecordDocIDs_Block_ReIndex(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	// Routine re-index: no docIDs at all; silent, state unchanged.
	err := s.RecordDocIDs(cols.Block, []map[string]any{{}}, nil)
	require.NoError(t, err)
	assert.Empty(t, s.blockID)
}

func TestRecordDocIDs_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	docs := []map[string]any{
		{SignatureFieldName: "sig1"},
		{SignatureFieldName: "sig2"},
	}
	err := s.RecordDocIDs(cols.Transaction, docs, []string{"tx-doc-1", "tx-doc-2"})
	require.NoError(t, err)
	assert.Equal(t, "tx-doc-1", s.sigToTxID["sig1"])
	assert.Equal(t, "tx-doc-2", s.sigToTxID["sig2"])
}

func TestRecordDocIDs_Transaction_PartialWrite(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	docs := []map[string]any{
		{SignatureFieldName: "sig1"},
		{SignatureFieldName: "sig2"},
	}
	// Shorter ids slice: routine partial write, not an error; uncovered doc
	// is simply not registered.
	err := s.RecordDocIDs(cols.Transaction, docs, []string{"tx-doc-1"})
	require.NoError(t, err)
	assert.Equal(t, "tx-doc-1", s.sigToTxID["sig1"])
	_, ok := s.sigToTxID["sig2"]
	assert.False(t, ok)
}

func TestRecordDocIDs_Transaction_MissingSignature(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	err := s.RecordDocIDs(cols.Transaction, []map[string]any{{}}, []string{"tx-doc-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing, non-string, or empty")
	// The error must prevent registration with an empty key.
	assert.Empty(t, s.sigToTxID)
}

func TestRecordDocIDs_OuterInstruction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []instructionKey{{txSignature: "sigA", outerIndex: 0}}, nil)

	docs := []map[string]any{
		{TransactionSignatureFieldName: "sigA"},                          // outer
		{TransactionSignatureFieldName: "sigA", StackHeightFieldName: 2}, // inner: skipped
	}
	err := s.RecordDocIDs(cols.Instruction, docs, []string{"outer-doc-1", "inner-doc-1"})
	require.NoError(t, err)

	assert.Equal(t, "outer-doc-1", s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}])
	// Inner docIDs are never registered.
	assert.Len(t, s.outerIDs, 1)
}

func TestRecordDocIDs_OuterInstruction_PartialWrite(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(
		cols,
		[]instructionKey{
			{txSignature: "sigA", outerIndex: 0},
			{txSignature: "sigA", outerIndex: 1},
		},
		nil,
	)

	docs := []map[string]any{
		{TransactionSignatureFieldName: "sigA"},
		{TransactionSignatureFieldName: "sigA"},
	}
	err := s.RecordDocIDs(cols.Instruction, docs, []string{"outer-doc-1"})
	require.NoError(t, err)

	assert.Equal(t, "outer-doc-1", s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}])
	_, ok := s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 1}]
	assert.False(t, ok, "partial writes skip uncovered docs without erroring")
}

func TestRecordDocIDs_OuterInstruction_MissingRef(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	// No parallel refs at all: nothing to register, still not an error.
	err := s.RecordDocIDs(cols.Instruction, []map[string]any{{TransactionSignatureFieldName: "sigA"}}, []string{"doc-1"})
	require.NoError(t, err)
	assert.Empty(t, s.outerIDs)
}

func TestRecordDocIDs_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	err := s.RecordDocIDs("Unknown__Collection", []map[string]any{{}}, []string{"doc-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown collection")
}

func TestRecordDocIDs_NeverMutatesDocs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil)

	docs := []map[string]any{
		{SignatureFieldName: "sig1"},
	}
	before := docs[0][SignatureFieldName]
	err := s.RecordDocIDs(cols.Transaction, docs, []string{"tx-doc-1"})
	require.NoError(t, err)

	assert.Len(t, docs[0], 1, "RecordDocIDs must not mutate docs")
	assert.Equal(t, before, docs[0][SignatureFieldName])
}

// hasParentLink reports whether doc carries a _parentInstructionID value.
func hasParentLink(doc map[string]any) bool {
	_, ok := doc["_parentInstructionID"]
	return ok
}

// ---------------------------------------------------------------------------
// Uniform protocol: the full group-order flow BlockHandler.Store runs
// (StampBeforeWrite -> write -> RecordDocIDs per group), driven through the
// converter's own output.
// ---------------------------------------------------------------------------

func TestLinkStamper_FullBlockFlow(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConfig())

	result, err := c.Convert(context.Background(), fakeBlockWithTxs(9, fakeTransaction("flow")))
	require.NoError(t, err)
	stamper := result.LinkStamper
	require.NotNil(t, stamper)
	require.Len(t, result.Groups, 6)

	// The write order is fixed: block -> transaction -> outer -> inner ->
	// tokenBalanceChange -> reward, each stamped before its simulated write
	// and recorded after, exactly as BlockHandler.Store drives the protocol.
	for _, g := range result.Groups {
		ids := make([]string, len(g.Docs))
		for j := range ids {
			ids[j] = "doc-" + g.Collection + "-" + strconv.Itoa(j)
		}
		require.NoError(t, stamper.StampBeforeWrite(g.Collection, g.Docs))
		require.NoError(t, stamper.RecordDocIDs(g.Collection, g.Docs, ids))
	}

	blockDoc := result.Groups[0].Docs[0]
	require.NotContains(t, blockDoc, "_blockID", "the block doc itself carries no link fields")

	txDoc := result.Groups[1].Docs[0]
	assert.Equal(t, "doc-"+result.Groups[0].Collection+"-0", txDoc["_blockID"])
	assert.NotContains(t, txDoc, "_transactionID",
		"transactions have no transaction link; _transactionID only joins derived docs")

	innerDoc := result.Groups[3].Docs[0]
	assert.Equal(t, "doc-"+result.Groups[1].Collection+"-0", innerDoc["_transactionID"])
	assert.Equal(t, "doc-"+result.Groups[2].Collection+"-0", innerDoc["_parentInstructionID"])

	tbcDoc := result.Groups[4].Docs[0]
	assert.Equal(t, "doc-"+result.Groups[1].Collection+"-0", tbcDoc["_transactionID"])

	rewardDoc := result.Groups[5].Docs[0]
	assert.Equal(t, "doc-"+result.Groups[0].Collection+"-0", rewardDoc["_blockID"])
	assert.NotContains(t, rewardDoc, "_transactionID", "rewards have no transaction relation")
}
