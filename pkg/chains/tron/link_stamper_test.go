package tron

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLinkStamper_FullFlow walks the whole protocol in group order: record
// the block's docID, record transaction docID hashes, then stamp derived
// docs with _blockID and _transactionID.
func TestLinkStamper_FullFlow(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	stamper := newTronLinkStamper(cols)

	// Block group writes first; its docID registers.
	blockDoc := map[string]any{"hash": "b1", "number": int64(10)}
	require.NoError(t, stamper.StampBeforeWrite(cols.Block, []map[string]any{blockDoc}))
	require.NoError(t, stamper.RecordDocIDs(cols.Block, []map[string]any{blockDoc}, []string{"block-doc-id"}))

	// Transaction group stamps _blockID, then registers the tx docIDs.
	txDoc := map[string]any{"hash": "t1", "blockNumber": int64(10)}
	require.NoError(t, stamper.StampBeforeWrite(cols.Transaction, []map[string]any{txDoc}))
	assert.Equal(t, "block-doc-id", txDoc["_blockID"])
	require.NoError(t, stamper.RecordDocIDs(cols.Transaction, []map[string]any{txDoc}, []string{"tx-doc-id"}))

	// Log and internal-transfer docs stamp both links from their own
	// transactionHash field.
	logDoc := map[string]any{"transactionHash": "t1", "blockNumber": int64(10)}
	require.NoError(t, stamper.StampBeforeWrite(cols.Log, []map[string]any{logDoc}))
	assert.Equal(t, "block-doc-id", logDoc["_blockID"])
	assert.Equal(t, "tx-doc-id", logDoc["_transactionID"])
	require.NoError(t, stamper.RecordDocIDs(cols.Log, []map[string]any{logDoc}, []string{"log-doc-id"}))

	transferDoc := map[string]any{"transactionHash": "t1", "blockNumber": int64(10)}
	require.NoError(t, stamper.StampBeforeWrite(cols.InternalTransfer, []map[string]any{transferDoc}))
	assert.Equal(t, "block-doc-id", transferDoc["_blockID"])
	assert.Equal(t, "tx-doc-id", transferDoc["_transactionID"])

	// The block doc itself carries no link fields before stamping.
	_, hasBlock := blockDoc["_blockID"]
	assert.False(t, hasBlock)
}

// TestLinkStamper_UnregisteredTransactionHash leaves _transactionID absent
// with no error: a well-formed hash whose tx was never recorded is a
// partial state, not a failure.
func TestLinkStamper_UnregisteredTransactionHash(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	stamper := newTronLinkStamper(cols)

	blockDoc := map[string]any{"hash": "b1"}
	require.NoError(t, stamper.RecordDocIDs(cols.Block, []map[string]any{blockDoc}, []string{"block-doc-id"}))

	logDoc := map[string]any{"transactionHash": "unknown-tx"}
	require.NoError(t, stamper.StampBeforeWrite(cols.Log, []map[string]any{logDoc}))
	assert.Equal(t, "block-doc-id", logDoc["_blockID"])
	_, present := logDoc["_transactionID"]
	assert.False(t, present, "_transactionID must be absent, never an empty string")
}

// TestLinkStamper_MissingTransactionHashErrors covers the input contract:
// log and internal-transfer docs must carry a non-empty transactionHash
// before any mutation happens.
func TestLinkStamper_MissingTransactionHashErrors(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)

	blockDoc := map[string]any{"hash": "b1"}
	for _, collection := range []string{cols.Log, cols.InternalTransfer} {
		stamper := newTronLinkStamper(cols)
		require.NoError(t, stamper.RecordDocIDs(cols.Block, []map[string]any{blockDoc}, []string{"block-doc-id"}))

		docs := []map[string]any{{"transactionHash": nil}, {"transactionHash": 42}, {"transactionHash": ""}}
		for i := range docs {
			err := stamper.StampBeforeWrite(collection, docs[:i+1])
			require.Error(t, err, "case %d", i)
			_, stamped := docs[i]["_blockID"]
			assert.False(t, stamped, "a failing doc is never stamped")
		}
	}
}

// TestLinkStamper_TransactionHashRequiredForRegistration covers
// RecordDocIDs: an invalid transaction hash must never enter txHashToID.
func TestLinkStamper_TransactionHashRequiredForRegistration(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	stamper := newTronLinkStamper(cols)

	docs := []map[string]any{{"hash": ""}}
	err := stamper.RecordDocIDs(cols.Transaction, docs, []string{"tx-doc-id"})
	require.Error(t, err)
	assert.Empty(t, stamper.txHashToID)
}

// TestLinkStamper_NoBlockBeforeStampErrors covers the ordering contract:
// _blockID is never stamped from an unregistered block.
func TestLinkStamper_NoBlockBeforeStampErrors(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	stamper := newTronLinkStamper(cols)

	txDoc := map[string]any{"hash": "t1"}
	err := stamper.StampBeforeWrite(cols.Transaction, []map[string]any{txDoc})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block docID registered")

	logDoc := map[string]any{"transactionHash": "t1"}
	err = stamper.StampBeforeWrite(cols.Log, []map[string]any{logDoc})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no block docID registered")
}

// TestLinkStamper_PartialWriteTolerance covers the partial-write contract:
// short id slices are routine, silent without any ids, and logged with
// some.
func TestLinkStamper_PartialWriteTolerance(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	stamper := newTronLinkStamper(cols)

	docs := []map[string]any{{"hash": "t1"}, {"hash": "t2"}, {"hash": "t3"}}

	// A re-indexed block returns no ids at all: silent, nothing registered.
	require.NoError(t, stamper.RecordDocIDs(cols.Transaction, docs, nil))
	assert.Empty(t, stamper.txHashToID)

	// A part-failed batch returns partial ids: covered docs register.
	require.NoError(t, stamper.RecordDocIDs(cols.Transaction, docs, []string{"doc-1"}))
	assert.Equal(t, "doc-1", stamper.txHashToID["t1"])
	_, present := stamper.txHashToID["t2"]
	assert.False(t, present)

	// A block group written without docIDs keeps its prior state.
	require.NoError(t, stamper.RecordDocIDs(cols.Block, []map[string]any{{"hash": "b1"}}, nil))
	assert.Empty(t, stamper.blockID)
}

// TestLinkStamper_UnknownCollection covers the registration safety: an
// unknown collection name errors in both protocol calls.
func TestLinkStamper_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames(DefaultCollectionPrefix)
	stamper := newTronLinkStamper(cols)

	require.Error(t, stamper.StampBeforeWrite("Unknown__Collection", []map[string]any{}))
	require.Error(t, stamper.RecordDocIDs("Unknown__Collection", []map[string]any{}, []string{"id"}))
}
