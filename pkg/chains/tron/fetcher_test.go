package tron

import (
	"context"
	"fmt"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchBlock_AssemblesBundle covers the happy path: block plus pointer
// transactions plus receipts, in one bundle.
func TestFetchBlock_AssemblesBundle(t *testing.T) {
	t.Parallel()
	block := &Block{BlockID: "b1", BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 10, Timestamp: 1777445121000, ParentHash: "p1"}}}
	block.Transactions = []Transaction{{TxID: "t1"}, {TxID: "t2"}}
	fake := &fakeTronClient{blockByNum: block, receipts: []*TransactionInfo{
		{ID: "t1", BlockNumber: 10, BlockTimeStamp: 1777445121000},
		{ID: "t2", BlockNumber: 10, BlockTimeStamp: 1777445121000},
	}}

	fetcher := NewFetcher(fake)
	raw, err := fetcher.FetchBlock(context.Background(), 10)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok, "FetchBlock must return *BlockBundle")
	assert.Same(t, block, bundle.Block)
	require.Len(t, bundle.Transactions, 2)
	assert.Equal(t, "t1", bundle.Transactions[0].TxID)
	assert.Equal(t, "t2", bundle.Transactions[1].TxID)
	require.Len(t, bundle.Receipts, 2)
	assert.Equal(t, "t1", bundle.Receipts[0].ID)
}

// TestFetchBlock_ReceiptPinMismatch covers the bundle pin: both reads are
// by height and Tron receipts carry no block hash, so blockNumber and
// blockTimeStamp must match the block. A mismatch fails the fetch, and the
// error must contain no "not found" so the processor treats it as a real
// failure rather than a benign missing block.
func TestFetchBlock_ReceiptPinMismatch(t *testing.T) {
	t.Parallel()
	block := &Block{BlockID: "b1", BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 10, Timestamp: 1777445121000}}}
	block.Transactions = []Transaction{{TxID: "t1"}}

	tests := []struct {
		name     string
		receipt  *TransactionInfo
		mismatch string
	}{
		{
			name:     "wrong block number",
			receipt:  &TransactionInfo{ID: "t1", BlockNumber: 11, BlockTimeStamp: 1777445121000},
			mismatch: "block number",
		},
		{
			name:     "wrong block timestamp",
			receipt:  &TransactionInfo{ID: "t1", BlockNumber: 10, BlockTimeStamp: 999},
			mismatch: "timestamp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeTronClient{blockByNum: block, receipts: []*TransactionInfo{tt.receipt}}
			fetcher := NewFetcher(fake)

			_, err := fetcher.FetchBlock(context.Background(), 10)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.mismatch)
			assert.NotContains(t, err.Error(), "not found", "a pin mismatch must not look like a missing block")
		})
	}
}

// TestFetchBlock_EmptyBlockIsABundleWithNoTxs covers the common Tron case:
// zero transactions and an empty receipt list.
func TestFetchBlock_EmptyBlockIsABundleWithNoTxs(t *testing.T) {
	t.Parallel()
	block := &Block{BlockID: "b1", BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 10, Timestamp: 1777445121000}}}
	fake := &fakeTronClient{blockByNum: block, receipts: nil}

	fetcher := NewFetcher(fake)
	raw, err := fetcher.FetchBlock(context.Background(), 10)
	require.NoError(t, err)
	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.NotNil(t, bundle.Block)
	assert.Empty(t, bundle.Transactions)
	assert.Empty(t, bundle.Receipts)
}

// TestFetchBlock_NotFoundPassesThrough pins that a not-yet-produced height
// keeps the not-found classification of the client error.
func TestFetchBlock_NotFoundPassesThrough(t *testing.T) {
	t.Parallel()
	fake := &fakeTronClient{blockByNumErr: fmt.Errorf("tron: block 123456 not found")}
	fetcher := NewFetcher(fake)

	_, err := fetcher.FetchBlock(context.Background(), 123456)
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err))
}

// TestFetchBlockHeaderByHash pins the walk-back read: height and parent
// hash from the node, hash echoed back from the request.
func TestFetchBlockHeaderByHash(t *testing.T) {
	t.Parallel()
	fake := &fakeTronClient{blockByID: &Block{
		BlockID:     "b1",
		BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 10, ParentHash: "p1"}},
	}}
	fetcher := NewFetcher(fake)

	header, err := fetcher.FetchBlockHeaderByHash(context.Background(), "b1")
	require.NoError(t, err)
	assert.Equal(t, int64(10), header.Number)
	assert.Equal(t, "b1", header.Hash, "the requested hash must be echoed back")
	assert.Equal(t, "p1", header.ParentHash)

	fake.blockByIDErr = fmt.Errorf("tron: block b1 not found")
	_, err = fetcher.FetchBlockHeaderByHash(context.Background(), "b1")
	require.Error(t, err)
	assert.True(t, errors.IsErrNotFound(err))
}

// TestFetchHeadAndFinalizedNumbers covers both tip queries.
func TestFetchHeadAndFinalizedNumbers(t *testing.T) {
	t.Parallel()
	fake := &fakeTronClient{
		nowBlock:      &Block{BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 105}}},
		solidityBlock: &Block{BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 100}}},
	}
	fetcher := NewFetcher(fake)

	head, err := fetcher.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(105), head)

	finalized, err := fetcher.FetchFinalizedBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), finalized)
}

// TestFetcher_NotConnected covers the fail-fast on every interface method
// when the fetcher was never connected.
func TestFetcher_NotConnected(t *testing.T) {
	t.Parallel()
	fetcher := &Fetcher{}

	_, err := fetcher.FetchBlock(context.Background(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetcher not connected")

	_, err = fetcher.FetchHighestBlockNumber(context.Background())
	require.Error(t, err)
	_, err = fetcher.FetchFinalizedBlockNumber(context.Background())
	require.Error(t, err)
	_, err = fetcher.FetchBlockHeaderByHash(context.Background(), "h")
	require.Error(t, err)

	assert.NoError(t, fetcher.Close())
}

// TestFetcher_Connect pins the two Connect contracts: no-op with a
// pre-connected client, and a real client build from config fields that
// then serves a fetch against an httptest node.
func TestFetcher_Connect(t *testing.T) {
	t.Parallel()
	client := &fakeTronClient{}
	fetcher := NewFetcher(client)
	require.NoError(t, fetcher.Connect(context.Background()))

	server := newWalletFake(t, func(_ string, _ string, _ []byte) (int, []byte) {
		return 200, []byte(blockJSON("head", 105, 2, "p"))
	})
	cfgFetcher := newTestFetcherFromConfig(t, server.URL)
	require.NoError(t, cfgFetcher.Connect(context.Background()), "config-driven Connect must build the HTTP client")
	head, err := cfgFetcher.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(105), head)
}

// TestFetcher_ConnectContextCancelled pins the dead-context guard at
// Connect time: a config-driven fetcher never reaches the node.
func TestFetcher_ConnectContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfgFetcher, err := NewFetcherFromConfig(&config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://api.trongrid.io"},
	})
	require.NoError(t, err)
	require.Error(t, cfgFetcher.Connect(ctx))
}

// TestNewFetcherFromConfig_NilConfig pins the configuration error.
func TestNewFetcherFromConfig_NilConfig(t *testing.T) {
	t.Parallel()
	_, err := NewFetcherFromConfig(nil)
	require.Error(t, err)
}

// TestFetcher_CloseDelegates pins close delegation.
func TestFetcher_CloseDelegates(t *testing.T) {
	t.Parallel()
	client := &fakeTronClient{}
	fetcher := NewFetcher(client)
	require.NoError(t, fetcher.Close())
	assert.True(t, client.closeCalled)
}

func newTestFetcherFromConfig(t *testing.T, nodeURL string) *Fetcher {
	t.Helper()
	cfg := &config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: nodeURL},
	}
	fetcher, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	return fetcher
}
