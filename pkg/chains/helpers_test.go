package chains

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/types"
)

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

func fakeTx(hash string) types.Transaction {
	return types.Transaction{
		Hash:      hash,
		BlockHash: "0x0000000000000000000000000000000000000000000000000000000000000001",
		From:      "0x0000000000000000000000000000000000000001",
		To:        "0x0000000000000000000000000000000000000002",
		Value:     "0x3e8",
		Gas:       "0x5208",
		GasPrice:  "0x3b9aca00",
		Input:     "0x",
		Nonce:     "0x0",
		Type:      "0x0",
		V:         "0x1b",
		R:         "0x1111111111111111111111111111111111111111111111111111111111111111",
		S:         "0x2222222222222222222222222222222222222222222222222222222222222222",
	}
}

func fakeBlockWithTxs(num int64, txs ...types.Transaction) *types.Block {
	b := fakeBlock(num)
	b.Transactions = txs
	return b
}

func fakeReceipt(txHash string, blockNum int64) *types.TransactionReceipt {
	return &types.TransactionReceipt{
		TransactionHash:   txHash,
		TransactionIndex:  "0x0",
		BlockHash:         "0x0000000000000000000000000000000000000000000000000000000000000001",
		BlockNumber:       "0x" + big.NewInt(blockNum).Text(16),
		From:              "0x0000000000000000000000000000000000000001",
		To:                "0x0000000000000000000000000000000000000002",
		CumulativeGasUsed: "0x5208",
		GasUsed:           "0x5208",
		Status:            "0x1",
	}
}

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

	blockFn     func(ctx context.Context, n *big.Int) (*types.Block, error)
	batchFn     func(ctx context.Context, n *big.Int) ([]*types.TransactionReceipt, error)
	txReceiptFn func(ctx context.Context, hash string) (*types.TransactionReceipt, error)
}

func (f *fakeRPCClient) GetLatestBlockNumber(_ context.Context) (*big.Int, error) {
	return f.latestNum, f.latestErr
}

func (f *fakeRPCClient) GetBlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	if f.blockFn != nil {
		return f.blockFn(ctx, n)
	}
	return f.block, f.blockErr
}

func (f *fakeRPCClient) GetBlockReceipts(ctx context.Context, n *big.Int) ([]*types.TransactionReceipt, error) {
	if f.batchFn != nil {
		return f.batchFn(ctx, n)
	}
	return f.batchReceipts, f.batchErr
}

func (f *fakeRPCClient) GetTransactionReceipt(ctx context.Context, hash string) (*types.TransactionReceipt, error) {
	if f.txReceiptFn != nil {
		return f.txReceiptFn(ctx, hash)
	}
	return f.txReceipt, f.txErr
}

func (f *fakeRPCClient) Close() error {
	f.closed = true
	return nil
}
