package evm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
)

// TestFetcher_MergesReceiptOutcomeOntoTransactions proves the merge step in
// fetchTransactionsAndReceipts: the execution results recorded in the receipts
// overwrite the signed payload's conversion defaults on both receipt paths,
// and the converter's transaction doc map carries the merged values. An empty
// receipt status string is pinned explicitly as failed, since only the exact
// successful marker may classify a transaction as successful.
func TestFetcher_MergesReceiptOutcomeOntoTransactions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		fallback      bool
		receiptStatus string
		wantStatus    bool
	}{
		{name: "BatchPathSuccessful", receiptStatus: "1", wantStatus: true},
		{name: "BatchPathFailed", receiptStatus: "0", wantStatus: false},
		{name: "FallbackPathSuccessful", fallback: true, receiptStatus: "1", wantStatus: true},
		{name: "FallbackPathFailed", fallback: true, receiptStatus: "0", wantStatus: false},
		{name: "EmptyStatusIsFailed", receiptStatus: "", wantStatus: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			txHash := fakeHash("merge-" + tc.name)
			block := fakeBlockWithTxs(12345, fakeTx(txHash))

			receipt := fakeReceipt(txHash, 12345)
			receipt.BlockHash = block.Hash
			receipt.Status = tc.receiptStatus
			receipt.GasUsed = "53000"
			receipt.CumulativeGasUsed = "79000"
			receipt.EffectiveGasPrice = "20000000000"

			client := &fakeRPCClient{block: block}
			if tc.fallback {
				client.batchErr = fmt.Errorf("eth_getBlockReceipts not supported")
				client.txReceiptFn = func(_ context.Context, _ string) (*TransactionReceipt, error) {
					return receipt, nil
				}
			} else {
				client.receiptsByHash = map[string][]*TransactionReceipt{block.Hash: {receipt}}
			}
			f := NewFetcher(client, 8)

			raw, err := f.FetchBlock(context.Background(), 12345)
			require.NoError(t, err)
			bundle, ok := raw.(*BlockBundle)
			require.True(t, ok)

			require.Len(t, bundle.Transactions, 1)
			tx := bundle.Transactions[0]
			assert.Equal(t, tc.wantStatus, tx.Status)
			assert.Equal(t, "53000", tx.GasUsed)
			assert.Equal(t, "79000", tx.CumulativeGasUsed)
			assert.Equal(t, "20000000000", tx.EffectiveGasPrice)

			c := NewConverter(testConfig())
			result, err := c.Convert(context.Background(), bundle)
			require.NoError(t, err)

			var txGroup *chains.DocumentGroup
			for i := range result.Groups {
				if result.Groups[i].Collection == c.collections.Transaction {
					txGroup = &result.Groups[i]
					break
				}
			}
			require.NotNil(t, txGroup, "transaction group should exist")
			require.Len(t, txGroup.Docs, 1)
			doc := txGroup.Docs[0]
			assert.Equal(t, tc.wantStatus, doc[StatusFieldName])
			assert.Equal(t, "79000", doc[CumulativeGasUsedFieldName])
			assert.Equal(t, "20000000000", doc[EffectiveGasPriceFieldName])
		})
	}
}

// TestFetcher_Merge_MissingReceiptKeepsDefaults pins the lenient miss
// behavior: a transaction whose receipt is absent keeps its conversion
// defaults and the block still indexes, mirroring the lenient handling of
// receiptless bundles. The batch path serves a receipt for only one of the
// two transactions; the hash-pinned verification still accepts the result.
func TestFetcher_Merge_MissingReceiptKeepsDefaults(t *testing.T) {
	t.Parallel()

	hashA := fakeHash("merge-miss-matched")
	hashB := fakeHash("merge-miss-unmatched")
	txA := fakeTx(hashA)
	txB := fakeTx(hashB)
	txB.Status = true // the conversion default, pre-set so "untouched" is observable
	block := fakeBlockWithTxs(12345, txA, txB)

	receipt := fakeReceipt(hashA, 12345)
	receipt.BlockHash = block.Hash
	receipt.Status = "0"
	receipt.GasUsed = "53000"
	receipt.CumulativeGasUsed = "79000"
	receipt.EffectiveGasPrice = "20000000000"

	client := &fakeRPCClient{
		block:          block,
		receiptsByHash: map[string][]*TransactionReceipt{block.Hash: {receipt}},
	}
	f := NewFetcher(client, 8)

	raw, err := f.FetchBlock(context.Background(), 12345)
	require.NoError(t, err, "a missing receipt is logged, not failed")
	bundle, ok := raw.(*BlockBundle)
	require.True(t, ok)

	require.Len(t, bundle.Transactions, 2)
	assert.False(t, bundle.Transactions[0].Status)
	assert.Equal(t, "53000", bundle.Transactions[0].GasUsed)
	assert.Equal(t, "79000", bundle.Transactions[0].CumulativeGasUsed)
	assert.Equal(t, "20000000000", bundle.Transactions[0].EffectiveGasPrice)

	assert.True(t, bundle.Transactions[1].Status, "unmatched transaction keeps its conversion default")
	assert.Empty(t, bundle.Transactions[1].GasUsed)
	assert.Empty(t, bundle.Transactions[1].CumulativeGasUsed)
	assert.Empty(t, bundle.Transactions[1].EffectiveGasPrice)
}

// TestMergeReceiptsOntoTransactions_NilRobustness exercises the helper
// directly with nil transactions and nil receipts: both are skipped without
// altering any transaction.
func TestMergeReceiptsOntoTransactions_NilRobustness(t *testing.T) {
	t.Parallel()

	tx := fakeTx(fakeHash("merge-nil"))
	tx.Status = true

	mergeReceiptsOntoTransactions([]*Transaction{nil, &tx}, []*TransactionReceipt{nil})

	assert.True(t, tx.Status)
	assert.Empty(t, tx.GasUsed)
	assert.Empty(t, tx.CumulativeGasUsed)
	assert.Empty(t, tx.EffectiveGasPrice)
}
