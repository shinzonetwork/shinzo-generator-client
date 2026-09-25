package solana

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// StampBeforeWrite — live link stamping into docs
// ---------------------------------------------------------------------------

func TestStampBeforeWrite_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)
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
	s := newSolanaLinkStamper(cols, nil, nil, nil)

	err := s.StampBeforeWrite(cols.Transaction, []map[string]any{{SignatureFieldName: "sig1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block docID registered")
}

func TestStampBeforeWrite_Transaction_MissingSignature(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)
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
	s := newSolanaLinkStamper(cols, nil, nil, nil)
	s.blockID = "block-doc-id-1"

	rewardDocs := []map[string]any{{PubkeyFieldName: "p1"}}
	err := s.StampBeforeWrite(cols.Reward, rewardDocs)
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", rewardDocs[0]["_blockID"])
}

func TestStampBeforeWrite_Reward_BeforeBlockRegistered(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

	err := s.StampBeforeWrite(cols.Reward, []map[string]any{{PubkeyFieldName: "p1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block docID registered")
}

func TestStampBeforeWrite_OuterInstruction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []string{"sigA"}, nil, nil)
	s.sigToTxID["sigA"] = "tx-doc-id-1"

	outerDocs := []map[string]any{
		{ProgramIDFieldName: "p1", InstructionIndexFieldName: 0},
	}

	err := s.StampBeforeWrite(cols.Instruction, outerDocs)
	require.NoError(t, err)
	assert.Equal(t, "tx-doc-id-1", outerDocs[0]["_transactionID"])
	assert.False(t, hasParentLink(outerDocs[0]), "outer docs must never gain _parentInstructionID")
}

func TestStampBeforeWrite_OuterInstruction_UnregisteredTx(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []string{"sigUnknown"}, nil, nil)

	outerDocs := []map[string]any{{InstructionIndexFieldName: 0}}
	err := s.StampBeforeWrite(cols.Instruction, outerDocs)
	require.NoError(t, err, "unregistered but well-formed parent ref is tolerated with absent link")

	_, hasTxID := outerDocs[0]["_transactionID"]
	assert.False(t, hasTxID)
}

func TestStampBeforeWrite_OuterInstruction_FewerRefs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []string{"sigA"}, nil, nil)
	s.sigToTxID["sigA"] = "tx-doc-id-1"

	outerDocs := []map[string]any{
		{InstructionIndexFieldName: 0},
		{InstructionIndexFieldName: 1},
	}
	err := s.StampBeforeWrite(cols.Instruction, outerDocs)
	require.NoError(t, err)

	assert.Equal(t, "tx-doc-id-1", outerDocs[0]["_transactionID"])
	_, hasTxID := outerDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "second doc should not get _transactionID when refs are shorter")
}

func TestStampBeforeWrite_InnerInstruction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(
		cols,
		[]string{"sigA"},
		[]innerParentRef{{txSignature: "sigA", outerIndex: 0}},
		nil,
	)
	s.sigToTxID["sigA"] = "tx-doc-id-1"
	s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}] = "outer-doc-id-1"

	innerDocs := []map[string]any{
		{ProgramIDFieldName: "p1", InstructionIndexFieldName: 0, StackHeightFieldName: 2},
	}

	err := s.StampBeforeWrite(cols.Instruction, innerDocs)
	require.NoError(t, err)
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
		nil,
	)
	s.sigToTxID["sigA"] = "tx-doc-id-1"

	innerDocs := []map[string]any{
		{InstructionIndexFieldName: 3, StackHeightFieldName: 2},
	}
	err := s.StampBeforeWrite(cols.Instruction, innerDocs)
	require.NoError(t, err, "well-formed but unregistered outer is a tolerated absence")

	assert.Equal(t, "tx-doc-id-1", innerDocs[0]["_transactionID"], "tx link still resolves")
	_, hasParent := innerDocs[0]["_parentInstructionID"]
	assert.False(t, hasParent)
}

func TestStampBeforeWrite_InnerInstruction_MissingInstructionIndex(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, []innerParentRef{{txSignature: "sigA", outerIndex: 0}}, nil)

	err := s.StampBeforeWrite(cols.Instruction, []map[string]any{{StackHeightFieldName: 2}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing or non-int")
}

func TestStampBeforeWrite_TokenBalanceChange(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, []string{"sigB"})
	s.sigToTxID["sigB"] = "tx-doc-id-2"

	tbcDocs := []map[string]any{{MintFieldName: "m1"}}

	err := s.StampBeforeWrite(cols.TokenBalanceChange, tbcDocs)
	require.NoError(t, err)
	assert.Equal(t, "tx-doc-id-2", tbcDocs[0]["_transactionID"])
}

func TestStampBeforeWrite_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

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
		[]string{"sigA"},
		[]innerParentRef{{txSignature: "sigA", outerIndex: 0}},
		nil,
	)
	s.sigToTxID["sigA"] = "tx-doc-id-1"
	s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}] = "outer-doc-id-1"

	outerDocs := []map[string]any{
		{InstructionIndexFieldName: 0}, // outer: no stackHeight key
	}
	innerDocs := []map[string]any{
		{InstructionIndexFieldName: 0, StackHeightFieldName: 2}, // inner
	}

	require.NoError(t, s.StampBeforeWrite(cols.Instruction, outerDocs))
	require.NoError(t, s.StampBeforeWrite(cols.Instruction, innerDocs))

	assert.Equal(t, "tx-doc-id-1", outerDocs[0]["_transactionID"])
	assert.NotContains(t, outerDocs[0], "_parentInstructionID")
	assert.Equal(t, "tx-doc-id-1", innerDocs[0]["_transactionID"])
	assert.Equal(t, "outer-doc-id-1", innerDocs[0]["_parentInstructionID"])
}

// ---------------------------------------------------------------------------
// RecordDocIDs — lookup-state registration
// ---------------------------------------------------------------------------

func TestRecordDocIDs_Block(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

	docs := []map[string]any{{SlotFieldName: uint64(1)}}
	err := s.RecordDocIDs(cols.Block, docs, []string{"block-doc-id-1"})
	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", s.blockID)
}

func TestRecordDocIDs_Block_ReIndex(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

	// Routine re-index: no docIDs at all — silent, state unchanged.
	err := s.RecordDocIDs(cols.Block, []map[string]any{{}}, nil)
	require.NoError(t, err)
	assert.Empty(t, s.blockID)
}

func TestRecordDocIDs_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

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
	s := newSolanaLinkStamper(cols, nil, nil, nil)

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
	s := newSolanaLinkStamper(cols, nil, nil, nil)

	err := s.RecordDocIDs(cols.Transaction, []map[string]any{{}}, []string{"tx-doc-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing, non-string, or empty")
	// The error must prevent registration with an empty key.
	assert.Empty(t, s.sigToTxID)
}

func TestRecordDocIDs_OuterInstruction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []string{"sigA"}, nil, nil)

	docs := []map[string]any{
		{InstructionIndexFieldName: 0},                          // outer
		{InstructionIndexFieldName: 0, StackHeightFieldName: 2}, // inner: skipped
	}
	err := s.RecordDocIDs(cols.Instruction, docs, []string{"outer-doc-1", "inner-doc-1"})
	require.NoError(t, err)

	assert.Equal(t, "outer-doc-1", s.outerIDs[instructionKey{txSignature: "sigA", outerIndex: 0}])
	// Inner docIDs are never registered.
	assert.Len(t, s.outerIDs, 1)
}

func TestRecordDocIDs_OuterInstruction_MissingInstructionIndex(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, []string{"sigA"}, nil, nil)

	err := s.RecordDocIDs(cols.Instruction, []map[string]any{{}}, []string{"doc-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing or non-int")
}

func TestRecordDocIDs_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

	err := s.RecordDocIDs("Unknown__Collection", []map[string]any{{}}, []string{"doc-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown collection")
}

func TestRecordDocIDs_NeverMutatesDocs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Solana__Mainnet")
	s := newSolanaLinkStamper(cols, nil, nil, nil)

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
