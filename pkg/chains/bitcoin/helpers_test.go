package bitcoin

// The mock Bitcoin Core JSON-RPC server and the fixture loader shared by the
// package's tests, plus the fetcher's rawClient seam fake, the wire fixture
// builders, and the DefraDB write helpers the fetcher, converter and stamper
// suites use. The harness speaks Core's JSON-RPC 1.0 dialect
// (result/error/id envelopes, RPC failures served as HTTP 500 with an error
// object), records every request's method, params and auth headers, and can
// serve prebuilt raw bodies for byte-identity pass-through tests.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/sourcenetwork/defradb/client"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// fixtureFile holds real mainnet verbosity-3 data for height 184498 (327
// transactions, a coinbase paying 50.2207 BTC, and an 857.05 BTC prevout).
const fixtureFile = "testdata/block_184498.json"

// mockRPCError mirrors one JSON-RPC error object.
type mockRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mockResponse describes what the mock server answers with. Exactly one
// style wins, in the order rawBody, rpcErr, httpStatus, result.
type mockResponse struct {
	// result rides inside the standard JSON-RPC 1.0 envelope Core serves
	// for successes.
	result json.RawMessage
	// rpcErr rides inside the error envelope with Core's RPC-failure
	// convention: HTTP 500 plus a JSON body.
	rpcErr *mockRPCError
	// rawBody is a prebuilt full response body served unchanged; the
	// fixture pass-through tests use it to pin byte identity.
	rawBody string
	// httpStatus serves a non-JSON body, the proxy-shaped failure rpcclient
	// cannot parse into a JSON-RPC envelope.
	httpStatus int
	httpBody   string
}

// recordedCall is one request the mock server received.
type recordedCall struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	// Header carries the request's headers on the wire, including the
	// Authorization and api-key headers the auth tests assert on.
	Header http.Header `json:"-"`
}

// coreRPCServer is an httptest server speaking Bitcoin Core's JSON-RPC 1.0
// dialect with request recording.
type coreRPCServer struct {
	*httptest.Server

	mu    sync.Mutex
	calls []recordedCall
}

// callCount returns how many requests hit the given method.
func (s *coreRPCServer) callCount(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.calls {
		if call.Method == method {
			count++
		}
	}
	return count
}

// recordedCalls copies the recorded requests under the lock, so test-side
// reads cannot race the handler goroutine that appends them.
func (s *coreRPCServer) recordedCalls() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// newCoreRPCServer starts a mock Bitcoin Core whose answers come from the
// handler; requests are recorded before the handler runs, so cleanup-free
// uniform assertions stay possible from the tests.
func newCoreRPCServer(t *testing.T, handler func(call recordedCall) mockResponse) *coreRPCServer {
	t.Helper()
	server := &coreRPCServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		clone := r.Header.Clone()
		res := handler(recordedCall{Method: req.Method, Params: req.Params, Header: clone})
		server.mu.Lock()
		server.calls = append(server.calls, recordedCall{Method: req.Method, Params: req.Params, Header: clone})
		server.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case res.rawBody != "":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(res.rawBody))
		case res.rpcErr != nil:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": nil,
				"error":  res.rpcErr,
				"id":     req.ID,
			})
		case res.httpStatus != 0:
			w.WriteHeader(res.httpStatus)
			_, _ = w.Write([]byte(res.httpBody))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": res.result,
				"error":  nil,
				"id":     req.ID,
			})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// testConfig is a config fixture with basic auth filled in; tests override
// individual fields as needed.
func testConfig() config.BitcoinConfig {
	return config.BitcoinConfig{
		RPCUser:     "testrpcuser",
		RPCPassword: "testrpcpass",
	}
}

// newTestClient constructs a client against the mock endpoint and registers
// its shutdown as test cleanup.
func newTestClient(t *testing.T, cfg config.BitcoinConfig) *Client {
	t.Helper()
	client, err := NewClient(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// mustJSONMarshal turns typed payloads into the wire bytes a mock serves.
func mustJSONMarshal(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return raw
}

// mockBlockAt builds a successful getblock verbosity-3 answer for a canned
// payload.
func mockBlockAt(t *testing.T, payload any) mockResponse {
	return mockResponse{result: mustJSONMarshal(t, payload)}
}

// loadFixture returns the fixture's verbosity-3 JSON with trailing
// whitespace trimmed. The suite skips when the fixture has not been fetched
// yet: the file holds real mainnet data and must be taken from an archive
// node (pruned false), Bitcoin Core 23.0 or newer, with a call shaped like
//
//	bitcoin-cli getblock "$(bitcoin-cli getblockhash 184498)" 3 \
//	  > pkg/chains/bitcoin/testdata/block_184498.json
func loadFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixtureFile)
	if err != nil {
		require.ErrorIs(t, err, fs.ErrNotExist, "unexpected failure loading %s", fixtureFile)
		t.Skipf("fixture %s is absent: fetch verbosity-3 output of mainnet height 184498 from an archive Bitcoin Core node (prune=0, Core 23.0+)", fixtureFile)
		return nil
	}
	return bytes.TrimRight(raw, " \n\r\t")
}

// unmarshalParams decodes one recorded request's params array, with numbers
// as float64 and booleans as bool, ready for shape assertions.
func unmarshalParams(t *testing.T, call recordedCall) []any {
	t.Helper()
	var params []any
	require.NoError(t, json.Unmarshal(call.Params, &params), "params %s", call.Params)
	return params
}

// serveFixtureEnvelope wraps the fixture bytes into a full response body, so
// the served result stays byte-identical to the bytes the tests loaded.
func serveFixtureEnvelope(fixture []byte) string {
	return `{"result":` + string(fixture) + `,"error":null,"id":1}`
}

// ---------------------------------------------------------------------------
// Fetcher seam fake
// ---------------------------------------------------------------------------

// fakeRPCClient implements the fetcher's package-private rawClient seam with
// static fields plus per-method function overrides (the evm fake's shape), so
// tests can script dynamic behavior like routing, call counting and context
// timing without standing up an HTTP server. The mutex keeps the one
// in-flight edge safe: Connect's probe goroutine may still be running when a
// timeout already returned, and the counters must stay truthful for both the
// resolved and the timeout path.
type fakeRPCClient struct {
	mu sync.Mutex

	blockCount    int64
	blockCountErr error
	blockCountFn  func(ctx context.Context) (int64, error)

	hashByHeight map[int64]string
	hashErr      error
	hashFn       func(ctx context.Context, height int64) (string, error)

	blockByHash map[string]*Block
	blockErr    error
	blockFn     func(ctx context.Context, hash string) (*Block, error)

	archiveByHash map[string]*Block
	archiveErr    error
	archiveFn     func(ctx context.Context, hash string) (*Block, error)

	headersByHash map[string]*VerboseBlockHeader
	headerErr     error
	headerFn      func(ctx context.Context, hash string) (*VerboseBlockHeader, error)

	chainInfo    *ChainInfo
	chainInfoErr error
	chainInfoFn  func(ctx context.Context) (*ChainInfo, error)

	calls  map[string]int
	closed bool
}

func newFakeRPCClient() *fakeRPCClient {
	return &fakeRPCClient{
		hashByHeight:  make(map[int64]string),
		blockByHash:   make(map[string]*Block),
		archiveByHash: make(map[string]*Block),
		headersByHash: make(map[string]*VerboseBlockHeader),
		calls:         make(map[string]int),
	}
}

func (f *fakeRPCClient) record(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
}

func (f *fakeRPCClient) callCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeRPCClient) GetBlockCount(ctx context.Context) (int64, error) {
	f.record("getblockcount")
	if f.blockCountFn != nil {
		return f.blockCountFn(ctx)
	}
	return f.blockCount, f.blockCountErr
}

func (f *fakeRPCClient) GetBlockHash(ctx context.Context, height int64) (string, error) {
	f.record("getblockhash")
	if f.hashFn != nil {
		return f.hashFn(ctx, height)
	}
	hash, ok := f.hashByHeight[height]
	if !ok {
		return "", f.hashErr
	}
	return hash, nil
}

func (f *fakeRPCClient) GetBlockVerbosity3(ctx context.Context, hash string) (*Block, error) {
	f.record("getblock verbosity 3")
	if f.blockFn != nil {
		return f.blockFn(ctx, hash)
	}
	block, ok := f.blockByHash[hash]
	if !ok {
		return nil, f.blockErr
	}
	return block, nil
}

func (f *fakeRPCClient) GetBlockVerbosity3FromArchive(ctx context.Context, hash string) (*Block, error) {
	f.record("getblock verbosity 3 archive")
	if f.archiveFn != nil {
		return f.archiveFn(ctx, hash)
	}
	block, ok := f.archiveByHash[hash]
	if !ok {
		return nil, f.archiveErr
	}
	return block, nil
}

func (f *fakeRPCClient) GetBlockHeaderByHash(ctx context.Context, hash string) (*VerboseBlockHeader, error) {
	f.record("getblockheader")
	if f.headerFn != nil {
		return f.headerFn(ctx, hash)
	}
	header, ok := f.headersByHash[hash]
	if !ok {
		return nil, f.headerErr
	}
	return header, nil
}

func (f *fakeRPCClient) GetChainInfo(ctx context.Context) (*ChainInfo, error) {
	f.record("getblockchaininfo")
	if f.chainInfoFn != nil {
		return f.chainInfoFn(ctx)
	}
	return f.chainInfo, f.chainInfoErr
}

func (f *fakeRPCClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// Fixture builders
// ---------------------------------------------------------------------------

// fakeBitcoinHash derives a deterministic 64-hex identity from a seed, the
// same trick the evm fixtures use so validation of chain-shaped values
// always succeeds.
func fakeBitcoinHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// fakeValue returns the BTC float the satoshi tables pin against; the pointer
// form keeps every builder's values aligned in one place.
//
//go:fix inline
func fakeValue(btc float64) *float64 {
	return new(btc)
}

// fakeBlock builds one coinbase-only wire block at the given height. Every
// identity is sha256-derived from the height, so docs built from different
// heights never collide on content.
func fakeBlock(num int64) *Block {
	return &Block{
		Hash:              fakeBitcoinHash(fmt.Sprintf("block-%d", num)),
		Confirmations:     int64(1),
		StrippedSize:      int64(1000),
		Size:              int64(2000),
		Weight:            int64(4000),
		Height:            num,
		Version:           uint32(2),
		MerkleRoot:        fakeBitcoinHash(fmt.Sprintf("merkle-%d", num)),
		Time:              1231006505 + num*600,
		Nonce:             uint32(num),
		Bits:              "1a0facdc",
		Chainwork:         fakeBitcoinHash(fmt.Sprintf("chainwork-%d", num)),
		PreviousBlockHash: fakeBitcoinHash(fmt.Sprintf("block-%d", num-1)),
		Tx:                []Tx{fakeCoinbaseTx(num)},
	}
}

// fakeBlockWithTxs builds a wire block holding the coinbase plus the given
// transactions, in order.
func fakeBlockWithTxs(num int64, txs ...Tx) *Block {
	block := fakeBlock(num)
	block.Tx = append([]Tx{fakeCoinbaseTx(num)}, txs...)
	return block
}

// fakeCoinbaseTx builds the coinbase transaction of one synthetic block: a
// single coinbase input, one output, and no fee (undo data never prices the
// coinbase).
func fakeCoinbaseTx(num int64) Tx {
	return Tx{
		TxID:     fakeBitcoinHash(fmt.Sprintf("coinbase-%d", num)),
		Hash:     fakeBitcoinHash(fmt.Sprintf("coinbase-wtxid-%d", num)),
		Version:  uint32(1),
		LockTime: 0,
		Vin: []Vin{{
			Coinbase: fmt.Sprintf("03%06x", num),
			Sequence: 4294967295,
		}},
		Vout: []Vout{{
			Index: 0,
			Value: 50,
			ScriptPubKey: ScriptPubKey{
				Hex:  "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
				Type: "pubkeyhash",
			},
		}},
	}
}

// fakeTransaction builds one regular transaction with a witness input, a
// denormalized prevout, and a nonzero fee: the full wire shape the converter
// maps.
func fakeTransaction(num int64, txIndex int) Tx {
	key := fmt.Sprintf("tx-%d-%d", num, txIndex)
	return Tx{
		TxID:     fakeBitcoinHash(key),
		Hash:     fakeBitcoinHash(key + "-wtxid"),
		Version:  uint32(2),
		LockTime: 0,
		Fee:      new(0.0001),
		Vin: []Vin{{
			TxID:        fakeBitcoinHash(key + "-prev"),
			Vout:        new(uint32(0)),
			ScriptSig:   &ScriptSig{Hex: "00"},
			Sequence:    4294967295,
			TxInWitness: []string{fakeBitcoinHash(key + "-witness-0"), fakeBitcoinHash(key + "-witness-1")},
			Prevout: &PrevOut{
				Generated: false,
				Height:    num - 1,
				Value:     857.05,
				ScriptPubKey: ScriptPubKey{
					Hex:     "0014abcdef",
					Address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
					Type:    "witness_v0_keyhash",
				},
			},
		}},
		Vout: []Vout{{
			Index: 0,
			Value: 857.0499,
			ScriptPubKey: ScriptPubKey{
				Hex:     "76a91481b02c27b21004aeb4df5265dd90d4d56f56f84a88ac",
				Address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080",
				Type:    "pubkeyhash",
			},
		}},
	}
}

// fakeHeader is a verbose getblockheader answer used where only the height
// and parent hash are asserted.
func fakeHeader(num int64, parent string) *VerboseBlockHeader {
	return &VerboseBlockHeader{
		Hash:              fakeBitcoinHash(fmt.Sprintf("block-%d", num)),
		Confirmations:     int64(1),
		Height:            num,
		Version:           uint32(2),
		MerkleRoot:        fakeBitcoinHash(fmt.Sprintf("merkle-%d", num)),
		Time:              1231006505 + num*600,
		Nonce:             uint64(num),
		Bits:              "1a0facdc",
		PreviousBlockHash: parent,
	}
}

// testConverterConfig is a full config fixture for converter tests, with the
// batch knobs set to small distinct values so tests can pin which knob drives
// which group.
func testConverterConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Adapter: config.BitcoinAdapter,
			Name:    "Bitcoin",
			Network: "Mainnet",
		},
		Indexer: config.IndexerConfig{
			MaxDocsPerTxn:      1000,
			MaxTxDocsPerBatch:  7,
			MaxLogDocsPerBatch: 5,
		},
	}
}

// testFetcherConfig is a full config fixture for fetcher construction tests,
// pointed at the given RPC endpoint.
func testFetcherConfig(nodeURL string) *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Adapter: config.BitcoinAdapter,
			Name:    "Bitcoin",
			Network: "Mainnet",
		},
		Bitcoin: config.BitcoinConfig{
			RPCURL:             nodeURL,
			RPCUser:            "testrpcuser",
			RPCPassword:        "testrpcpass",
			ConfirmationDepth:  6,
			DialTimeoutSeconds: 10,
		},
	}
}

// ---------------------------------------------------------------------------
// DefraDB-backed helpers
// ---------------------------------------------------------------------------

// newBitcoinTestDB boots an embedded node with the bitcoin adapter's own
// schema applied: the shared SetupTestDefraDB default is the evm schema, so
// the SDL is taken from the converter under test.
func newBitcoinTestDB(t *testing.T, c *Converter) *testutils.TestDefraDB {
	t.Helper()
	sdl, err := c.GetSchema()
	require.NoError(t, err)
	return testutils.SetupTestDefraDBWithSchema(t, sdl)
}

// storeBitcoinBlockDoc writes a single block document through the same write
// path production uses (buildBlockDoc, NewDocFromMap, AddDocument inside an
// explicit transaction).
func storeBitcoinBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, block *Block) string {
	t.Helper()
	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)
	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)
	doc, err := client.NewDocFromMap(ctx, c.buildBlockDoc(block), col.Version())
	require.NoError(t, err)
	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())
	return doc.ID().String()
}

// storeBitcoinResidueBlockDoc writes a block document without its number
// field, simulating the numberless rows purge and P2P leaves behind, which
// the block-number queries must both exclude and survive. The hash seed keeps
// the content unique: DefraDB derives docIDs from content, so identical
// residue documents would collide.
func storeBitcoinResidueBlockDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, seed int64) string {
	t.Helper()
	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)
	col, err := txn.GetCollectionByName(ctx, c.collections.Block)
	require.NoError(t, err)
	data := c.buildBlockDoc(fakeBlock(seed))
	delete(data, constants.NumberFieldName)
	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)
	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())
	return doc.ID().String()
}

// storeBitcoinSignatureDoc writes one BlockSignature document the way the
// store's signing stage would, so range queries can be pinned against real
// rows.
func storeBitcoinSignatureDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, number int64, blockHash string) string {
	t.Helper()
	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)
	col, err := txn.GetCollectionByName(ctx, c.collections.BlockSignature)
	require.NoError(t, err)
	data := map[string]any{
		constants.BlockNumberFieldName:   number,
		constants.BlockHashFieldName:     blockHash,
		constants.MerkleRootFieldName:    fakeBitcoinHash(fmt.Sprintf("sig-merkle-%d", number)),
		constants.CIDCountFieldName:      int64(2),
		constants.CIDsFieldName:          []string{fakeBitcoinHash(fmt.Sprintf("cid-a-%d", number)), fakeBitcoinHash(fmt.Sprintf("cid-b-%d", number))},
		constants.SignatureTypeFieldName: "ecdsa",
		constants.CreatedAtFieldName:     "2026-01-01T00:00:00Z",
	}
	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)
	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())
	return doc.ID().String()
}

// storeBitcoinTxChildDoc writes one TxInput or TxOutput document directly by
// collection name, with its txid join field and block number, so the block
// range queries can be pinned against real child rows.
func storeBitcoinTxChildDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, collectionName string, blockNumber int64, txid string) string {
	t.Helper()
	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)
	col, err := txn.GetCollectionByName(ctx, collectionName)
	require.NoError(t, err)
	data := map[string]any{
		TxIDFieldName:                  txid,
		constants.BlockNumberFieldName: blockNumber,
	}
	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)
	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())
	return doc.ID().String()
}

// storeSnapshotSignatureDoc writes one SnapshotSignature document, used to
// pin the exclusion contract: snapshot history must never ride the
// block-range deletion queries.
func storeSnapshotSignatureDoc(ctx context.Context, t *testing.T, td *testutils.TestDefraDB, c *Converter, merkleRoot string) string {
	t.Helper()
	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)
	col, err := txn.GetCollectionByName(ctx, c.collections.SnapshotSignature)
	require.NoError(t, err)
	data := map[string]any{
		"startBlock":    int64(1),
		"endBlock":      int64(10),
		"merkleRoot":    merkleRoot,
		"blockCount":    int64(10),
		"signatureType": "ecdsa",
	}
	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)
	require.NoError(t, col.AddDocument(ctx, doc))
	require.NoError(t, txn.Commit())
	return doc.ID().String()
}
