package evm

import (
	"context"
	stderrors "errors"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewAdapter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		cfgMutator      func(*config.Config)
		expectedPrefix  string
		expectedWorkers int
	}{
		{
			name:            "CollectionsAndPrefix",
			cfgMutator:      func(_ *config.Config) {},
			expectedPrefix:  "Ethereum__Mainnet",
			expectedWorkers: 8,
		},
		{
			name:            "DefaultReceiptWorkers",
			cfgMutator:      func(c *config.Config) { c.Indexer.ReceiptWorkers = 0 },
			expectedPrefix:  "Ethereum__Mainnet",
			expectedWorkers: 16,
		},
		{
			name: "CustomPrefix",
			cfgMutator: func(c *config.Config) {
				c.Chain = config.ChainConfig{Name: "Arbitrum", Network: "One"}
			},
			expectedPrefix:  "Arbitrum__One",
			expectedWorkers: 8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			tc.cfgMutator(cfg)
			a := newAdapter(cfg, &fakeRPCClient{})

			expected := NewCollectionNames(tc.expectedPrefix)
			assert.Equal(t, expected, a.collections)
			assert.Equal(t, tc.expectedWorkers, a.receiptWorkers)
			assert.NotNil(t, a.signingChan)
		})
	}
}

// ---------------------------------------------------------------------------
// GetSchema / GetCollections (valid before Init)
// ---------------------------------------------------------------------------

func TestAdapter_GetSchema(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newAdapter(cfg, &fakeRPCClient{})

	got, err := a.GetSchema()
	require.NoError(t, err)
	assert.NotEmpty(t, got)

	// For the default prefix the chain-specific schema equals the default schema.
	def, err := schema.LoadSchemaSDL(NewCollectionNames("Ethereum__Mainnet"))
	require.NoError(t, err)
	assert.Equal(t, def, got)
}

func TestAdapter_GetCollections(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newAdapter(cfg, &fakeRPCClient{})

	got := a.GetCollections()
	expected := NewCollectionNames("Ethereum__Mainnet").AllCollections()
	assert.Equal(t, expected, got)
	assert.Contains(t, got, DefaultCollectionPrefix+"__BlockSignature")
	assert.Contains(t, got, DefaultCollectionPrefix+"__SnapshotSignature")
}

// ---------------------------------------------------------------------------
// Pre-Init guards
// ---------------------------------------------------------------------------

func TestAdapter_PreInitGuard(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newAdapter(cfg, &fakeRPCClient{})
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"GetHighestStoredBlockNumber", func() error {
			_, err := a.GetHighestStoredBlockNumber(ctx)
			return err
		}},
		{"GetLowestStoredBlockNumber", func() error {
			_, err := a.GetLowestStoredBlockNumber(ctx)
			return err
		}},
		{"GetDocIDsByBlockRange", func() error {
			_, err := a.GetDocIDsByBlockRange(ctx, 1, 10)
			return err
		}},
		{"FetchAndStoreBlock", func() error {
			_, err := a.FetchAndStoreBlock(ctx, 1)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.call(), chains.ErrAdapterNotInitialized)
		})
	}
}

// ---------------------------------------------------------------------------
// GetHighest/GetLowestStoredBlockNumber/GetDocIDsByBlockRange delegation (to BlockHandler)
// ---------------------------------------------------------------------------

func TestAdapter_StoredBlockNumberDelegation(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cfg := testConfig()
	client := &fakeRPCClient{
		block:     fakeBlock(100),
		batchErr:  stderrors.New("batch receipts unavailable"),
		latestNum: big.NewInt(100),
	}
	a := newAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })

	require.NoError(t, a.Init(context.Background(), td.Node))

	// Empty DB before any fetch: stored queries return not-found.
	_, err := a.GetHighestStoredBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = a.GetLowestStoredBlockNumber(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Persist block 100 via the adapter.
	_, err = a.FetchAndStoreBlock(context.Background(), 100)
	require.NoError(t, err)

	highest, err := a.GetHighestStoredBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), highest)

	lowest, err := a.GetLowestStoredBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), lowest)

	// GetDocIDsByBlockRange delegates to BlockHandler and returns the stored
	// Block document for block 100. fakeBlock has no transactions, so only the
	// Block collection is populated. No identity context means no BlockSignature.
	docIDs, err := a.GetDocIDsByBlockRange(context.Background(), 100, 100)
	require.NoError(t, err)
	assert.Contains(t, docIDs, "Ethereum__Mainnet__Block")
	assert.Len(t, docIDs["Ethereum__Mainnet__Block"], 1)
	assert.NotContains(t, docIDs, "Ethereum__Mainnet__SnapshotSignature")

	// Tip still delegates to the fake client.
	tip, err := a.FetchHighestBlockNumber(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(100), tip)
}

// ---------------------------------------------------------------------------
// FetchAndStoreBlock: persistence, duplicate, batch receipts
// ---------------------------------------------------------------------------

func TestAdapter_FetchAndStoreBlock(t *testing.T) {
	t.Parallel()

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	cases := []struct {
		name          string
		blockNum      int64
		txs           []types.Transaction
		batchReceipts []*types.TransactionReceipt
		calls         int
	}{
		{
			name:          "NoTx_Success",
			blockNum:      500,
			batchReceipts: []*types.TransactionReceipt{},
			calls:         1,
		},
		{
			name:          "DuplicateBlock",
			blockNum:      700,
			batchReceipts: []*types.TransactionReceipt{},
			calls:         2,
		},
		{
			name:          "WithTxAndBatchReceipts",
			blockNum:      0xbbb0,
			txs:           []types.Transaction{fakeTx(txHash)},
			batchReceipts: []*types.TransactionReceipt{fakeReceipt(txHash, 0xbbb0)},
			calls:         1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			td := testutils.SetupTestDefraDB(t)
			cfg := testConfig()

			block := fakeBlock(tc.blockNum)
			if len(tc.txs) > 0 {
				block = fakeBlockWithTxs(tc.blockNum, tc.txs...)
			}

			client := &fakeRPCClient{
				block:         block,
				batchReceipts: tc.batchReceipts,
			}
			a := newAdapter(cfg, client)
			t.Cleanup(func() { _ = a.Close() })
			require.NoError(t, a.Init(context.Background(), td.Node))

			for range tc.calls {
				_, err := a.FetchAndStoreBlock(context.Background(), tc.blockNum)
				require.NoError(t, err)
			}

			highest, err := a.GetHighestStoredBlockNumber(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.blockNum, highest)
		})
	}
}

// ---------------------------------------------------------------------------
// Context cancellation during batch creation
// ---------------------------------------------------------------------------

func TestAdapter_FetchAndStoreBlock_ContextCancelDuringBatch(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cfg := testConfig()
	client := &fakeRPCClient{
		block:         fakeBlock(0xdead),
		batchReceipts: []*types.TransactionReceipt{},
	}
	a := newAdapter(cfg, client)
	t.Cleanup(func() { _ = a.Close() })
	require.NoError(t, a.Init(context.Background(), td.Node))

	// First call: block persists.
	_, err := a.FetchAndStoreBlock(context.Background(), 0xdead)
	require.NoError(t, err)

	// Second call with pre-canceled ctx — either already-exists or ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = a.FetchAndStoreBlock(ctx, 0xdead)
	if err != nil {
		assert.Error(t, err)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestAdapter_CloseClosesClient(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	client := &fakeRPCClient{}
	a := newAdapter(cfg, client)

	require.NoError(t, a.Close())
	assert.True(t, client.closed)
}
