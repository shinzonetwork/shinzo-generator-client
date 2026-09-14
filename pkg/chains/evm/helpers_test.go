package evm

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/websocket"
	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
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

func fakeBlock(num int64) *Block {
	return &Block{
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

func fakeTx(hash string) Transaction {
	return Transaction{
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

func fakeBlockWithTxs(num int64, txs ...Transaction) *Block {
	b := fakeBlock(num)
	b.Transactions = txs
	return b
}

func fakeReceipt(txHash string, blockNum int64) *TransactionReceipt {
	return &TransactionReceipt{
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

// storeTestBlockDoc writes a single block document through the same write
// path used in production (buildBlockData → NewDocFromMap → AddDocument).
func storeTestBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, num int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	doc, err := client.NewDocFromMap(ctx, c.buildBlockData(fakeBlock(num), num), col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}

// storeResidueBlockDoc writes a block document without a number field,
// simulating the numberless rows left behind by purge residue or P2P
// replication that the block-number queries must exclude. The seed keeps the
// content unique — DefraDB derives DocIDs from document content, so identical
// residue documents would collide on the same DocID.
func storeResidueBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, seed int64) string {
	t.Helper()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)

	data := c.buildBlockData(fakeBlock(seed), seed)
	delete(data, NumberFieldValue)

	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)

	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())

	return doc.ID().String()
}

type fakeRPCClient struct {
	latestNum     *big.Int
	latestErr     error
	block         *Block
	blockErr      error
	batchReceipts []*TransactionReceipt
	batchErr      error
	txReceipt     *TransactionReceipt
	txErr         error
	closed        bool

	blockFn     func(ctx context.Context, n *big.Int) (*Block, error)
	batchFn     func(ctx context.Context, n *big.Int) ([]*TransactionReceipt, error)
	txReceiptFn func(ctx context.Context, hash string) (*TransactionReceipt, error)
}

func (f *fakeRPCClient) GetLatestBlockNumber(_ context.Context) (*big.Int, error) {
	return f.latestNum, f.latestErr
}

func (f *fakeRPCClient) GetBlockByNumber(ctx context.Context, n *big.Int) (*Block, error) {
	if f.blockFn != nil {
		return f.blockFn(ctx, n)
	}
	return f.block, f.blockErr
}

func (f *fakeRPCClient) GetBlockReceipts(ctx context.Context, n *big.Int) ([]*TransactionReceipt, error) {
	if f.batchFn != nil {
		return f.batchFn(ctx, n)
	}
	return f.batchReceipts, f.batchErr
}

func (f *fakeRPCClient) GetTransactionReceipt(ctx context.Context, hash string) (*TransactionReceipt, error) {
	if f.txReceiptFn != nil {
		return f.txReceiptFn(ctx, hash)
	}
	return f.txReceipt, f.txErr
}

func (f *fakeRPCClient) Close() error {
	f.closed = true
	return nil
}

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// --- EthereumClient test infrastructure (shared across client test files) ---

// ethGetBlockByNumber is used in multiple tests, so define it as a constant for easy updates if needed.
const ethGetBlockByNumber = "eth_getBlockByNumber"

// ethGetTransactionReceipt is used in multiple tests, so define it as a constant for easy updates if needed.
const ethGetTransactionReceipt = "eth_getTransactionReceipt"

// ethGetBlockReceipts is used in multiple tests, so define it as a constant for easy updates if needed.
const ethGetBlockReceipts = "eth_getBlockReceipts"

type jsonRPCRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     any             `json:"id"`
}

func newMockRPCServer(handler func(method string, params json.RawMessage) (any, error)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		result, err := handler(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32000, "message": err.Error()},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func simpleRPCServer() *httptest.Server {
	return newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "eth_chainId", "net_version":
			return "0x1", nil
		default:
			return "0x1", nil
		}
	})
}

func fullBlockResponse(number string, txs []any) map[string]any {
	// Empty trie root hash — must match empty transaction list
	emptyTrieRoot := "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
	block := map[string]any{
		NumberFieldValue:         number,
		"hash":                   "0x0000000000000000000000000000000000000000000000000000000000000001",
		ParentHashKeyValue:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		NonceKeyValue:            "0x0000000000000000",
		Sha3UnclesKeyValue:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		LogsBloomKeyValue:        "0x" + fmt.Sprintf("%0512x", 0),
		TransactionsRootKeyValue: emptyTrieRoot,
		StateRootKeyValue:        "0x0000000000000000000000000000000000000000000000000000000000000000",
		ReceiptsRootKeyValue:     "0x0000000000000000000000000000000000000000000000000000000000000000",
		MinerKeyValue:            "0x0000000000000000000000000000000000000000",
		DifficultyKeyValue:       "0x0",
		"totalDifficulty":        "0x0",
		ExtraDataKeyValue:        "0x",
		"size":                   "0x100",
		GasLimitKeyValue:         "0x1000000",
		GasUsedKeyValue:          "0x5208",
		TimestampKeyValue:        "0x60000000",
		MixHashKeyValue:          "0x0000000000000000000000000000000000000000000000000000000000000000",
		"uncles":                 []any{},
	}
	if txs != nil {
		block["transactions"] = txs
	} else {
		block["transactions"] = []any{}
	}
	return block
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// newWSMockServer creates an httptest.Server that upgrades to WebSocket and
// handles JSON-RPC messages, simulating an Ethereum node.
func newWSMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var req jsonRPCRequest
			if err := json.Unmarshal(msg, &req); err != nil {
				return
			}

			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  "0x1",
			}
			respBytes, _ := json.Marshal(resp)
			if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
				return
			}
		}
	}))
}

func defaultTestKey() (*ecdsa.PrivateKey, common.Address) {
	// Use a fixed test private key
	key, err := crypto.HexToECDSA("fad9c8855b740a0b7ed4c221dbad0f33a83a49cad6b3fe8d5817ac83d38b6a19")
	if err != nil {
		panic(fmt.Sprintf("failed to parse test key: %v", err))
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	return key, addr
}

// newHangingWSServer returns a server that accepts the TCP connection and the
// HTTP upgrade request, then never responds: a hermetic black-hole for the
// WebSocket handshake. The handler blocks on its request context, which is
// cancelled when the server is closed.
func newHangingWSServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
}

// assertDeadlineError requires err to stem from the dial context's deadline —
// either the context sentinel or the socket-deadline sentinel. The WS dialer
// materialises a context deadline as a connection read/write deadline, so the
// handshake surfaces os.ErrDeadlineExceeded (not the context's own sentinel).
func assertDeadlineError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected a deadline-related error, got: %v", err)
	}
}
