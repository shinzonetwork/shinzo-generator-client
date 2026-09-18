package evm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
)

func TestStampLinks_Block(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	blockDocs := []map[string]any{{constants.HashKeyValue: "0xabc"}}
	blockIDs := []string{"block-doc-id-1"}

	err := s.StampLinks(nil, cols.Block, blockDocs, blockIDs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", s.blockID)
}

func TestStampLinks_Transaction(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"

	txDocs := []map[string]any{
		{constants.HashKeyValue: "0xtx1"},
		{constants.HashKeyValue: "0xtx2"},
	}
	txIDs := []string{"tx-doc-id-1", "tx-doc-id-2"}

	err := s.StampLinks(nil, cols.Transaction, txDocs, txIDs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", txDocs[0]["_blockID"])
	assert.Equal(t, "block-doc-id-1", txDocs[1]["_blockID"])
	assert.Equal(t, "tx-doc-id-1", s.txHashToID["0xtx1"])
	assert.Equal(t, "tx-doc-id-2", s.txHashToID["0xtx2"])
}

func TestStampLinks_Log(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"
	s.txHashToID["0xtx1"] = "tx-doc-id-1"

	logDocs := []map[string]any{
		{constants.TransactionHashKeyValue: "0xtx1"},
	}
	logIDs := []string{"log-doc-id-1"}

	err := s.StampLinks(nil, cols.Log, logDocs, logIDs)
	require.NoError(t, err)

	assert.Equal(t, "block-doc-id-1", logDocs[0]["_blockID"])
	assert.Equal(t, "tx-doc-id-1", logDocs[0]["_transactionID"])
}

func TestStampLinks_Log_TxNotYetStamped(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"

	logDocs := []map[string]any{
		{constants.TransactionHashKeyValue: "0xunknown"},
	}

	err := s.StampLinks(nil, cols.Log, logDocs, []string{"log-doc-id-1"})
	require.NoError(t, err, "a well-formed hash with no registered tx is a tolerated absence, not an error")

	assert.Equal(t, "block-doc-id-1", logDocs[0]["_blockID"])
	_, hasTxID := logDocs[0]["_transactionID"]
	assert.False(t, hasTxID, "_transactionID should not be set when tx hash is unknown")
}

func TestStampLinks_AccessListEntry(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtx1", "0xtx2"})
	s.txHashToID["0xtx1"] = "tx-doc-id-1"
	s.txHashToID["0xtx2"] = "tx-doc-id-2"

	aleDocs := []map[string]any{
		{constants.AddressKeyValue: "0x01"},
		{constants.AddressKeyValue: "0x02"},
	}

	err := s.StampLinks(nil, cols.AccessListEntry, aleDocs, []string{"ale-1", "ale-2"})
	require.NoError(t, err)

	assert.Equal(t, "tx-doc-id-1", aleDocs[0]["_transactionID"])
	assert.Equal(t, "tx-doc-id-2", aleDocs[1]["_transactionID"])
}

func TestStampLinks_AccessListEntry_FewerParentRefs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtx1"})
	s.txHashToID["0xtx1"] = "tx-doc-id-1"

	aleDocs := []map[string]any{
		{constants.AddressKeyValue: "0x01"},
		{constants.AddressKeyValue: "0x02"},
	}

	err := s.StampLinks(nil, cols.AccessListEntry, aleDocs, []string{"ale-1", "ale-2"})
	require.NoError(t, err)

	assert.Equal(t, "tx-doc-id-1", aleDocs[0]["_transactionID"])
	_, hasTxID := aleDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "second ALE should not get _transactionID when parentRefs is shorter")
}

func TestStampLinks_AccessListEntry_UnknownParentRef(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtxUnknown"})
	s.txHashToID["0xtx1"] = "tx-doc-id-1"

	aleDocs := []map[string]any{
		{constants.AddressKeyValue: "0x01"},
	}

	err := s.StampLinks(nil, cols.AccessListEntry, aleDocs, []string{"ale-1"})
	require.NoError(t, err, "unregistered but well-formed parent ref is tolerated with absent link")

	_, hasTxID := aleDocs[0]["_transactionID"]
	assert.False(t, hasTxID, "ALE should not get _transactionID when parent tx was never registered")
}

func TestStampLinks_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	docs := []map[string]any{{"foo": "bar"}}
	err := s.StampLinks(nil, "UnknownCollection", docs, []string{"id-1"})

	require.Error(t, err, "an unknown collection must fail loudly instead of silently skipping stamping")
	assert.Contains(t, err.Error(), "unknown collection")
	assert.Equal(t, "bar", docs[0]["foo"], "docs must be untouched for unknown collections")
	_, hasBlockID := docs[0]["_blockID"]
	assert.False(t, hasBlockID)
}

func TestStampLinks_BlockNoDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	err := s.StampLinks(nil, cols.Block, []map[string]any{{constants.HashKeyValue: "0xabc"}}, nil)
	require.NoError(t, err, "block with no docIDs keeps prior blockID without error")

	assert.Empty(t, s.blockID)
}

func TestStampLinks_TransactionBuildsTxHashToID(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"

	txDocs := []map[string]any{
		{constants.HashKeyValue: "0xtxA"},
		{constants.HashKeyValue: "0xtxB"},
	}
	txIDs := []string{"docA", "docB"}

	err := s.StampLinks(nil, cols.Transaction, txDocs, txIDs)
	require.NoError(t, err)

	require.Len(t, s.txHashToID, 2)
	assert.Equal(t, "docA", s.txHashToID["0xtxA"])
	assert.Equal(t, "docB", s.txHashToID["0xtxB"])
}

func TestStampLinks_FullBlockSequence(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, []string{"0xtx1"})

	groups := []chains.DocumentGroup{
		{Collection: cols.Block, Docs: []map[string]any{{constants.HashKeyValue: "0xblock"}}},
		{Collection: cols.Transaction, Docs: []map[string]any{{constants.HashKeyValue: "0xtx1"}}},
		{Collection: cols.Log, Docs: []map[string]any{{constants.TransactionHashKeyValue: "0xtx1"}}},
		{Collection: cols.AccessListEntry, Docs: []map[string]any{{constants.AddressKeyValue: "0x01"}}},
	}

	require.NoError(t, s.StampLinks(groups, cols.Block, groups[0].Docs, []string{"block-id-1"}))
	require.NoError(t, s.StampLinks(groups, cols.Transaction, groups[1].Docs, []string{"tx-id-1"}))
	require.NoError(t, s.StampLinks(groups, cols.Log, groups[2].Docs, []string{"log-id-1"}))
	require.NoError(t, s.StampLinks(groups, cols.AccessListEntry, groups[3].Docs, []string{"ale-id-1"}))

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

func TestStampLinks_Transaction_InvalidDocFails(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")

	tests := []struct {
		name    string
		blockID string // preset block docID; "" leaves it unregistered
		txHash  any    // doc "hash" value
		txIDs   []string
		wantErr string
	}{
		{
			name:    "non-string hash (pre-write)",
			blockID: "block-doc-id-1",
			txHash:  12345,
			txIDs:   nil,
			wantErr: constants.HashKeyValue,
		},
		{
			name:    "non-string hash (post-write)",
			blockID: "block-doc-id-1",
			txHash:  12345,
			txIDs:   []string{"tx-doc-id-1"},
			wantErr: constants.HashKeyValue,
		},
		{
			name:    "empty hash",
			blockID: "block-doc-id-1",
			txHash:  "",
			txIDs:   []string{"tx-doc-id-1"},
			wantErr: constants.HashKeyValue,
		},
		{
			name:    "no block docID registered",
			blockID: "",
			txHash:  "0xtx1",
			txIDs:   []string{"tx-doc-id-1"},
			wantErr: "block docID",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newEvmLinkStamper(cols, nil)
			s.blockID = tc.blockID

			txDocs := []map[string]any{{constants.HashKeyValue: tc.txHash}}
			err := s.StampLinks(nil, cols.Transaction, txDocs, tc.txIDs)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			_, hasBlockID := txDocs[0]["_blockID"]
			assert.False(t, hasBlockID, "failing doc must not be stamped")
			_, hasEmpty := s.txHashToID[""]
			assert.False(t, hasEmpty, "empty hash must never be registered")
			assert.Empty(t, s.txHashToID, "nothing may be registered when a doc fails validation")
		})
	}
}

func TestStampLinks_Log_InvalidDocFails(t *testing.T) {
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
				logDocs[0][constants.TransactionHashKeyValue] = tc.logHash
			}

			err := s.StampLinks(nil, cols.Log, logDocs, []string{"log-doc-id-1"})

			require.Error(t, err)
			assert.Contains(t, err.Error(), constants.TransactionHashKeyValue)
			_, hasBlockID := logDocs[0]["_blockID"]
			assert.False(t, hasBlockID, "failing doc must not be stamped")
			_, hasTxID := logDocs[0]["_transactionID"]
			assert.False(t, hasTxID, "a real tx must never be linked into a failing doc")
		})
	}
}

func TestStampLinks_Log_CorruptionRegression(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	// Register a real transaction.
	s.blockID = "block-doc-id-1"
	require.NoError(t, s.StampLinks(nil, cols.Transaction,
		[]map[string]any{{constants.HashKeyValue: "0xtx1"}}, []string{"tx-doc-id-1"}))

	// A log batch where the second doc is missing transactionHash. Before the
	// error contract, the failed tx assertion could register txHashToID[""]
	// and a log with a missing hash looked it up, receiving an unrelated
	// transaction's _transactionID. That must be structurally impossible now.
	logDocs := []map[string]any{
		{constants.TransactionHashKeyValue: "0xtx1"},
		{},
	}
	err := s.StampLinks(nil, cols.Log, logDocs, []string{"log-doc-id-1", "log-doc-id-2"})

	require.Error(t, err)
	assert.Equal(t, "tx-doc-id-1", logDocs[0]["_transactionID"], "the valid log doc is stamped normally")
	_, hasTxID := logDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "a log with a missing transactionHash must never receive a _transactionID")
	_, hasBlockID := logDocs[1]["_blockID"]
	assert.False(t, hasBlockID, "a failing log doc must not be stamped at all")
	_, hasEmpty := s.txHashToID[""]
	assert.False(t, hasEmpty, "txHashToID must never contain the empty-string key")
}

func TestStampLinks_Transaction_PartialDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)
	s.blockID = "block-doc-id-1"

	// Two docs but only one docID: routine partial write (e.g. re-indexed
	// block whose docs already exist), not an error.
	txDocs := []map[string]any{
		{constants.HashKeyValue: "0xtx1"},
		{constants.HashKeyValue: "0xtx2"},
	}
	err := s.StampLinks(nil, cols.Transaction, txDocs, []string{"tx-doc-id-1"})

	require.NoError(t, err)
	assert.Equal(t, "block-doc-id-1", txDocs[0]["_blockID"])
	assert.Equal(t, "block-doc-id-1", txDocs[1]["_blockID"], "stamping proceeds for docs without docIDs")
	assert.Equal(t, "tx-doc-id-1", s.txHashToID["0xtx1"])
	_, hasTx2 := s.txHashToID["0xtx2"]
	assert.False(t, hasTx2, "docs without docIDs are not registered")
}
