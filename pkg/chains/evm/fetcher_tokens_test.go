package evm

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
)

// A block without transactions has no receipts to tie its hash to the node, so in tokens mode the
// node must know the block by that hash. Full mode does not ask.
func TestFetcher_FetchBlock_NoTransactionsUnknownHash(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		tokensMode bool
	}{
		{"tokens mode", true},
		{"full mode", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blockNum := int64(100)
			block := fakeBlock(blockNum)
			var asked string
			client := &fakeRPCClient{
				block:          block,
				receiptsByHash: map[string][]*TransactionReceipt{block.Hash: {}},
				headerByHashFn: func(_ context.Context, hash string) (*ethtypes.Header, error) {
					asked = hash
					return nil, ethereum.NotFound
				},
			}
			f := NewFetcher(client, 8)
			f.tokensMode = tc.tokensMode

			_, err := f.FetchBlock(context.Background(), blockNum)
			if !tc.tokensMode {
				require.NoError(t, err)
				assert.Empty(t, asked)
				return
			}
			require.ErrorIs(t, err, ethereum.NotFound)
			assert.Equal(t, block.Hash, asked)
		})
	}
}

// In tokens mode a block whose transactions or receipts do not rebuild the header's roots fails
// the fetch, so the height is retried. Full mode does not check.
func TestFetcher_FetchBlock_TokensModeChecksRoots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mode    string
		alter   func(block *Block, receipts []*TransactionReceipt) []*TransactionReceipt
		wantErr bool
	}{
		{name: "matching answers", mode: config.ChainModeTokens},
		{
			name: "receipt altered", mode: config.ChainModeTokens, wantErr: true,
			alter: func(_ *Block, receipts []*TransactionReceipt) []*TransactionReceipt {
				receipts[1].GethReceipt.Status = ethtypes.ReceiptStatusFailed
				return receipts
			},
		},
		{
			name: "transaction missing", mode: config.ChainModeTokens, wantErr: true,
			alter: func(block *Block, receipts []*TransactionReceipt) []*TransactionReceipt {
				block.GethTransactions = block.GethTransactions[:1]
				return receipts
			},
		},
		{
			name: "full mode, receipt altered", mode: config.ChainModeFull,
			alter: func(_ *Block, receipts []*TransactionReceipt) []*TransactionReceipt {
				receipts[1].GethReceipt.Status = ethtypes.ReceiptStatusFailed
				return receipts
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle := gethBundle(t, 12345, nil, nil)
			block, receipts := bundle.Block, bundle.Receipts
			if tt.alter != nil {
				receipts = tt.alter(block, receipts)
			}
			f, err := NewFetcherFromConfig(&config.Config{Chain: config.ChainConfig{Mode: tt.mode}})
			require.NoError(t, err)
			f.client = &fakeRPCClient{block: block, receiptsByHash: map[string][]*TransactionReceipt{block.Hash: receipts}}

			_, err = f.FetchBlock(context.Background(), 12345)
			if tt.wantErr {
				require.ErrorIs(t, err, errRootMismatch)
				return
			}
			require.NoError(t, err)
		})
	}
}
