package bitcoin

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

// per-block fixtures reuse the sha256-derived seeds so every assertion can be
// reconstructed from the height alone.
const (
	testBlockID  = "block-doc-id-1"
	testTxDocID  = "tx-doc-id-1"
	testChildID1 = "child-doc-id-1"
	testChildID2 = "child-doc-id-2"
)

// ---------------------------------------------------------------------------
// StampBeforeWrite: live link stamping into docs
// ---------------------------------------------------------------------------

func TestStampBeforeWrite_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)
	s.blockID = testBlockID

	txDocs := []map[string]any{
		{TxIDFieldName: "tx-a"},
		{TxIDFieldName: "tx-b"},
	}
	err := s.StampBeforeWrite(cols.Transaction, txDocs)
	require.NoError(t, err)

	assert.Equal(t, testBlockID, txDocs[0]["_blockID"])
	assert.Equal(t, testBlockID, txDocs[1]["_blockID"])
}

func TestStampBeforeWrite_Transaction_InvalidDocFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")

	tests := []struct {
		name    string
		blockID string
		txid    any
		wantErr string
	}{
		{name: "non-string txid", blockID: testBlockID, txid: 12345, wantErr: TxIDFieldName},
		{name: "empty txid", blockID: testBlockID, txid: "", wantErr: TxIDFieldName},
		{name: "no block docID registered", blockID: "", txid: "tx-a", wantErr: "block docID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newBitcoinLinkStamper(cols)
			s.blockID = tc.blockID

			txDocs := []map[string]any{{TxIDFieldName: tc.txid}}
			err := s.StampBeforeWrite(cols.Transaction, txDocs)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			_, hasBlockID := txDocs[0]["_blockID"]
			assert.False(t, hasBlockID, "failing doc must not be stamped")
			assert.Empty(t, s.txidToID, "stamping must not register anything")
		})
	}
}

// TestStampBeforeWrite_ChildDocs covers the txInput and txOutput cases, which
// share one branch by construction and differ only in their collection names.
func TestStampBeforeWrite_ChildDocs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)
	s.blockID = testBlockID
	s.txidToID["tx-a"] = testTxDocID

	for _, collection := range []string{cols.TxInput, cols.TxOutput} {
		t.Run(collection, func(t *testing.T) {
			t.Parallel()
			childDocs := []map[string]any{{TxIDFieldName: "tx-a"}}
			require.NoError(t, s.StampBeforeWrite(collection, childDocs))
			assert.Equal(t, testTxDocID, childDocs[0]["_transactionID"])
		})
	}
}

func TestStampBeforeWrite_ChildDocs_UnknownTxidTolerated(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)
	s.blockID = testBlockID

	childDocs := []map[string]any{{TxIDFieldName: "tx-unknown"}}
	err := s.StampBeforeWrite(cols.TxInput, childDocs)
	require.NoError(t, err, "a well-formed txid with no registered transaction is a tolerated absence, not an error")

	_, hasTxID := childDocs[0]["_transactionID"]
	assert.False(t, hasTxID, "_transactionID should stay absent when the txid is unknown")
}

func TestStampBeforeWrite_ChildDocs_InvalidFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")

	tests := []struct {
		name    string
		blockID string
		txid    any
		wantErr string
	}{
		{name: "missing txid", blockID: testBlockID, txid: nil, wantErr: TxIDFieldName},
		{name: "non-string txid", blockID: testBlockID, txid: 42, wantErr: TxIDFieldName},
		{name: "empty txid", blockID: testBlockID, txid: "", wantErr: TxIDFieldName},
		{name: "no block docID registered", blockID: "", txid: "tx-a", wantErr: "block docID"},
	}
	for _, collection := range []string{cols.TxInput, cols.TxOutput} {
		for _, tc := range tests {
			t.Run(collection+"_"+tc.name, func(t *testing.T) {
				t.Parallel()
				s := newBitcoinLinkStamper(cols)
				s.blockID = tc.blockID
				s.txidToID["tx-real"] = testTxDocID // a real tx must never leak into a failing doc

				childDocs := []map[string]any{{}}
				if tc.txid != nil {
					childDocs[0][TxIDFieldName] = tc.txid
				}
				err := s.StampBeforeWrite(collection, childDocs)

				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				_, hasTxID := childDocs[0]["_transactionID"]
				assert.False(t, hasTxID, "failing doc must not be stamped")
				_, hasBlockID := childDocs[0]["_blockID"]
				assert.False(t, hasBlockID, "failing doc must not be stamped")
				assert.Empty(t, s.txidToID[""], "the empty-string key must never appear")
			})
		}
	}
}

func TestStampBeforeWrite_BlockIsNoOp(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	blockDocs := []map[string]any{{constants.HashFieldName: "00block"}}
	require.NoError(t, s.StampBeforeWrite(cols.Block, blockDocs))

	_, hasBlockID := blockDocs[0]["_blockID"]
	assert.False(t, hasBlockID, "the block doc carries no link fields")
}

func TestStampBeforeWrite_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	docs := []map[string]any{{"foo": "bar"}}
	err := s.StampBeforeWrite("UnknownCollection", docs)

	require.Error(t, err, "an unknown collection must fail loudly instead of silently skipping stamping")
	assert.Contains(t, err.Error(), "unknown collection")
	assert.Equal(t, "bar", docs[0]["foo"], "docs must be untouched for unknown collections")
	_, hasBlockID := docs[0]["_blockID"]
	assert.False(t, hasBlockID)
}

// ---------------------------------------------------------------------------
// RecordDocIDs: lookup-state registration, never mutating docs
// ---------------------------------------------------------------------------

func TestRecordDocIDs_Block(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	err := s.RecordDocIDs(cols.Block, []map[string]any{{constants.HashFieldName: "00block"}}, []string{testBlockID})
	require.NoError(t, err)
	assert.Equal(t, testBlockID, s.blockID)
}

func TestRecordDocIDs_Block_NoDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	err := s.RecordDocIDs(cols.Block, []map[string]any{{constants.HashFieldName: "00block"}}, nil)
	require.NoError(t, err, "a block write with no docIDs keeps the prior blockID without error")
	assert.Empty(t, s.blockID)
}

func TestRecordDocIDs_TransactionBuildsTxidToID(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	txDocs := []map[string]any{{TxIDFieldName: "tx-a"}, {TxIDFieldName: "tx-b"}}
	err := s.RecordDocIDs(cols.Transaction, txDocs, []string{testTxDocID, "tx-doc-id-2"})
	require.NoError(t, err)

	assert.Equal(t, testTxDocID, s.txidToID["tx-a"])
	assert.Equal(t, "tx-doc-id-2", s.txidToID["tx-b"])
}

func TestRecordDocIDs_Transaction_PartialDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	txDocs := []map[string]any{{TxIDFieldName: "tx-a"}, {TxIDFieldName: "tx-b"}}
	err := s.RecordDocIDs(cols.Transaction, txDocs, []string{testTxDocID})
	require.NoError(t, err, "fewer docIDs than docs is a routine partial write, not an error")

	assert.Equal(t, testTxDocID, s.txidToID["tx-a"])
	_, hasB := s.txidToID["tx-b"]
	assert.False(t, hasB, "docs without docIDs are not registered")
	_, hasBlockID := txDocs[0]["_blockID"]
	assert.False(t, hasBlockID, "RecordDocIDs never mutates docs")
}

func TestRecordDocIDs_Transaction_ZeroDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	txDocs := []map[string]any{{TxIDFieldName: "tx-a"}, {TxIDFieldName: "tx-b"}}
	err := s.RecordDocIDs(cols.Transaction, txDocs, nil)
	require.NoError(t, err, "a routine re-index with no docIDs at all stays silent")
	assert.Empty(t, s.txidToID)
	_, hasBlockID := txDocs[0]["_blockID"]
	assert.False(t, hasBlockID, "RecordDocIDs never mutates docs")
}

func TestRecordDocIDs_Transaction_InvalidTxidFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")

	tests := []struct {
		name string
		txid any
	}{
		{name: "non-string txid", txid: 12345},
		{name: "empty txid", txid: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newBitcoinLinkStamper(cols)

			txDocs := []map[string]any{{TxIDFieldName: tc.txid}}
			err := s.RecordDocIDs(cols.Transaction, txDocs, []string{testTxDocID})

			require.Error(t, err, "an unparseable txid cannot be registered")
			assert.Contains(t, err.Error(), TxIDFieldName)
			_, hasEmpty := s.txidToID[""]
			assert.False(t, hasEmpty, "the empty-string key must never appear")
			assert.Empty(t, s.txidToID, "nothing may be registered when a doc fails validation")
		})
	}
}

func TestRecordDocIDs_ChildCollectionsNoOp(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	for _, collection := range []string{cols.TxInput, cols.TxOutput} {
		err := s.RecordDocIDs(collection, []map[string]any{{TxIDFieldName: "tx-a"}}, []string{testChildID1})
		require.NoError(t, err, "%s docIDs have no later consumer", collection)
	}
	assert.Empty(t, s.txidToID)
}

func TestRecordDocIDs_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	docs := []map[string]any{{"foo": "bar"}}
	err := s.RecordDocIDs("UnknownCollection", docs, []string{"id-1"})

	require.Error(t, err, "an unknown collection must fail loudly instead of silently skipping recording")
	assert.Contains(t, err.Error(), "unknown collection")
	assert.Empty(t, s.blockID, "nothing may be registered for unknown collections")
	assert.Empty(t, s.txidToID, "nothing may be registered for unknown collections")
}

// TestRecordDocIDs_NeverMutatesDocs proves the post-write field assignments
// are gone: the record pass performs zero doc mutations for every collection.
func TestRecordDocIDs_NeverMutatesDocs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")

	cases := []struct {
		name       string
		collection string
		docs       []map[string]any
		ids        []string
	}{
		{
			name:       "block",
			collection: cols.Block,
			docs:       []map[string]any{{constants.HashFieldName: "00block"}},
			ids:        []string{"block-id"},
		},
		{
			name:       "transaction",
			collection: cols.Transaction,
			docs:       []map[string]any{{TxIDFieldName: "tx-a"}},
			ids:        []string{"tx-id"},
		},
		{
			name:       "txInput",
			collection: cols.TxInput,
			docs:       []map[string]any{{TxIDFieldName: "tx-a"}},
			ids:        []string{"input-id"},
		},
		{
			name:       "txOutput",
			collection: cols.TxOutput,
			docs:       []map[string]any{{TxIDFieldName: "tx-a"}},
			ids:        []string{"output-id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newBitcoinLinkStamper(cols)
			s.blockID = testBlockID
			s.txidToID["tx-a"] = testTxDocID

			want := make([]map[string]any, len(tc.docs))
			for i, m := range tc.docs {
				c := make(map[string]any, len(m))
				maps.Copy(c, m)
				want[i] = c
			}

			require.NoError(t, s.RecordDocIDs(tc.collection, tc.docs, tc.ids))
			assert.Equal(t, want, tc.docs, "RecordDocIDs must never mutate docs")
		})
	}
}

// ---------------------------------------------------------------------------
// Uniform protocol
// ---------------------------------------------------------------------------

// TestUniformProtocol_FullBlockSequence drives every group through the same
// stamp, record protocol the store runs: the block group's stamp is a no-op
// and its record harvests the block docID the later groups stamp from.
func TestUniformProtocol_FullBlockSequence(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	groups := []string{cols.Block, cols.Transaction, cols.TxInput, cols.TxOutput}
	docs := [][]map[string]any{
		{{constants.HashFieldName: "00block"}},
		{{TxIDFieldName: "tx-a"}},
		{{TxIDFieldName: "tx-a"}},
		{{TxIDFieldName: "tx-a"}},
	}
	assignedIDs := [][]string{
		{testBlockID},
		{testTxDocID},
		{testChildID1},
		{testChildID2},
	}

	for i, collection := range groups {
		require.NoError(t, s.StampBeforeWrite(collection, docs[i]))
		require.NoError(t, s.RecordDocIDs(collection, docs[i], assignedIDs[i]))
	}

	assert.Equal(t, testBlockID, s.blockID)
	assert.Equal(t, testBlockID, docs[1][0]["_blockID"])
	assert.Equal(t, testTxDocID, s.txidToID["tx-a"])
	assert.Equal(t, testTxDocID, docs[2][0]["_transactionID"])
	assert.Equal(t, testTxDocID, docs[3][0]["_transactionID"])
}

// TestUniformProtocol_ConverterOutput drives the stamper through the exact
// groups Convert emits, so the group order (block, transaction, txInput,
// txOutput) is pinned against real docs and the txid join field the converter
// actually writes.
func TestUniformProtocol_ConverterOutput(t *testing.T) {
	t.Parallel()
	c := NewConverter(testConverterConfig())

	result, err := c.Convert(context.Background(), fakeBlockWithTxs(7, fakeTransaction(7, 1)))
	require.NoError(t, err)
	require.Len(t, result.Groups, 4)

	s, ok := result.LinkStamper.(*bitcoinLinkStamper)
	require.True(t, ok, "the converter must ship a bitcoinLinkStamper")

	assigned := map[string][]string{
		result.Groups[0].Collection: {testBlockID},
		result.Groups[1].Collection: {testTxDocID, "tx-doc-id-2"},
		result.Groups[2].Collection: {testChildID1, testChildID2},
		result.Groups[3].Collection: {testChildID1},
	}
	for i := range result.Groups {
		group := &result.Groups[i]
		require.NoError(t, s.StampBeforeWrite(group.Collection, group.Docs))
		require.NoError(t, s.RecordDocIDs(group.Collection, group.Docs, assigned[group.Collection]))
	}

	assert.Equal(t, testBlockID, result.Groups[1].Docs[0]["_blockID"])
	assert.Equal(t, testBlockID, result.Groups[1].Docs[1]["_blockID"])
	assert.Equal(t, testTxDocID, result.Groups[2].Docs[0]["_transactionID"])
	assert.Equal(t, "tx-doc-id-2", result.Groups[2].Docs[1]["_transactionID"])
	assert.Equal(t, testTxDocID, result.Groups[3].Docs[0]["_transactionID"])
}

// TestUniformProtocol_OutOfOrderStampErrors pins the misuse contract: no
// derived doc may be stamped before the block docID is registered. Docs stay
// untouched when the guard fires.
func TestUniformProtocol_OutOfOrderStampErrors(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")

	for _, collection := range []string{cols.Transaction, cols.TxInput, cols.TxOutput} {
		t.Run(collection, func(t *testing.T) {
			t.Parallel()
			s := newBitcoinLinkStamper(cols)
			docs := []map[string]any{{TxIDFieldName: "tx-a"}}

			err := s.StampBeforeWrite(collection, docs)
			require.Error(t, err, "stamping before the block record is a protocol misuse")
			assert.Contains(t, err.Error(), "block docID")
			_, hasBlockID := docs[0]["_blockID"]
			assert.False(t, hasBlockID, "failing doc must not be stamped")
			_, hasTxID := docs[0]["_transactionID"]
			assert.False(t, hasTxID, "failing doc must not be stamped")
		})
	}
}

// TestCorruptionRegression pins the structural guard against the poisoned-map
// failure: a malformed doc inside a batch must never let a well-formed doc
// receive an unrelated transaction's link.
func TestCorruptionRegression(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Bitcoin__Mainnet")
	s := newBitcoinLinkStamper(cols)

	s.blockID = testBlockID
	require.NoError(t, s.RecordDocIDs(cols.Transaction,
		[]map[string]any{{TxIDFieldName: "tx-a"}}, []string{testTxDocID}))

	// Second doc is missing the txid key entirely; the first doc is valid and
	// must stamp normally before the failure.
	childDocs := []map[string]any{
		{TxIDFieldName: "tx-a"},
		{},
	}
	err := s.StampBeforeWrite(cols.TxInput, childDocs)

	require.Error(t, err)
	assert.Equal(t, testTxDocID, childDocs[0]["_transactionID"], "the valid doc stamps normally")
	_, hasTxID := childDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "a doc with a missing txid must never receive a _transactionID")
	_, hasBlockID := childDocs[1]["_blockID"]
	assert.False(t, hasBlockID, "a failing doc must not be stamped at all")
	_, hasEmpty := s.txidToID[""]
	assert.False(t, hasEmpty, "txidToID must never contain the empty-string key")
}
