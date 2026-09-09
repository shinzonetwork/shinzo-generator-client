package evm

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// ---------------------------------------------------------------------------
// StampBeforeWrite — live link stamping into docs
// ---------------------------------------------------------------------------

func TestStampBeforeWrite_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"

	txDocs := []map[string]any{
		{HashKeyValue: "0xtx1"},
		{HashKeyValue: "0xtx2"},
	}

	err := s.StampBeforeWrite(cols.Transaction, txDocs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", txDocs[0]["_blockID"])
	assert.Equal(t, "block-doc-id-1", txDocs[1]["_blockID"])
}

func TestStampBeforeWrite_Log(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"
	s.txHashToID["0xtx1"] = "tx-doc-id-1"

	logDocs := []map[string]any{
		{TransactionHashKeyValue: "0xtx1"},
	}

	err := s.StampBeforeWrite(cols.Log, logDocs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", logDocs[0]["_blockID"])
	assert.Equal(t, "tx-doc-id-1", logDocs[0]["_transactionID"])
}

func TestStampBeforeWrite_Log_TxNotYetStamped(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"

	logDocs := []map[string]any{
		{TransactionHashKeyValue: "0xunknown"},
	}

	err := s.StampBeforeWrite(cols.Log, logDocs)
	require.NoError(t, err, "a well-formed hash with no registered tx is a tolerated absence, not an error")

	assert.Equal(t, "block-doc-id-1", logDocs[0]["_blockID"])
	_, hasTxID := logDocs[0]["_transactionID"]
	assert.False(t, hasTxID, "_transactionID should not be set when tx hash is unknown")
}

func TestStampBeforeWrite_AccessListEntry(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtx1", "0xtx2"})
	s.txHashToID["0xtx1"] = "tx-doc-id-1"
	s.txHashToID["0xtx2"] = "tx-doc-id-2"

	aleDocs := []map[string]any{
		{AddressKeyValue: "0x01"},
		{AddressKeyValue: "0x02"},
	}

	err := s.StampBeforeWrite(cols.AccessListEntry, aleDocs)
	require.NoError(t, err)

	assert.Equal(t, "tx-doc-id-1", aleDocs[0]["_transactionID"])
	assert.Equal(t, "tx-doc-id-2", aleDocs[1]["_transactionID"])
}

func TestStampBeforeWrite_AccessListEntry_FewerParentRefs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtx1"})
	s.txHashToID["0xtx1"] = "tx-doc-id-1"

	aleDocs := []map[string]any{
		{AddressKeyValue: "0x01"},
		{AddressKeyValue: "0x02"},
	}

	err := s.StampBeforeWrite(cols.AccessListEntry, aleDocs)
	require.NoError(t, err)

	assert.Equal(t, "tx-doc-id-1", aleDocs[0]["_transactionID"])
	_, hasTxID := aleDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "second ALE should not get _transactionID when parentRefs is shorter")
}

func TestStampBeforeWrite_AccessListEntry_UnknownParentRef(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtxUnknown"})
	s.txHashToID["0xtx1"] = "tx-doc-id-1"

	aleDocs := []map[string]any{
		{AddressKeyValue: "0x01"},
	}

	err := s.StampBeforeWrite(cols.AccessListEntry, aleDocs)
	require.NoError(t, err, "unregistered but well-formed parent ref is tolerated with absent link")

	_, hasTxID := aleDocs[0]["_transactionID"]
	assert.False(t, hasTxID, "ALE should not get _transactionID when parent tx was never registered")
}

func TestStampBeforeWrite_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	docs := []map[string]any{{"foo": "bar"}}
	err := s.StampBeforeWrite("UnknownCollection", docs)

	require.Error(t, err, "an unknown collection must fail loudly instead of silently skipping stamping")
	assert.Contains(t, err.Error(), "unknown collection")
	assert.Equal(t, "bar", docs[0]["foo"], "docs must be untouched for unknown collections")
	_, hasBlockID := docs[0]["_blockID"]
	assert.False(t, hasBlockID)
}

// ---------------------------------------------------------------------------
// RecordDocIDs — lookup-state registration, never mutating docs
// ---------------------------------------------------------------------------

func TestRecordDocIDs_Block(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	blockDocs := []map[string]any{{HashKeyValue: "0xabc"}}
	blockIDs := []string{"block-doc-id-1"}

	err := s.RecordDocIDs(cols.Block, blockDocs, blockIDs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", s.blockID)
}

func TestRecordDocIDs_Block_NoDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	err := s.RecordDocIDs(cols.Block, []map[string]any{{HashKeyValue: "0xabc"}}, nil)
	require.NoError(t, err, "block with no docIDs keeps prior blockID without error")

	assert.Empty(t, s.blockID)
}

func TestRecordDocIDs_TransactionBuildsTxHashToID(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	txDocs := []map[string]any{
		{HashKeyValue: "0xtxA"},
		{HashKeyValue: "0xtxB"},
	}
	txIDs := []string{"docA", "docB"}

	err := s.RecordDocIDs(cols.Transaction, txDocs, txIDs)
	require.NoError(t, err)

	require.Len(t, s.txHashToID, 2)
	assert.Equal(t, "docA", s.txHashToID["0xtxA"])
	assert.Equal(t, "docB", s.txHashToID["0xtxB"])
}

func TestRecordDocIDs_Transaction_PartialDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	// Two docs but only one docID: routine partial write (e.g. re-indexed
	// block whose docs already exist), not an error. The guard contract:
	// registration is skipped for docs without docIDs.
	txDocs := []map[string]any{
		{HashKeyValue: "0xtx1"},
		{HashKeyValue: "0xtx2"},
	}
	err := s.RecordDocIDs(cols.Transaction, txDocs, []string{"tx-doc-id-1"})

	require.NoError(t, err)
	assert.Equal(t, "tx-doc-id-1", s.txHashToID["0xtx1"])
	_, hasTx2 := s.txHashToID["0xtx2"]
	assert.False(t, hasTx2, "docs without docIDs are not registered")
	_, hasBlockID := txDocs[0]["_blockID"]
	assert.False(t, hasBlockID, "RecordDocIDs never mutates docs")
}

func TestRecordDocIDs_Transaction_ZeroDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	// Routine re-index: every doc already exists, no docIDs returned at all.
	// The call is silent — no error, nothing registered, docs untouched.
	txDocs := []map[string]any{
		{HashKeyValue: "0xtx1"},
		{HashKeyValue: "0xtx2"},
	}
	err := s.RecordDocIDs(cols.Transaction, txDocs, nil)

	require.NoError(t, err)
	assert.Empty(t, s.txHashToID, "no docIDs means nothing is registered")
	_, hasBlockID := txDocs[0]["_blockID"]
	assert.False(t, hasBlockID, "RecordDocIDs never mutates docs")
}

func TestRecordDocIDs_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	docs := []map[string]any{{"foo": "bar"}}
	err := s.RecordDocIDs("UnknownCollection", docs, []string{"id-1"})

	require.Error(t, err, "an unknown collection must fail loudly instead of silently skipping recording")
	assert.Contains(t, err.Error(), "unknown collection")
	assert.Empty(t, s.blockID, "nothing may be registered for unknown collections")
	assert.Empty(t, s.txHashToID, "nothing may be registered for unknown collections")
}

// TestRecordDocIDs_NeverMutatesDocs proves the post-write field assignments
// are gone: the Record pass performs zero doc mutations for every collection.
func TestRecordDocIDs_NeverMutatesDocs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")

	cases := []struct {
		name       string
		collection string
		docs       []map[string]any
		ids        []string
	}{
		{
			name:       "block",
			collection: cols.Block,
			docs:       []map[string]any{{HashKeyValue: "0xblock"}},
			ids:        []string{"block-id"},
		},
		{
			name:       "transaction",
			collection: cols.Transaction,
			docs:       []map[string]any{{HashKeyValue: "0xtx1"}},
			ids:        []string{"tx-id"},
		},
		{
			name:       "log",
			collection: cols.Log,
			docs:       []map[string]any{{TransactionHashKeyValue: "0xtx1"}},
			ids:        []string{"log-id"},
		},
		{
			name:       "accessListEntry",
			collection: cols.AccessListEntry,
			docs:       []map[string]any{{AddressKeyValue: "0x01"}},
			ids:        []string{"ale-id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newEvmLinkStamper(cols, nil)
			s.blockID = "block-doc-id-1"
			s.txHashToID["0xtx1"] = "tx-doc-id-1"

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

// TestUniformProtocol_FullBlockSequence drives every group through the same
// uniform Stamp → Record protocol — the block group included: the block doc's
// Stamp is field-wise a no-op, and its Record harvests the block docID that
// later groups stamp from.
func TestUniformProtocol_FullBlockSequence(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtx1"})

	groups := []chains.DocumentGroup{
		{Collection: cols.Block, Docs: []map[string]any{{HashKeyValue: "0xblock"}}},
		{Collection: cols.Transaction, Docs: []map[string]any{{HashKeyValue: "0xtx1"}}},
		{Collection: cols.Log, Docs: []map[string]any{{TransactionHashKeyValue: "0xtx1"}}},
		{Collection: cols.AccessListEntry, Docs: []map[string]any{{AddressKeyValue: "0x01"}}},
	}
	assignedIDs := [][]string{
		{"block-id-1"},
		{"tx-id-1"},
		{"log-id-1"},
		{"ale-id-1"},
	}

	for i, g := range groups {
		require.NoError(t, s.StampBeforeWrite(g.Collection, g.Docs))
		require.NoError(t, s.RecordDocIDs(g.Collection, g.Docs, assignedIDs[i]))
	}

	assert.Equal(t, "block-id-1", s.blockID)
	assert.Equal(t, "block-id-1", groups[1].Docs[0]["_blockID"])
	assert.Equal(t, "tx-id-1", s.txHashToID["0xtx1"])
	assert.Equal(t, "block-id-1", groups[2].Docs[0]["_blockID"])
	assert.Equal(t, "tx-id-1", groups[2].Docs[0]["_transactionID"])
	assert.Equal(t, "tx-id-1", groups[3].Docs[0]["_transactionID"])
}

// ---------------------------------------------------------------------------
// Error contract
// ---------------------------------------------------------------------------

func TestStampBeforeWrite_Transaction_InvalidDocFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")

	tests := []struct {
		name    string
		blockID string // preset block docID; "" leaves it unregistered
		txHash  any    // doc "hash" value
		wantErr string
	}{
		{
			name:    "non-string hash",
			blockID: "block-doc-id-1",
			txHash:  12345,
			wantErr: HashKeyValue,
		},
		{
			name:    "empty hash",
			blockID: "block-doc-id-1",
			txHash:  "",
			wantErr: HashKeyValue,
		},
		{
			name:    "no block docID registered",
			blockID: "",
			txHash:  "0xtx1",
			wantErr: "block docID",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newEvmLinkStamper(cols, nil)
			s.blockID = tc.blockID

			txDocs := []map[string]any{{HashKeyValue: tc.txHash}}
			err := s.StampBeforeWrite(cols.Transaction, txDocs)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			_, hasBlockID := txDocs[0]["_blockID"]
			assert.False(t, hasBlockID, "failing doc must not be stamped")
			_, hasEmpty := s.txHashToID[""]
			assert.False(t, hasEmpty, "empty hash must never be registered")
			assert.Empty(t, s.txHashToID, "stamping must not register anything")
		})
	}
}

func TestRecordDocIDs_Transaction_InvalidHashFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")

	tests := []struct {
		name   string
		txHash any
	}{
		{name: "non-string hash", txHash: 12345},
		{name: "empty hash", txHash: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newEvmLinkStamper(cols, nil)

			txDocs := []map[string]any{{HashKeyValue: tc.txHash}}
			err := s.RecordDocIDs(cols.Transaction, txDocs, []string{"tx-doc-id-1"})

			require.Error(t, err, "an unparseable hash cannot be registered")
			assert.Contains(t, err.Error(), HashKeyValue)
			_, hasEmpty := s.txHashToID[""]
			assert.False(t, hasEmpty, "empty hash must never be registered")
			assert.Empty(t, s.txHashToID, "nothing may be registered when a doc fails validation")
		})
	}
}

func TestStampBeforeWrite_Log_InvalidDocFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")

	tests := []struct {
		name    string
		logHash any // nil omits the key entirely (missing hash case)
	}{
		{name: "missing transactionHash", logHash: nil},
		{name: "non-string transactionHash", logHash: 42},
		{name: "empty transactionHash", logHash: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newEvmLinkStamper(cols, nil)
			s.blockID = "block-doc-id-1"
			s.txHashToID["0xtx1"] = "tx-doc-id-1" // a real tx must never leak into the failing doc

			logDocs := []map[string]any{{}}
			if tc.logHash != nil {
				logDocs[0][TransactionHashKeyValue] = tc.logHash
			}

			err := s.StampBeforeWrite(cols.Log, logDocs)

			require.Error(t, err)
			assert.Contains(t, err.Error(), TransactionHashKeyValue)
			_, hasBlockID := logDocs[0]["_blockID"]
			assert.False(t, hasBlockID, "failing doc must not be stamped")
			_, hasTxID := logDocs[0]["_transactionID"]
			assert.False(t, hasTxID, "a real tx must never be linked into a failing doc")
		})
	}
}

func TestLog_CorruptionRegression(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	// Register a real transaction.
	s.blockID = "block-doc-id-1"
	txDocs := []map[string]any{{HashKeyValue: "0xtx1"}}
	require.NoError(t, s.RecordDocIDs(cols.Transaction, txDocs, []string{"tx-doc-id-1"}))

	// A log batch where the second doc is missing transactionHash. Before the
	// error contract, the failed tx assertion could register txHashToID[""]
	// and a log with a missing hash looked it up, receiving an unrelated
	// transaction's _transactionID. That must be structurally impossible now.
	logDocs := []map[string]any{
		{TransactionHashKeyValue: "0xtx1"},
		{},
	}
	err := s.StampBeforeWrite(cols.Log, logDocs)

	require.Error(t, err)
	assert.Equal(t, "tx-doc-id-1", logDocs[0]["_transactionID"], "the valid log doc is stamped normally")
	_, hasTxID := logDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "a log with a missing transactionHash must never receive a _transactionID")
	_, hasBlockID := logDocs[1]["_blockID"]
	assert.False(t, hasBlockID, "a failing log doc must not be stamped at all")
	_, hasEmpty := s.txHashToID[""]
	assert.False(t, hasEmpty, "txHashToID must never contain the empty-string key")
}
