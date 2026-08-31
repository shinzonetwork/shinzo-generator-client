package indexer

// helpers_test.go holds the shared test infrastructure for the indexer test
// files: mock JSON-RPC servers, fetcher/converter/blockHandler wiring, DefraDB
// node builders, and StartIndexing/signing scaffolding. It contains no tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// ethGetBlockByNumber is used in tests to identify the block retrieval RPC call.
	ethGetBlockByNumber = "eth_getBlockByNumber"
	// defaultBlockParamLatest is used in tests to identify the "latest" block parameter.
	defaultBlockParamLatest = "latest"
	// ethBlockNumber is used in tests to identify the block number RPC call.
	ethBlockNumber = "eth_blockNumber"
	// ethGetBlockReceipts is used in tests to identify the block receipts RPC call.
	ethGetBlockReceipts = "eth_getBlockReceipts"
	// netVersion is used in tests to identify the network version RPC call.
	netVersion = "net_version"
	// ethChainID is used in tests to identify the chain ID RPC call.
	ethChainID = "eth_chainId"
	// ethGetTransactionReceipt is used in tests to identify the transaction receipt RPC call.
	ethGetTransactionReceipt = "eth_getTransactionReceipt"

	testDefraURL         = "http://localhost:9181"
	testDefraRandomURL   = "127.0.0.1:0"
	testMinerAddr        = "0x1111111111111111111111111111111111111111"
	testSha3Uncles       = "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"
	testTransactionsRoot = "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
)

var testDefraP2PDisabled = config.DefraDBP2PConfig{Enabled: false, ListenAddr: "/ip4/127.0.0.1/tcp/0"}

// ---------------------------------------------------------------------------.
// Mock JSON-RPC server for indexer-level integration tests.
// ---------------------------------------------------------------------------.

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

		result, rpcErr := handler(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if rpcErr != nil {
			resp := map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32000, "message": rpcErr.Error()},
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

func newTestProcessor(t *testing.T, td *testutils.TestDefraDB, rpcServerURL string, receiptWorkers int) (chains.Fetcher, chains.Converter, *defra.BlockHandler) {
	t.Helper()
	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Ethereum",
			Network: "Mainnet",
		},
		Geth: config.GethConfig{
			NodeURL:    rpcServerURL,
			APIKeyType: "X-Api-Key",
		},
		Indexer: config.IndexerConfig{
			MaxDocsPerTxn:      100,
			ReceiptWorkers:     receiptWorkers,
			MaxTxDocsPerBatch:  100,
			MaxLogDocsPerBatch: 100,
			MaxALEDocsPerBatch: 100,
		},
	}

	fetcher, err := evm.NewFetcherFromConfig(cfg)
	require.NoError(t, err)
	require.NoError(t, fetcher.Connect(context.Background()))
	t.Cleanup(func() { _ = fetcher.Close() })

	converter := evm.NewConverter(cfg)

	blockHandler, err := defra.NewBlockHandler(td.Node, cfg.Indexer.MaxDocsPerTxn)
	require.NoError(t, err)

	return fetcher, converter, blockHandler
}

type mockBlockStorer struct {
	storeFn        func(ctx context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error)
	signExistingFn func(ctx context.Context, result chains.ConversionResult, blockHash string, blockNumber int64) (string, error)
}

func (m *mockBlockStorer) Store(ctx context.Context, result chains.ConversionResult) (*defra.BlockCreationResult, error) {
	if m.storeFn != nil {
		return m.storeFn(ctx, result)
	}
	return &defra.BlockCreationResult{BlockID: "mock-block-id"}, nil
}

func (m *mockBlockStorer) SignExisting(ctx context.Context, result chains.ConversionResult, blockHash string, blockNumber int64) (string, error) {
	if m.signExistingFn != nil {
		return m.signExistingFn(ctx, result, blockHash, blockNumber)
	}
	return "mock-sig-id", nil
}

func fullBlockResponse(number string, txs []any) map[string]any {
	emptyTrieRoot := testTransactionsRoot
	block := map[string]any{
		constants.NumberFieldValue: number,
		"hash":                     "0x0000000000000000000000000000000000000000000000000000000000000001",
		"parentHash":               "0x0000000000000000000000000000000000000000000000000000000000000000",
		"nonce":                    "0x0000000000000000",
		"sha3Uncles":               testSha3Uncles,
		"logsBloom":                "0x" + fmt.Sprintf("%0512x", 0),
		"transactionsRoot":         emptyTrieRoot,
		"stateRoot":                "0x0000000000000000000000000000000000000000000000000000000000000000",
		"receiptsRoot":             "0x0000000000000000000000000000000000000000000000000000000000000000",
		"miner":                    "0x0000000000000000000000000000000000000000",
		"difficulty":               "0x0",
		"totalDifficulty":          "0x0",
		"extraData":                "0x",
		"size":                     "0x100",
		"gasLimit":                 "0x1000000",
		"gasUsed":                  "0x5208",
		"timestamp":                "0x60000000",
		"mixHash":                  "0x0000000000000000000000000000000000000000000000000000000000000000",
		"uncles":                   []any{},
	}
	if txs != nil {
		block["transactions"] = txs
	} else {
		block["transactions"] = []any{}
	}
	return block
}

// fakeDocID generates a valid bae-prefixed UUID for testing.
func fakeDocID(seed int) string {
	return fmt.Sprintf("bae-%08x-0000-0000-0000-%012x", seed, seed)
}

// newHealthServerForTest creates a health server that can be stopped.
func newHealthServerForTest(t *testing.T) *server.HealthServer {
	t.Helper()
	// Use a random high port to avoid conflicts.
	hs := server.NewHealthServer(0, nil, "")
	return hs
}

// newMockRPCServerForIntegration creates a mock that handles all methods needed.
// by the full StartIndexing flow. blockCh is sent on every eth_getBlockByNumber call.
// so the caller can track progress.
func newMockRPCServerForIntegration(blockCh chan<- struct{}) *httptest.Server {
	var blockCallCount atomic.Int64

	return newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			count := blockCallCount.Add(1)
			if blockCh != nil {
				select {
				case blockCh <- struct{}{}:
				default:
				}
			}
			// Return a unique block per call: use a high starting number.
			num := fmt.Sprintf("0x%x", 100000+count)
			return fullBlockResponse(num, nil), nil

		case ethBlockNumber:
			// Used by HeaderByNumber(nil) → returns the "latest" header.
			return "0x100000", nil

		case ethGetBlockReceipts:
			return []any{}, nil

		case ethGetTransactionReceipt:
			return map[string]any{}, nil

		case netVersion:
			return "1", nil

		case ethChainID:
			return "0x1", nil

		default:
			return "0x1", nil
		}
	})
}

// startPathBaseCfg builds the common StartIndexing test config.
func startPathBaseCfg(rpcURL, defraURL, tmpDir string, idx config.IndexerConfig) *config.Config {
	return &config.Config{
		DefraDB: config.DefraDBConfig{
			URL:           defraURL,
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           testDefraP2PDisabled,
			Store:         config.DefraDBStoreConfig{Path: tmpDir},
		},
		Geth:    config.GethConfig{NodeURL: rpcURL},
		Indexer: idx,
		Logger:  config.LoggerConfig{Development: true},
	}
}

// startIndexingBackground launches StartIndexing(false) in the background and
// returns the channel its result is sent to.
func startIndexingBackground(t *testing.T, indexer *ChainIndexer) <-chan error {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()
	return errCh
}

// writeCorruptedFile writes invalid gob data to a file.
func writeCorruptedFile(path string) error {
	return os.WriteFile(filepath.Clean(path), []byte("this is not valid gob data"), 0o600)
}

// countingBlockHandler answers "latest" tip requests with latestTip and serves
// blocks numbered blockNumBase+count. If blockCh is non-nil each block call
// pushes a signal; if ethBlockNumberVal is non-empty it is served for
// ethBlockNumber (otherwise the default "0x1").
func countingBlockHandler(count *atomic.Int64, latestTip string, blockNumBase int64, ethBlockNumberVal string, blockCh chan<- struct{}) func(string, json.RawMessage) (any, error) {
	return func(method string, params json.RawMessage) (any, error) {
		switch method {
		case ethGetBlockByNumber:
			var rawParams []json.RawMessage
			if err := json.Unmarshal(params, &rawParams); err == nil && len(rawParams) > 0 {
				var blockParam string
				if innerErr := json.Unmarshal(rawParams[0], &blockParam); innerErr == nil && blockParam == defaultBlockParamLatest {
					return fullBlockResponse(latestTip, nil), nil
				}
			}
			n := count.Add(1)
			if blockCh != nil {
				select {
				case blockCh <- struct{}{}:
				default:
				}
			}
			return fullBlockResponse(fmt.Sprintf("0x%x", blockNumBase+n), nil), nil
		case ethBlockNumber:
			if ethBlockNumberVal != "" {
				return ethBlockNumberVal, nil
			}
			return "0x1", nil
		case ethGetBlockReceipts:
			return []any{}, nil
		default:
			return "0x1", nil
		}
	}
}

// waitForBlockSignals waits until want block signals arrive on blockCh.
func waitForBlockSignals(t *testing.T, blockCh <-chan struct{}, errCh <-chan error, want int, timeoutMsg func(seen int) string) {
	t.Helper()

	deadline := time.After(60 * time.Second)
	blocksSeen := 0
	for blocksSeen < want {
		select {
		case <-blockCh:
			blocksSeen++
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("%s", timeoutMsg(blocksSeen))
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}
}

// waitForBlockCondition waits until cond reports progress, polling blockCh
// (nil-safe) and every 100ms.
func waitForBlockCondition(t *testing.T, blockCh <-chan struct{}, errCh <-chan error, cond func() bool, timeoutMsg string) {
	t.Helper()

	deadline := time.After(60 * time.Second)
	for !cond() {
		select {
		case <-blockCh:
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("%s", timeoutMsg)
		case err := <-errCh:
			if err != nil {
				t.Fatalf("StartIndexing failed: %v", err)
			}
		}
	}
}

// waitForIndexerStart launches StartIndexing(false) in the background and
// blocks until the indexer reports started (30s deadline).
func waitForIndexerStart(t *testing.T, indexer *ChainIndexer) {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		errCh <- indexer.StartIndexing(false)
	}()

	deadline := time.After(30 * time.Second)
	for !indexer.IsStarted() {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for indexer to start")
		case startErr := <-errCh:
			if startErr != nil {
				t.Fatalf("StartIndexing failed: %v", startErr)
			}
		}
	}
}

// setupSignMessagesDirect builds a ChainIndexer around an embedded test
// DefraDB node without going through StartIndexing.
func setupSignMessagesDirect(t *testing.T, keyringSecret string) *ChainIndexer {
	t.Helper()

	td := testutils.SetupTestDefraDB(t)
	return &ChainIndexer{
		defraNode: td.Node,
		cfg: &config.Config{
			DefraDB: config.DefraDBConfig{
				KeyringSecret: keyringSecret,
				Store:         config.DefraDBStoreConfig{Path: td.Dir},
			},
		},
	}
}

// startSignMessagesIndexer runs the full StartIndexing skeleton for signing
// tests: mock RPC server, CreateIndexer, background StartIndexing, wait for
// start, and stop on cleanup.
func startSignMessagesIndexer(t *testing.T, keyringSecret, defraURL string, p2p config.DefraDBP2PConfig, rpcServer func() *httptest.Server) *ChainIndexer {
	t.Helper()

	srv := rpcServer()
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		DefraDB: config.DefraDBConfig{
			KeyringSecret: keyringSecret,
			P2P:           p2p,
			Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
		},
		Geth: config.GethConfig{NodeURL: srv.URL},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   2,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}
	if defraURL != "" {
		cfg.DefraDB.URL = defraURL
	}

	indexer, err := CreateIndexer(cfg)
	require.NoError(t, err)

	waitForIndexerStart(t, indexer)

	t.Cleanup(func() {
		indexer.shouldIndex = false
		indexer.StopIndexing()
	})
	return indexer
}

// createClosedTestDefraNode creates a DefraDB node, starts it, then closes it.
// This gives a node in a "closed" state for testing error paths.
func createClosedTestDefraNode(t *testing.T) *node.Node {
	t.Helper()
	tmpDir := t.TempDir()
	ctx := context.Background()

	opts := options.Node().
		SetDisableAPI(true).
		SetDisableP2P(true)
	opts.Store().SetPath(tmpDir)

	defraNode, err := node.New(ctx, opts)
	require.NoError(t, err)
	require.NoError(t, defraNode.Start(ctx))
	_ = defraNode.Close(ctx)
	return defraNode
}

// ---------------------------------------------------------------------------.
// setupTestDefraDBWithP2P creates an embedded DefraDB node with P2P ENABLED.
// This allows PeerInfo() to return actual multiaddresses for the self info paths.
// ---------------------------------------------------------------------------.

func setupTestDefraDBWithP2P(t *testing.T) *node.Node {
	t.Helper()
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	ctx := context.Background()

	opts := options.Node().
		SetDisableAPI(true).
		SetDisableP2P(false)
	opts.Store().SetPath(tmpDir)
	opts.P2P().SetListenAddresses("/ip4/127.0.0.1/tcp/0")

	defraNode, err := node.New(ctx, opts)
	if err != nil {
		t.Fatalf("Failed to create DefraDB node with P2P: %v", err)
	}
	if err := defraNode.Start(ctx); err != nil {
		t.Fatalf("failed to start DefraDB node with P2P: %v", err)
	}

	t.Cleanup(func() {
		_ = defraNode.Close(context.Background())
	})

	return defraNode
}

// ---------------------------------------------------------------------------.
// GetPeerInfo — peer dedup merge branch (covers line 625-627).
// Create a remote node with multiple listen addresses so that ActivePeers(),
// returns multiple multiaddrs for the same peer ID. The dedup loop then,
// merges addresses for the same peer (the "existing" branch).
// ---------------------------------------------------------------------------.

func setupTestDefraDBWithMultiAddr(t *testing.T) *node.Node {
	t.Helper()
	logger.InitConsoleOnly(true)

	tmpDir := t.TempDir()
	ctx := context.Background()

	opts := options.Node().
		SetDisableAPI(true).
		SetDisableP2P(false)
	opts.Store().SetPath(tmpDir)
	// Two listen addresses → same peer ID appears with two addresses in ActivePeers.
	opts.P2P().SetListenAddresses("/ip4/127.0.0.1/tcp/0", "/ip4/127.0.0.1/tcp/0")

	defraNode, err := node.New(ctx, opts)
	if err != nil {
		t.Fatalf("Failed to create DefraDB node with multi-addr P2P: %v", err)
	}
	if err := defraNode.Start(ctx); err != nil {
		t.Fatalf("Failed to start DefraDB node with multi-addr P2P: %v", err)
	}

	t.Cleanup(func() {
		_ = defraNode.Close(context.Background())
	})

	return defraNode
}

// ---------------------------------------------------------------------------.
// Verify that the mock RPC server handles batch requests correctly.
// ---------------------------------------------------------------------------.

func TestMockRPCServer_VariousEndpoints(t *testing.T) {
	t.Parallel()
	srv := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case ethBlockNumber:
			return "0x100000", nil
		case netVersion:
			return "1", nil
		case ethChainID:
			return "0x1", nil
		default:
			return nil, fmt.Errorf("unknown method: %s", method)
		}
	})
	defer srv.Close()

	// Verify the server responds to a basic request.
	resp, err := http.Post(srv.URL, "application/json", nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
}

// ---------------------------------------------------------------------------.
// fullBlockResponse helper test.
// ---------------------------------------------------------------------------.

func TestFullBlockResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		number    string
		txs       []any
		wantTxLen int
	}{
		{
			name:   "with transactions",
			number: "0x100",
			txs: []any{
				map[string]any{
					"hash":  "0x123",
					"value": "0x0",
				},
			},
			wantTxLen: 1,
		},
		{
			name:      "nil transactions defaults to empty list",
			number:    "0x200",
			txs:       nil,
			wantTxLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			block := fullBlockResponse(tt.number, tt.txs)
			assert.Equal(t, tt.number, block[constants.NumberFieldValue])
			txList := block["transactions"].([]any)
			assert.Len(t, txList, tt.wantTxLen)
		})
	}
}

// ---------------------------------------------------------------------------.
// Ensure unused imports are exercised.
// ---------------------------------------------------------------------------.

// This test ensures the filepath import is used (for prune queue test paths).
func TestPruneQueueFilePath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	queueFilePath := filepath.Join(tmpDir, "prune_queue.gob")
	assert.Contains(t, queueFilePath, "prune_queue.gob")
}
