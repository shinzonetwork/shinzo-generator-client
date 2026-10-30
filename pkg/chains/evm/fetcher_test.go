package evm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	pkgerrors "github.com/shinzonetwork/shinzo-generator-client/pkg/errors"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewFetcher(t *testing.T) {
	t.Parallel()
	client := &fakeRPCClient{}
	f := NewFetcher(client, 8)
	assert.NotNil(t, f.client)
	assert.Equal(t, 8, f.receiptWorkers)
}

// ---------------------------------------------------------------------------
// NewFetcherFromConfig
// ---------------------------------------------------------------------------

func TestNewFetcherFromConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		cfg             *config.Config
		wantErr         bool
		wantReceipt     int
		wantNodeURL     string
		wantAPIKey      string
		wantAPIKeyType  string
		wantDialTimeout time.Duration
	}{
		{
			name:    "NilConfig",
			cfg:     nil,
			wantErr: true,
		},
		{
			name: "DefaultReceiptWorkers",
			cfg: &config.Config{
				Chain:   config.ChainConfig{Name: "Ethereum", Network: "Mainnet"},
				Geth:    config.GethConfig{NodeURL: "http://localhost:8545"},
				Indexer: config.IndexerConfig{},
			},
			wantReceipt: 16,
			wantNodeURL: "http://localhost:8545",
		},
		{
			name: "CustomReceiptWorkers",
			cfg: &config.Config{
				Chain:   config.ChainConfig{Name: "Ethereum", Network: "Mainnet"},
				Geth:    config.GethConfig{NodeURL: "http://localhost:8545", APIKey: "secret", APIKeyType: "X-Api-Key"},
				Indexer: config.IndexerConfig{ReceiptWorkers: 4},
			},
			wantReceipt:    4,
			wantNodeURL:    "http://localhost:8545",
			wantAPIKey:     "secret",
			wantAPIKeyType: "X-Api-Key",
		},
		{
			name: "DialTimeoutSecondsPropagates",
			cfg: &config.Config{
				Chain:   config.ChainConfig{Name: "Ethereum", Network: "Mainnet"},
				Geth:    config.GethConfig{NodeURL: "http://localhost:8545", DialTimeoutSeconds: 15},
				Indexer: config.IndexerConfig{},
			},
			wantReceipt:     16,
			wantNodeURL:     "http://localhost:8545",
			wantDialTimeout: 15 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := NewFetcherFromConfig(tc.cfg)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, f)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, f)
			assert.Equal(t, tc.wantReceipt, f.receiptWorkers)
			assert.Equal(t, tc.wantNodeURL, f.nodeURL)
			assert.Equal(t, tc.wantAPIKey, f.apiKey)
			assert.Equal(t, tc.wantAPIKeyType, f.apiKeyType)
			assert.Equal(t, tc.wantDialTimeout, f.dialTimeout)
			assert.Nil(t, f.client, "should not have a connected client")
		})
	}
}

// ---------------------------------------------------------------------------
// Connect
// ---------------------------------------------------------------------------

func TestFetcher_Connect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		setup func() *Fetcher
	}{
		{
			name:  "NoOpWhenClientAlreadySet",
			setup: func() *Fetcher { return NewFetcher(&fakeRPCClient{}, 8) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := tc.setup()
			err := f.Connect(context.Background())
			require.NoError(t, err)
		})
	}
}

// TestFetcher_Connect_PropagatesCtx proves the context survives the
// Connect → NewEthereumClient seam: dialing a hanging WebSocket server
// (accepts the TCP connection, never completes the handshake) is aborted by
// the context deadline instead of blocking until OS-level timeouts.
func TestFetcher_Connect_PropagatesCtx(t *testing.T) {
	t.Parallel()
	wsServer := newHangingWSServer()
	defer wsServer.Close()
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")

	cfg := testConfig()
	cfg.Geth.WsURL = wsURL
	f, err := NewFetcherFromConfig(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = f.Connect(ctx)
	elapsed := time.Since(start)

	assert.Error(t, err)
	assertDeadlineError(t, err)
	assert.Less(t, elapsed, 5*time.Second, "Connect must abort on the context deadline, not on OS-level timeouts")
}

// ---------------------------------------------------------------------------
// Nil-client guards
// ---------------------------------------------------------------------------

func TestFetcher_NotConnectedGuards(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		fn   func(f *Fetcher) error
	}{
		{
			name: "FetchBlock",
			fn:   func(f *Fetcher) error { _, err := f.FetchBlock(context.Background(), 1); return err },
		},
		{
			name: "FetchHighestBlockNumber",
			fn:   func(f *Fetcher) error { _, err := f.FetchHighestBlockNumber(context.Background()); return err },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &Fetcher{}
			err := tc.fn(f)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "Connect")
		})
	}
}

// ---------------------------------------------------------------------------
// FetchHighestBlockNumber (moved from adapter_test.go)
// ---------------------------------------------------------------------------

func TestFetcher_FetchHighestBlockNumber(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		latestNum   *big.Int
		latestErr   error
		wantResult  int64
		wantErr     bool
		errContains string
	}{
		{
			name:       "DelegatesToClient",
			latestNum:  big.NewInt(123456),
			wantResult: 123456,
		},
		{
			name:        "ClientError",
			latestErr:   errors.New("failed to get latest header"),
			wantErr:     true,
			errContains: "failed to get latest block number",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeRPCClient{latestNum: tc.latestNum, latestErr: tc.latestErr}
			f := NewFetcher(client, 8)

			n, err := f.FetchHighestBlockNumber(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantResult, n)
		})
	}
}

// ---------------------------------------------------------------------------
// FetchBlock: happy path with batch receipts
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_BatchSuccess(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blockNum := int64(12345)
	block := fakeBlockWithTxs(blockNum, fakeTx(txHash))
	// The receipt must carry the served block's hash for the pinned batch
	// path to accept it.
	receipt := fakeGethReceipt(txHash, blockNum)
	receipt.BlockHash = common.HexToHash(block.Hash)

	client := &fakeRPCClient{
		block:          block,
		receiptsByHash: map[string][]*ethtypes.Receipt{block.Hash: {receipt}},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), blockNum)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.Equal(t, block, bundle.Block)
	assert.Len(t, bundle.Transactions, 1)
	assert.Equal(t, txHash, bundle.Transactions[0].Hash)
	assert.Len(t, bundle.Receipts, 1)
	assert.Equal(t, convertGethReceipt(receipt), bundle.Receipts[0])
}

func TestFetcher_FetchBlock_NoTransactions(t *testing.T) {
	t.Parallel()

	blockNum := int64(100)
	block := fakeBlock(blockNum)

	client := &fakeRPCClient{
		block:          block,
		receiptsByHash: map[string][]*ethtypes.Receipt{block.Hash: {}},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), blockNum)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.Equal(t, block, bundle.Block)
	assert.Empty(t, bundle.Transactions)
	assert.Empty(t, bundle.Receipts)
}

func TestFetcher_FetchBlock_ReceiptHashMismatchFallsBack(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blockNum := int64(12345)
	block := fakeBlockWithTxs(blockNum, fakeTx(txHash))

	// A receipt served for a different block than the one the fetcher
	// holds: the pinned batch path must reject it instead of bundling
	// cross-branch content, and the tx-pinned fallback must win.
	foreignReceipt := fakeGethReceipt(txHash, blockNum)
	foreignReceipt.BlockHash = common.HexToHash(fakeHash("foreign-block"))
	fallbackReceipt := fakeGethReceipt(txHash, blockNum)
	fallbackReceipt.BlockHash = common.HexToHash(block.Hash)

	client := &fakeRPCClient{
		block:          block,
		receiptsByHash: map[string][]*ethtypes.Receipt{block.Hash: {foreignReceipt}},
		txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
			return fallbackReceipt, nil
		},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), blockNum)
	require.NoError(t, err)

	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)
	assert.Equal(t, []*TransactionReceipt{convertGethReceipt(fallbackReceipt)}, bundle.Receipts,
		"receipts belonging to another block must not land in the bundle; the tx-pinned fallback serves them instead")
}

func TestFetcher_FetchBlock_FallbackRejectsReceiptsFromAnotherBlock(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	blockNum := int64(12345)
	block := fakeBlockWithTxs(blockNum, fakeTx(txHash))

	// Our block was orphaned and the tx re-included: both the pinned batch
	// and the by-tx-hash lookup serve the canonical block's receipt.
	canonical := fakeGethReceipt(txHash, blockNum)
	canonical.BlockHash = common.HexToHash(fakeHash("canonical-block"))
	client := &fakeRPCClient{
		block:          block,
		receiptsByHash: map[string][]*ethtypes.Receipt{block.Hash: {canonical}},
		txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
			return canonical, nil
		},
	}
	f := NewFetcher(client, 8)

	_, err := f.FetchBlock(context.Background(), blockNum)
	require.Error(t, err, "receipts from another block must fail the fetch, not be bundled")
}

func TestFetcher_FetchBlock_ChecksReceiptsRoot(t *testing.T) {
	t.Parallel()

	hashA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	blockNum := int64(12345)
	failedB := func() *ethtypes.Receipt {
		r := fakeGethReceipt(hashB, blockNum)
		r.Status = ethtypes.ReceiptStatusFailed
		return r
	}

	cases := []struct {
		name string
		// batch is what eth_getBlockReceipts serves; nil means the call is unsupported.
		batch   []*ethtypes.Receipt
		byTx    map[string]*ethtypes.Receipt
		wantErr error
	}{
		{
			name:  "BatchDropsReceipt",
			batch: []*ethtypes.Receipt{fakeGethReceipt(hashA, blockNum)},
			byTx:  map[string]*ethtypes.Receipt{hashA: fakeGethReceipt(hashA, blockNum), hashB: fakeGethReceipt(hashB, blockNum)},
		},
		{
			name:  "BatchAltersReceipt",
			batch: []*ethtypes.Receipt{fakeGethReceipt(hashA, blockNum), failedB()},
			byTx:  map[string]*ethtypes.Receipt{hashA: fakeGethReceipt(hashA, blockNum), hashB: fakeGethReceipt(hashB, blockNum)},
		},
		{
			name:    "FallbackAltersReceipt",
			byTx:    map[string]*ethtypes.Receipt{hashA: fakeGethReceipt(hashA, blockNum), hashB: failedB()},
			wantErr: errRootMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			block := fakeBlockWithTxs(blockNum, fakeTx(hashA), fakeTx(hashB))
			client := &fakeRPCClient{
				block: block,
				txReceiptFn: func(_ context.Context, hash string) (*ethtypes.Receipt, error) {
					return tc.byTx[hash], nil
				},
			}
			if tc.batch == nil {
				client.batchErr = fmt.Errorf("eth_getBlockReceipts not supported")
			} else {
				client.receiptsByHash = map[string][]*ethtypes.Receipt{block.Hash: tc.batch}
			}

			raw, err := NewFetcher(client, 8).FetchBlock(context.Background(), blockNum)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			bundle, ok := raw.(*BlockBundle)
			require.True(t, ok)
			assert.Equal(t, convertGethReceipts([]*ethtypes.Receipt{tc.byTx[hashA], tc.byTx[hashB]}), bundle.Receipts,
				"a batch that does not rebuild the receipts root must give way to the per-transaction receipts")
		})
	}
}

// ---------------------------------------------------------------------------
// FetchBlock: block not found (no retry, error passed through unwrapped)
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_BlockNotFound(t *testing.T) {
	t.Parallel()

	errNotFound := errors.New("block not found")
	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*Block, error) {
			calls++
			return nil, errNotFound
		},
	}
	f := NewFetcher(client, 8)

	_, err := f.FetchBlock(context.Background(), 42)
	require.Error(t, err)
	assert.Equal(t, errNotFound, err, "not-found error should pass through unwrapped")
	assert.Equal(t, 1, calls, "should not retry on not-found errors")
}

// ---------------------------------------------------------------------------
// FetchBlock: RPC error — no retry at fetcher level
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_RPCErrorNoRetry(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*Block, error) {
			calls++
			return nil, errors.New("connection refused")
		},
	}
	f := NewFetcher(client, 8)

	_, err := f.FetchBlock(context.Background(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
	assert.NotContains(t, err.Error(), "failed to fetch block")
	assert.Equal(t, 1, calls, "should make exactly 1 call — no retry at fetcher level")
}

// ---------------------------------------------------------------------------
// FetchBlock: pre-canceled context
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlock_ContextAlreadyCanceled(t *testing.T) {
	t.Parallel()

	calls := 0
	client := &fakeRPCClient{
		blockFn: func(_ context.Context, _ *big.Int) (*Block, error) {
			calls++
			return fakeBlock(1), nil
		},
	}
	f := NewFetcher(client, 8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.FetchBlock(ctx, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, calls, "should not call RPC with pre-canceled context")
}

// ---------------------------------------------------------------------------
// Receipt fallback (moved from adapter_test.go — now pure unit tests)
// ---------------------------------------------------------------------------

func TestFetcher_ReceiptFallback(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC := "0xccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	cases := []struct {
		name           string
		blockNum       int64
		txs            []Transaction
		txReceiptFn    func(_ context.Context, _ string) (*ethtypes.Receipt, error)
		wantErr        bool
		wantErrContain string
		wantReceipts   int
	}{
		{
			name:     "NoTxns",
			blockNum: 100000,
		},
		{
			name:     "IndividualSuccess",
			blockNum: 100000,
			txs:      []Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
				return fakeGethReceipt(txHash, 100000), nil
			},
			wantReceipts: 1,
		},
		{
			name:     "IndividualFail",
			blockNum: 100001,
			txs:      []Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
				return nil, fmt.Errorf("receipt not available")
			},
			wantErr:        true,
			wantErrContain: "1/1 transaction receipts could not be fetched",
		},
		{
			name:     "IndividualReceiptSuccess",
			blockNum: 0xccc0,
			txs:      []Transaction{fakeTx(txHash)},
			txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
				return fakeGethReceipt(txHash, 0xccc0), nil
			},
			wantReceipts: 1,
		},
		{
			name:     "PartialFailure",
			blockNum: 100003,
			txs:      []Transaction{fakeTx(hashA), fakeTx(hashB), fakeTx(hashC)},
			txReceiptFn: func(_ context.Context, hash string) (*ethtypes.Receipt, error) {
				if hash == hashB {
					return nil, fmt.Errorf("receipt not available")
				}
				return fakeGethReceipt(hash, 100003), nil
			},
			wantErr:        true,
			wantErrContain: "1/3 transaction receipts could not be fetched",
		},
		{
			name:     "FullFailure",
			blockNum: 100004,
			txs:      []Transaction{fakeTx(hashA), fakeTx(hashB)},
			txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
				return nil, fmt.Errorf("rpc error")
			},
			wantErr:        true,
			wantErrContain: "2/2 transaction receipts could not be fetched",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			block := fakeBlock(tc.blockNum)
			if len(tc.txs) > 0 {
				block = fakeBlockWithTxs(tc.blockNum, tc.txs...)
			}

			client := &fakeRPCClient{
				block:       block,
				batchErr:    fmt.Errorf("eth_getBlockReceipts not supported"),
				txReceiptFn: tc.txReceiptFn,
			}
			f := NewFetcher(client, 8)

			raw, err := f.FetchBlock(context.Background(), tc.blockNum)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContain)
				assert.Nil(t, raw)
				return
			}
			require.NoError(t, err)

			bundle, ok := raw.(*BlockBundle)
			require.True(t, ok)
			assert.Equal(t, block, bundle.Block)
			assert.Len(t, bundle.Receipts, tc.wantReceipts)
		})
	}
}

// ---------------------------------------------------------------------------
// Receipt fallback: positional nil preservation
// ---------------------------------------------------------------------------

func TestFetcher_ReceiptFallback_PreservesPositionalNils(t *testing.T) {
	t.Parallel()

	hashA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC := "0xccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	block := fakeBlockWithTxs(100003, fakeTx(hashA), fakeTx(hashB), fakeTx(hashC))

	client := &fakeRPCClient{
		block:    block,
		batchErr: fmt.Errorf("eth_getBlockReceipts not supported"),
		txReceiptFn: func(_ context.Context, hash string) (*ethtypes.Receipt, error) {
			if hash == hashB {
				return nil, fmt.Errorf("receipt not available")
			}
			return fakeGethReceipt(hash, 100003), nil
		},
	}
	f := NewFetcher(client, 8)

	txs, receipts, err := f.fetchTransactionsAndReceipts(context.Background(), block, 100003)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1/3 transaction receipts could not be fetched")

	assert.Len(t, txs, 3)
	assert.Len(t, receipts, 3, "positional slice must not be collapsed")
	assert.NotNil(t, receipts[0])
	assert.Nil(t, receipts[1], "failed receipt must remain as nil in position")
	assert.NotNil(t, receipts[2])
}

// ---------------------------------------------------------------------------
// Context cancellation during receipt fetch (moved from adapter_test.go)
// ---------------------------------------------------------------------------

func TestFetcher_ReceiptFallback_ContextCancelTiming(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name        string
		blockNum    int64
		txSleep     time.Duration
		ctxTimeout  time.Duration
		returnError bool
	}{
		{
			name:        "ContextCancel",
			blockNum:    100002,
			txSleep:     2 * time.Second,
			ctxTimeout:  500 * time.Millisecond,
			returnError: true,
		},
		{
			name:        "ContextCancelDuringFetch",
			blockNum:    1000,
			txSleep:     500 * time.Millisecond,
			ctxTimeout:  200 * time.Millisecond,
			returnError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			block := fakeBlockWithTxs(tc.blockNum, fakeTx(txHash))
			client := &fakeRPCClient{
				block:    block,
				batchErr: fmt.Errorf("not supported"),
				txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
					time.Sleep(tc.txSleep)
					if tc.returnError {
						return nil, fmt.Errorf("timeout")
					}
					return fakeGethReceipt(txHash, tc.blockNum), nil
				},
			}
			f := NewFetcher(client, 8)

			ctx, cancel := context.WithTimeout(context.Background(), tc.ctxTimeout)
			defer cancel()

			_, err := f.FetchBlock(ctx, tc.blockNum)
			t.Logf("FetchBlock error: %v", err)
		})
	}
}

func TestFetcher_ReceiptFallback_ContextCancelDuringSemaphoreWait(t *testing.T) {
	t.Parallel()

	txs := make([]Transaction, 3)
	for i := range 3 {
		txs[i] = fakeTx(fmt.Sprintf("0x%064x", i+1))
	}
	block := fakeBlockWithTxs(0xddd0, txs...)

	firstReceiptCalled := make(chan struct{})
	client := &fakeRPCClient{
		block:    block,
		batchErr: fmt.Errorf("not supported"),
		txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
			select {
			case firstReceiptCalled <- struct{}{}:
			default:
			}
			time.Sleep(5 * time.Second)
			return nil, fmt.Errorf("timeout")
		},
	}
	f := NewFetcher(client, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := f.FetchBlock(ctx, 0xddd0)
		errCh <- err
	}()

	select {
	case <-firstReceiptCalled:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for first receipt call")
	}

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		t.Logf("FetchBlock error: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for FetchBlock to complete")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestFetcher_Close(t *testing.T) {
	t.Parallel()

	client := &fakeRPCClient{}
	f := NewFetcher(client, 8)

	require.NoError(t, f.Close())
	assert.True(t, client.closed)
}

func TestFetcher_Close_NilClient(t *testing.T) {
	t.Parallel()

	f := &Fetcher{}
	require.NoError(t, f.Close())
}

// ---------------------------------------------------------------------------
// FetchBlockHeaderByHash
// ---------------------------------------------------------------------------

func TestFetcher_FetchBlockHeaderByHash(t *testing.T) {
	t.Parallel()

	var batchReceiptCalls int
	var individualReceiptCalls int
	parentHash := common.HexToHash(fakeHash("walkback-parent"))
	client := &fakeRPCClient{
		headerByHash: &ethtypes.Header{Number: big.NewInt(50), ParentHash: parentHash},
		batchFn: func(_ context.Context, _ string) ([]*ethtypes.Receipt, error) {
			batchReceiptCalls++
			return nil, fmt.Errorf("batch receipts unavailable")
		},
		txReceiptFn: func(_ context.Context, _ string) (*ethtypes.Receipt, error) {
			individualReceiptCalls++
			return nil, fmt.Errorf("receipt unavailable")
		},
	}
	f := NewFetcher(client, 4)

	header, err := f.FetchBlockHeaderByHash(context.Background(), fakeHash("walkback"))
	require.NoError(t, err)
	assert.Equal(t, chains.BlockHeader{Number: 50, Hash: fakeHash("walkback"), ParentHash: parentHash.Hex()}, header,
		"the header fetch must carry only the height, content hash, and parent hash")
	assert.Zero(t, batchReceiptCalls,
		"the header fetch must not call eth_getBlockReceipts: by-number receipts belong to the canonical branch and mix into an orphan descent")
	assert.Zero(t, individualReceiptCalls,
		"the header fetch must not fall back to individual receipt fetches")
}

func TestFetcher_FetchBlockHeaderByHash_NotFoundIsClassified(t *testing.T) {
	t.Parallel()

	client := &fakeRPCClient{headerByHashErr: fmt.Errorf("block not found")}
	f := NewFetcher(client, 4)

	_, err := f.FetchBlockHeaderByHash(context.Background(), fakeHash("missing"))
	require.Error(t, err)
	assert.True(t, pkgerrors.IsErrNotFound(err),
		"the by-hash not-found error must keep its message so IsErrNotFound classifies it for the shared retry logic")
}

func TestFetcher_FetchBlockHeaderByHash_ErrorCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		client     *fakeRPCClient
		hash       string
		wantErrSub string
	}{
		{
			name:       "NotConnected",
			client:     nil,
			hash:       fakeHash("unreachable"),
			wantErrSub: "not connected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var f *Fetcher
			if tt.client == nil {
				f = &Fetcher{}
			} else {
				f = NewFetcher(tt.client, 4)
			}

			_, err := f.FetchBlockHeaderByHash(context.Background(), tt.hash)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrSub)
		})
	}
}

// ---------------------------------------------------------------------------
// FetchFinalizedBlockNumber
// ---------------------------------------------------------------------------

func TestFetcher_FetchFinalizedBlockNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		client     *fakeRPCClient
		wantHeight int64
		wantErrSub string
	}{
		{
			name:       "PassthroughNumber",
			client:     &fakeRPCClient{finalizedNum: 42},
			wantHeight: 42,
		},
		{
			name:       "ClientErrorSurfaces",
			client:     &fakeRPCClient{finalizedErr: fmt.Errorf("finalized header not found")},
			wantErrSub: "finalized header not found",
		},
		{
			name:       "NotConnected",
			client:     nil,
			wantErrSub: "not connected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var f *Fetcher
			if tt.client == nil {
				f = &Fetcher{}
			} else {
				f = NewFetcher(tt.client, 4)
			}

			height, err := f.FetchFinalizedBlockNumber(context.Background())
			if tt.wantErrSub != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSub)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHeight, height)
		})
	}
}

// ---------------------------------------------------------------------------
// MockFetcher Connect
// ---------------------------------------------------------------------------

func TestMockFetcher_Connect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		setupFn func(m *testutils.MockFetcher)
		wantErr bool
		errSub  string
	}{
		{
			name:    "Default",
			setupFn: func(_ *testutils.MockFetcher) {},
		},
		{
			name: "CustomFn",
			setupFn: func(m *testutils.MockFetcher) {
				m.ConnectFn = func(_ context.Context) error { return errors.New("dial failed") }
			},
			wantErr: true,
			errSub:  "dial failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &testutils.MockFetcher{}
			tc.setupFn(m)
			err := m.Connect(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSub)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, 1, m.ConnectCalls)
		})
	}
}
