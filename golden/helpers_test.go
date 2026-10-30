//go:build integration
// +build integration

package golden

// Harness pieces for the golden e2e freeze: a local JSON-RPC server that
// replays captured chain fixtures verbatim, fixture loading and validation,
// stored-document read-back, and the stable serialization the goldens are
// compared with. Everything stays deterministic: the only network is the
// in-process httptest server, and every compared value derives from the
// fixtures rather than wall-clock state.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/sourcenetwork/defradb/node"
)

// RPC method names the fixture server answers. Any other method is rejected
// so unexpected RPC traffic fails loudly instead of returning junk.
const (
	methodGetBlockByNumber      = "eth_getBlockByNumber"
	methodGetBlockReceipts      = "eth_getBlockReceipts"
	methodGetTransactionReceipt = "eth_getTransactionReceipt"
)

// blockFixture is one captured block with its receipts, loaded from a chain's
// testdata folder. blockRaw and receiptsRaw keep the captured wire shape
// verbatim so served responses carry the same payload a real node returns.
type blockFixture struct {
	height         int64
	hash           string
	blockRaw       json.RawMessage
	receiptsRaw    json.RawMessage
	receiptsByHash map[string]json.RawMessage
}

// loadChainFixtures loads every block fixture (with its receipts twin) from a
// chain folder. Blocks are named by decimal height: blocks/HEIGHT.json plus
// receipts/HEIGHT.json; the stem keeps the two files paired without parsing
// the hash out of the filename.
func loadChainFixtures(t *testing.T, chainDir string) []*blockFixture {
	t.Helper()

	blocksDir := filepath.Join(chainDir, "testdata", "blocks")
	entries, err := os.ReadDir(blocksDir)
	require.NoError(t, err, "read block fixtures at %s (capture a real block response under blocks/ and its eth_getBlockReceipts response under receipts/, both named by decimal height)", blocksDir)

	var fixtures []*blockFixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".json")
		fixtures = append(fixtures, loadBlockFixture(t, chainDir, stem))
	}
	require.NotEmpty(t, fixtures, "no block fixtures found in %s; the golden flow needs a captured block to run against", blocksDir)
	return fixtures
}

// loadBlockFixture loads one block plus its receipts, parses the block's
// height and hash for addressing, and indexes the receipts by transaction
// hash for the per-tx fallback path.
func loadBlockFixture(t *testing.T, chainDir, stem string) *blockFixture {
	t.Helper()

	blockRaw := loadFixtureResult(t, filepath.Join(chainDir, "testdata", "blocks", stem+".json"))
	receiptsRaw := loadFixtureResult(t, filepath.Join(chainDir, "testdata", "receipts", stem+".json"))

	var meta struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(blockRaw, &meta), "block fixture %s: missing or malformed number/hash", stem)
	require.NotEmpty(t, meta.Number, "block fixture %s: no number field", stem)
	require.NotEmpty(t, meta.Hash, "block fixture %s: no hash field", stem)

	height, err := strconv.ParseInt(strings.TrimPrefix(meta.Number, "0x"), 16, 64)
	require.NoError(t, err, "block fixture %s: unparseable hex height %q", stem, meta.Number)

	receiptsByHash, err := decodeReceipts(receiptsRaw)
	require.NoError(t, err, "receipts fixture %s: not a receipt array", stem)

	return &blockFixture{
		height:         height,
		hash:           strings.ToLower(meta.Hash),
		blockRaw:       blockRaw,
		receiptsRaw:    receiptsRaw,
		receiptsByHash: receiptsByHash,
	}
}

// loadFixtureResult reads a captured fixture file and unwraps the JSON-RPC
// response envelope when one is present, so both save styles work: a full
// curl response body or a bare result payload.
func loadFixtureResult(t *testing.T, path string) json.RawMessage {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read fixture %s", path)

	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Result) > 0 {
		return envelope.Result
	}
	return json.RawMessage(bytes.TrimSpace(data))
}

// decodeReceipts flattens a captured receipts array into per-transaction
// JSON objects keyed by lowercased transaction hash, the key the fallback
// receipt path requests by.
func decodeReceipts(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var receipts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &receipts); err != nil {
		return nil, fmt.Errorf("receipt fixture is not an array: %w", err)
	}
	byHash := make(map[string]json.RawMessage, len(receipts))
	for _, receipt := range receipts {
		var txHash string
		if err := json.Unmarshal(receipt["transactionHash"], &txHash); err != nil {
			return nil, fmt.Errorf("receipt without transactionHash: %w", err)
		}
		single, err := json.Marshal(receipt)
		if err != nil {
			return nil, fmt.Errorf("re-encode receipt: %w", err)
		}
		byHash[strings.ToLower(txHash)] = single
	}
	return byHash, nil
}

// fixtureRPCServer replays captured fixtures over JSON-RPC: block bodies are
// addressed by the requested hex height, receipts by the block's content
// hash, mirroring how the real endpoints are addressed. failBatchReceipts
// makes every eth_getBlockReceipts call fail: with a healthy fixture the
// batch call always succeeds, so the per-tx eth_getTransactionReceipt
// fallback the fetcher degrades to would never run without the flag.
type fixtureRPCServer struct {
	*httptest.Server
	byHeight          map[int64]*blockFixture
	byHash            map[string]*blockFixture
	failBatchReceipts bool
}

func newFixtureRPCServer(fixtures []*blockFixture, failBatchReceipts bool) *fixtureRPCServer {
	byHeight := make(map[int64]*blockFixture, len(fixtures))
	byHash := make(map[string]*blockFixture, len(fixtures))
	for _, fixture := range fixtures {
		byHeight[fixture.height] = fixture
		byHash[fixture.hash] = fixture
	}
	s := &fixtureRPCServer{byHeight: byHeight, byHash: byHash, failBatchReceipts: failBatchReceipts}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *fixtureRPCServer) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}

	result, rpcErr := s.serve(req.Method, req.Params)
	w.Header().Set("Content-Type", "application/json")
	var resp map[string]any
	if rpcErr != nil {
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]any{"code": -32000, "message": rpcErr.Error()},
		}
	} else {
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// serve resolves one RPC method against the captured fixtures.
func (s *fixtureRPCServer) serve(method string, params json.RawMessage) (json.RawMessage, error) {
	var args []json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil || len(args) == 0 {
		return nil, fmt.Errorf("malformed params for %s", method)
	}

	switch method {
	case methodGetBlockByNumber:
		var heightHex string
		if err := json.Unmarshal(args[0], &heightHex); err != nil {
			return nil, fmt.Errorf("%s: expected a hex height parameter", method)
		}
		height, err := strconv.ParseInt(strings.TrimPrefix(heightHex, "0x"), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: unparseable height %q", method, heightHex)
		}
		fixture, ok := s.byHeight[height]
		if !ok {
			return nil, fmt.Errorf("no fixture captured for block height %d", height)
		}
		return fixture.blockRaw, nil

	case methodGetBlockReceipts:
		// Forced batch failure: a JSON-RPC error response makes the client
		// fail the call, which is what sends the fetcher into its per-tx
		// receipt fallback for the fallback run.
		if s.failBatchReceipts {
			return nil, fmt.Errorf("batch receipts unavailable: this run exercises the per-tx receipt fallback")
		}
		// The receipts selector is an object (blockHash or blockNumber
		// field), not a bare string.
		var arg struct {
			BlockHash string `json:"blockHash"`
		}
		if err := json.Unmarshal(args[0], &arg); err != nil {
			return nil, fmt.Errorf("%s: expected a block hash selector", method)
		}
		fixture, ok := s.byHash[strings.ToLower(arg.BlockHash)]
		if !ok {
			return nil, fmt.Errorf("no fixture captured for block hash %s", arg.BlockHash)
		}
		return fixture.receiptsRaw, nil

	case methodGetTransactionReceipt:
		var txHash string
		if err := json.Unmarshal(args[0], &txHash); err != nil {
			return nil, fmt.Errorf("%s: expected a transaction hash parameter", method)
		}
		for _, fixture := range s.byHeight {
			if raw, ok := fixture.receiptsByHash[strings.ToLower(txHash)]; ok {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("no fixture receipt captured for transaction %s", txHash)

	default:
		return nil, fmt.Errorf("unexpected RPC method %q: the golden flow only exercises block and receipt fetches", method)
	}
}

// collectionFields binds a golden file stem to the collection that provides
// its rows and to the chain-data fields the goldens compare. The field lists
// mirror the SDL field sets with the DefraDB artifacts excluded: no _docID
// and no relation link fields, so the freeze targets conversion output, not
// Defra's docID scheme.
type collectionFields struct {
	name   string
	column func(*evm.CollectionNames) string
	fields []string
}

var goldenCollections = []collectionFields{
	{
		name:   "block",
		column: func(n *evm.CollectionNames) string { return n.Block },
		fields: []string{
			"hash", "number", "timestamp", "parentHash", "difficulty",
			"totalDifficulty", "gasUsed", "gasLimit", "baseFeePerGas", "nonce",
			"miner", "size", "stateRoot", "sha3Uncles", "transactionsRoot",
			"receiptsRoot", "logsBloom", "extraData", "mixHash", "uncles",
		},
	},
	{
		name:   "transaction",
		column: func(n *evm.CollectionNames) string { return n.Transaction },
		fields: []string{
			"hash", "blockHash", "blockNumber", "from", "to", "value", "gas",
			"gasPrice", "gasUsed", "maxFeePerGas", "maxPriorityFeePerGas",
			"input", "nonce", "transactionIndex", "type", "chainId", "v", "r",
			"s", "status", "cumulativeGasUsed", "effectiveGasPrice",
		},
	},
	{
		name:   "log",
		column: func(n *evm.CollectionNames) string { return n.Log },
		fields: []string{
			"address", "topics", "data", "transactionHash", "blockHash",
			"blockNumber", "transactionIndex", "logIndex", "removed",
		},
	},
	{
		name:   "accessListEntry",
		column: func(n *evm.CollectionNames) string { return n.AccessListEntry },
		fields: []string{"address", "blockNumber", "storageKeys"},
	},
}

// readBackLimit bounds every read-back query explicitly: a server-side
// default limit, if any, must never truncate a large block's rows.
const readBackLimit = 100000

// readCollectionDocs queries every stored row of one collection through the
// run's node's in-process DB API, selecting exactly the golden field list.
func readCollectionDocs(ctx context.Context, t *testing.T, defraNode *node.Node, collection string, fields []string) []map[string]any {
	t.Helper()

	query := fmt.Sprintf(`query { %s(limit: %d) { %s } }`, collection, readBackLimit, strings.Join(fields, " "))
	result := defraNode.DB.ExecRequest(ctx, query)
	require.Empty(t, result.GQL.Errors, "read back %s", collection)

	data, ok := result.GQL.Data.(map[string]any)
	require.True(t, ok, "unexpected read-back result shape for %s", collection)

	return gqlRows(data[collection])
}

// gqlRows flattens a GraphQL result field into row maps. The rows arrive as
// []any or []map[string]any depending on the driver path; both shapes are
// handled by the existing ExecRequest consumers.
func gqlRows(raw any) []map[string]any {
	var rows []map[string]any
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
	case []map[string]any:
		rows = typed
	}
	return rows
}

// stableJSON renders the read-back rows as the golden bytes: documents are
// sorted by their compact JSON (Go marshals map keys sorted at every level,
// so no per-collection sort key is needed), duplicates kept, and pretty
// printed with a trailing newline so diffs stay reviewable. Rows are frozen
// verbatim because distinct documents can serialize identically once _docID
// and relation fields are excluded: two transactions sharing an access-list
// entry are stored twice and both must show up in the freeze, so a
// regression that stores a document twice can no longer pass silently.
func stableJSON(rows []map[string]any) ([]byte, error) {
	type keyRow struct {
		key string
		row map[string]any
	}
	pairs := make([]keyRow, 0, len(rows))
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("serialize document: %w", err)
		}
		pairs = append(pairs, keyRow{key: string(b), row: row})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })

	docs := make([]map[string]any, 0, len(pairs))
	for _, pair := range pairs {
		docs = append(docs, pair.row)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(docs); err != nil {
		return nil, fmt.Errorf("encode golden JSON: %w", err)
	}
	return buf.Bytes(), nil
}

// compareOrUpdateGolden compares the stored output with the committed golden
// file, or rewrites it when allowUpdate is set. Only the batch run passes
// -golden-update through, so regeneration always freezes the primary receipt
// path and the fallback run can never overwrite the goldens with diverging
// bytes. Missing goldens without the flag are a hard failure pointing at the
// regeneration command.
func compareOrUpdateGolden(t *testing.T, path string, actual []byte, allowUpdate bool) {
	t.Helper()

	if allowUpdate {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, actual, 0o644))
		t.Logf("golden updated: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("golden file %s is missing; regenerate with: go test -tags=integration ./golden/... -run Golden -golden-update", path)
	}
	require.NoError(t, err, "read golden %s", path)
	require.Equal(t, string(want), string(actual),
		"stored output for %s diverged from the golden; regenerate with -golden-update only if the delta is intended", path)
}
