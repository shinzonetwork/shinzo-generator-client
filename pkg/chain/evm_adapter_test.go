package chain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name:    "Ethereum",
			Network: "Mainnet",
		},
		Geth: config.GethConfig{},
		Indexer: config.IndexerConfig{
			MaxDocsPerTxn:      1000,
			MaxTxDocsPerBatch:  100,
			MaxLogDocsPerBatch: 100,
			MaxALEDocsPerBatch: 100,
			ReceiptWorkers:     8,
		},
	}
}

func fakeHash(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return "0x" + hex.EncodeToString(h[:])
}

func fakeBlock(num int64) *types.Block {
	return &types.Block{
		Hash:             fakeHash("block-" + big.NewInt(num).String()),
		Number:           "0x" + big.NewInt(num).Text(16),
		Timestamp:        "1640995200",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Difficulty:       "1000000",
		TotalDifficulty:  "1000000",
		GasUsed:          "21000",
		GasLimit:         "8000000",
		Nonce:            "0x0",
		Miner:            "0x0000000000000000000000000000000000000001",
		Size:             "1024",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000001",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		ReceiptsRoot:     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		LogsBloom:        "0x00",
		ExtraData:        "0x",
		MixHash:          "0x0000000000000000000000000000000000000000000000000000000000000000",
	}
}

// fakeRPCClient is a lightweight in-memory rpcClient for testing delegation.
type fakeRPCClient struct {
	latestNum     *big.Int
	latestErr     error
	block         *types.Block
	blockErr      error
	batchReceipts []*types.TransactionReceipt
	batchErr      error
	txReceipt     *types.TransactionReceipt
	txErr         error
	closed        bool
}

func (f *fakeRPCClient) GetLatestBlockNumber(_ context.Context) (*big.Int, error) {
	return f.latestNum, f.latestErr
}

func (f *fakeRPCClient) GetBlockByNumber(_ context.Context, _ *big.Int) (*types.Block, error) {
	return f.block, f.blockErr
}

func (f *fakeRPCClient) GetBlockReceipts(_ context.Context, _ *big.Int) ([]*types.TransactionReceipt, error) {
	return f.batchReceipts, f.batchErr
}

func (f *fakeRPCClient) GetTransactionReceipt(_ context.Context, _ string) (*types.TransactionReceipt, error) {
	return f.txReceipt, f.txErr
}

func (f *fakeRPCClient) Close() error {
	f.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewEVMAdapter(t *testing.T) {
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
			a := newEVMAdapter(cfg, &fakeRPCClient{})

			expected := constants.NewCollectionNames(tc.expectedPrefix)
			assert.Equal(t, expected, a.collections)
			assert.Equal(t, tc.expectedWorkers, a.receiptWorkers)
			assert.NotNil(t, a.signingChan)
		})
	}
}

// ---------------------------------------------------------------------------
// GetSchema / GetCollections (valid before Init)
// ---------------------------------------------------------------------------

func TestEVMAdapter_GetSchema(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newEVMAdapter(cfg, &fakeRPCClient{})

	got, err := a.GetSchema()
	require.NoError(t, err)
	assert.NotEmpty(t, got)

	// For the default prefix the chain-specific schema equals the default schema.
	def, err := schema.GetSchema()
	require.NoError(t, err)
	assert.Equal(t, def, got)
}

func TestEVMAdapter_GetCollections(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newEVMAdapter(cfg, &fakeRPCClient{})

	got := a.GetCollections()
	expected := constants.NewCollectionNames("Ethereum__Mainnet").AllCollections()
	assert.Equal(t, expected, got)
	assert.Contains(t, got, constants.DefaultCollectionPrefix+"__BlockSignature")
	assert.Contains(t, got, constants.DefaultCollectionPrefix+"__SnapshotSignature")
}

// ---------------------------------------------------------------------------
// Pre-Init guards
// ---------------------------------------------------------------------------

func TestEVMAdapter_PreInitGuard(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	a := newEVMAdapter(cfg, &fakeRPCClient{})
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
			return a.FetchAndStoreBlock(ctx, 1)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.call(), ErrAdapterNotInitialized)
		})
	}
}

// ---------------------------------------------------------------------------
// FetchHighestBlockNumber delegation (to rpcClient)
// ---------------------------------------------------------------------------

func TestEVMAdapter_FetchHighestBlockNumber(t *testing.T) {
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
			latestErr:   stderrors.New("failed to get latest header"),
			wantErr:     true,
			errContains: "failed to get latest block number",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			client := &fakeRPCClient{latestNum: tc.latestNum, latestErr: tc.latestErr}
			a := newEVMAdapter(cfg, client)

			n, err := a.FetchHighestBlockNumber(context.Background())
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
// GetHighest/GetLowestStoredBlockNumber/GetDocIDsByBlockRange delegation (to BlockHandler)
// ---------------------------------------------------------------------------

func TestEVMAdapter_StoredBlockNumberDelegation(t *testing.T) {
	t.Parallel()
	td := testutils.SetupTestDefraDB(t)
	cfg := testConfig()
	client := &fakeRPCClient{
		block:     fakeBlock(100),
		batchErr:  stderrors.New("batch receipts unavailable"),
		latestNum: big.NewInt(100),
	}
	a := newEVMAdapter(cfg, client)
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
	require.NoError(t, a.FetchAndStoreBlock(context.Background(), 100))

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
// Close
// ---------------------------------------------------------------------------

func TestEVMAdapter_CloseClosesClient(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	client := &fakeRPCClient{}
	a := newEVMAdapter(cfg, client)

	require.NoError(t, a.Close())
	assert.True(t, client.closed)
}
