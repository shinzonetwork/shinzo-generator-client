package tron

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

// TestFetchBlock_ReceiptCoverageRequired pins the coverage gate: java-tron
// saves one receipt per transaction when it applies a block, so a served
// list that is short or empty means the node does not have the receipts
// (transHistory.switch off, or a node without the block). The fetch must
// fail rather than bundle transactions with zero fee and no logs or
// internal transfers, and the error must contain no "not found" so the
// processor treats it as a real failure to retry.
func TestFetchBlock_ReceiptCoverageRequired(t *testing.T) {
	t.Parallel()
	ts := int64(1777445121000)

	tests := []struct {
		name        string
		txIDs       []string
		receipts    []*TransactionInfo
		wantErrText string
	}{
		{
			name:  "every transaction has a receipt",
			txIDs: []string{"t1", "t2"},
			receipts: []*TransactionInfo{
				{ID: "t1", BlockNumber: 10, BlockTimeStamp: ts},
				{ID: "t2", BlockNumber: 10, BlockTimeStamp: ts},
			},
		},
		{
			name:        "short list leaves one transaction uncovered",
			txIDs:       []string{"t1", "t2"},
			receipts:    []*TransactionInfo{{ID: "t1", BlockNumber: 10, BlockTimeStamp: ts}},
			wantErrText: "receipts cover 1 of 2 transactions in block 10; tx t2 has no receipt",
		},
		{
			name:        "empty list serves no receipts at all",
			txIDs:       []string{"t1"},
			receipts:    nil,
			wantErrText: "receipts cover 0 of 1 transactions in block 10; tx t1 has no receipt",
		},
		{
			name:        "nil receipt slot counts as missing",
			txIDs:       []string{"t1"},
			receipts:    []*TransactionInfo{nil},
			wantErrText: "tx t1 has no receipt",
		},
		{
			name:  "transaction id match is case-insensitive",
			txIDs: []string{"t1"},
			receipts: []*TransactionInfo{
				{ID: "T1", BlockNumber: 10, BlockTimeStamp: ts},
			},
		},
		{
			name:     "zero-transaction block needs no receipts",
			txIDs:    nil,
			receipts: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			block := &Block{BlockID: "b1", BlockHeader: BlockHeader{RawData: BlockHeaderRawData{Number: 10, Timestamp: ts}}}
			for _, id := range tt.txIDs {
				block.Transactions = append(block.Transactions, Transaction{TxID: id})
			}
			fake := &fakeTronClient{blockByNum: block, receipts: tt.receipts}
			fetcher := NewFetcher(fake)

			raw, err := fetcher.FetchBlock(context.Background(), 10)
			if tt.wantErrText == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrText)
			assert.NotContains(t, err.Error(), "not found", "a receipt shortage must not look like a missing block")
			assert.Nil(t, raw, "a failed fetch must not return a bundle")
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

// TestFetcher_Connect pins the Connect contracts: no-op with a
// pre-connected client, and a real client build from config fields that
// passes the genesis pin (the fake serves the Mainnet genesis for block 0)
// and then serves a fetch against the same httptest node.
func TestFetcher_Connect(t *testing.T) {
	t.Parallel()
	client := &fakeTronClient{}
	fetcher := NewFetcher(client)
	require.NoError(t, fetcher.Connect(context.Background()))

	server := newWalletFake(t, func(path string, _ http.Header, _ []byte) (int, []byte) {
		switch path {
		case endpointGetBlockByNum:
			return 200, []byte(blockJSON(genesisBlockIDs["Mainnet"], 0, 0, "genesis-parent"))
		case endpointGetNowBlock:
			return 200, []byte(blockJSON("head", 105, 2, "p"))
		default:
			return 404, []byte(`{}`)
		}
	})
	cfgFetcher := newTestFetcherFromConfig(t, server.URL)
	require.NoError(t, cfgFetcher.Connect(context.Background()), "config-driven Connect must build the HTTP client")
	head, err := cfgFetcher.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(105), head)
}

// TestFetcher_Connect_ChecksGenesis pins the network pin: Connect fetches
// block 0 and refuses to connect when the endpoint's genesis blockID is not
// the configured network's, leaving the fetcher unconnected so a retry
// re-runs the whole check. The wallet endpoints are network-agnostic, so
// without this pin a Mainnet config pointed at a Nile node would index Nile
// blocks under the Tron__Mainnet collections.
func TestFetcher_Connect_ChecksGenesis(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		network       string
		genesisStatus int
		genesisBody   string
		wantErr       error
		wantErrText   string
		wantNoRequest bool
		wantConnected bool
	}{
		{
			name:          "endpoint serves the configured network",
			network:       "Mainnet",
			genesisStatus: http.StatusOK,
			genesisBody:   blockJSON(genesisBlockIDs["Mainnet"], 0, 0, "genesis-parent"),
			wantConnected: true,
		},
		{
			name:          "endpoint serves another network",
			network:       "Mainnet",
			genesisStatus: http.StatusOK,
			genesisBody:   blockJSON(genesisBlockIDs["Nile"], 0, 0, "genesis-parent"),
			wantErr:       errNetworkMismatch,
			wantErrText:   genesisBlockIDs["Nile"],
			wantConnected: false,
		},
		{
			name:          "block 0 unknown to the node",
			network:       "Mainnet",
			genesisStatus: http.StatusOK,
			genesisBody:   `{}`,
			wantErrText:   "read genesis block",
			wantConnected: false,
		},
		{
			name:          "genesis read fails at transport level",
			network:       "Mainnet",
			genesisStatus: http.StatusInternalServerError,
			wantErrText:   "read genesis block",
			wantConnected: false,
		},
		{
			name:          "unsupported network fails closed before any request",
			network:       "Devnet",
			wantErrText:   "Mainnet, Nile, Shasta",
			wantNoRequest: true,
			wantConnected: false,
		},
		{
			name:          "quirky gateway reports a 0x-prefixed uppercase genesis",
			network:       "Nile",
			genesisStatus: http.StatusOK,
			genesisBody:   blockJSON("0x"+strings.ToUpper(genesisBlockIDs["Nile"]), 0, 0, "genesis-parent"),
			wantConnected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var served []string
			server := newWalletFake(t, func(path string, _ http.Header, _ []byte) (int, []byte) {
				mu.Lock()
				served = append(served, path)
				mu.Unlock()
				return tt.genesisStatus, []byte(tt.genesisBody)
			})
			fetcher, err := NewFetcherFromConfig(&config.Config{
				Chain: config.ChainConfig{Name: "Tron", Network: tt.network, Adapter: "tron"},
				Tron:  config.TronConfig{NodeURL: server.URL},
			})
			require.NoError(t, err)

			err = fetcher.Connect(context.Background())

			if tt.wantConnected {
				require.NoError(t, err)
				require.NotNil(t, fetcher.client, "a passing genesis check must retain the client")
				return
			}
			require.Error(t, err)
			require.Nil(t, fetcher.client, "a failed Connect must not retain the client, so a retry re-runs the check")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
			if tt.wantErrText != "" {
				assert.Contains(t, err.Error(), tt.wantErrText)
			}
			mu.Lock()
			paths := append([]string(nil), served...)
			mu.Unlock()
			if tt.wantNoRequest {
				assert.Empty(t, paths, "an unsupported network must fail closed before any request")
				return
			}
			for _, path := range paths {
				assert.Equal(t, endpointGetBlockByNum, path, "Connect must only touch the by-number endpoint")
			}
		})
	}
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

// TestNewFetcherFromConfig_NetworkDefaults pins that an empty chain.network
// reaches Connect as the Tron default, matching the converter's prefix
// fallback: both stages must agree on which network the config names.
func TestNewFetcherFromConfig_NetworkDefaults(t *testing.T) {
	t.Parallel()
	fetcher, err := NewFetcherFromConfig(&config.Config{
		Chain: config.ChainConfig{Name: "Tron", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://api.trongrid.io"},
	})
	require.NoError(t, err)
	assert.Equal(t, defaultNetwork, fetcher.network)
}

// TestNewFetcherFromConfig_APIKeyType pins the tron.api_key_type plumbing:
// the config value reaches the fetcher so Connect can hand it to the client.
func TestNewFetcherFromConfig_APIKeyType(t *testing.T) {
	t.Parallel()
	fetcher, err := NewFetcherFromConfig(&config.Config{
		Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet", Adapter: "tron"},
		Tron:  config.TronConfig{NodeURL: "https://api.trongrid.io", APIKeyType: "X-Api-Key"},
	})
	require.NoError(t, err)
	assert.Equal(t, "X-Api-Key", fetcher.apiKeyType, "NewFetcherFromConfig must carry tron.api_key_type")
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
