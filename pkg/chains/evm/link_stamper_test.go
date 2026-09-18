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

	s.StampLinks(nil, cols.Block, blockDocs, blockIDs)

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

	s.StampLinks(nil, cols.Transaction, txDocs, txIDs)

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

	s.StampLinks(nil, cols.Log, logDocs, logIDs)

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

	s.StampLinks(nil, cols.Log, logDocs, []string{"log-doc-id-1"})

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

	s.StampLinks(nil, cols.AccessListEntry, aleDocs, []string{"ale-1", "ale-2"})

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

	s.StampLinks(nil, cols.AccessListEntry, aleDocs, []string{"ale-1", "ale-2"})

	assert.Equal(t, "tx-doc-id-1", aleDocs[0]["_transactionID"])
	_, hasTxID := aleDocs[1]["_transactionID"]
	assert.False(t, hasTxID, "second ALE should not get _transactionID when parentRefs is shorter")
}

func TestStampLinks_UnknownCollection(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	docs := []map[string]any{{"foo": "bar"}}
	s.StampLinks(nil, "UnknownCollection", docs, []string{"id-1"})

	assert.Equal(t, "bar", docs[0]["foo"])
	_, hasBlockID := docs[0]["_blockID"]
	assert.False(t, hasBlockID)
}

func TestStampLinks_BlockNoDocIDs(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	s.StampLinks(nil, cols.Block, []map[string]any{{constants.HashKeyValue: "0xabc"}}, nil)

	assert.Empty(t, s.blockID)
}

func TestStampLinks_TransactionBuildsTxHashToID(t *testing.T) {
	t.Parallel()
	cols := NewCollectionNames("Ethereum__Mainnet")
	s := newEvmLinkStamper(cols, nil)

	txDocs := []map[string]any{
		{constants.HashKeyValue: "0xtxA"},
		{constants.HashKeyValue: "0xtxB"},
	}
	txIDs := []string{"docA", "docB"}

	s.StampLinks(nil, cols.Transaction, txDocs, txIDs)

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

	s.StampLinks(groups, cols.Block, groups[0].Docs, []string{"block-id-1"})
	s.StampLinks(groups, cols.Transaction, groups[1].Docs, []string{"tx-id-1"})
	s.StampLinks(groups, cols.Log, groups[2].Docs, []string{"log-id-1"})
	s.StampLinks(groups, cols.AccessListEntry, groups[3].Docs, []string{"ale-id-1"})

	assert.Equal(t, "block-id-1", s.blockID)
	assert.Equal(t, "block-id-1", groups[1].Docs[0]["_blockID"])
	assert.Equal(t, "tx-id-1", s.txHashToID["0xtx1"])
	assert.Equal(t, "block-id-1", groups[2].Docs[0]["_blockID"])
	assert.Equal(t, "tx-id-1", groups[2].Docs[0]["_transactionID"])
	assert.Equal(t, "tx-id-1", groups[3].Docs[0]["_transactionID"])
}
