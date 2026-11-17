//go:build integration
// +build integration

package golden

// The evm chain spec: the evm fetcher talks JSON-RPC with two receipt
// paths, the batch eth_getBlockReceipts call and the per-tx
// eth_getTransactionReceipt fallback it degrades to when the batch call
// fails. The mock RPC server serves both and can force the batch call to
// fail, which is how the fallback run is produced.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

// evmSpec implements chainSpec for the evm chain family.
type evmSpec struct{}

// RPC method names the fixture server answers. Any other method is rejected
// so unexpected RPC traffic fails loudly instead of returning junk.
const (
	methodGetBlockByNumber      = "eth_getBlockByNumber"
	methodGetBlockReceipts      = "eth_getBlockReceipts"
	methodGetTransactionReceipt = "eth_getTransactionReceipt"
)

func (evmSpec) fixtures(t *testing.T, folder string) []chainFixture {
	return loadChainFixtures(t, folder, parseEVMFixtureMeta,
		"capture a real eth_getBlockByNumber response under blocks/ and its eth_getBlockReceipts response under receipts/, both named by decimal height")
}

func (evmSpec) modes() []receiptMode {
	return []receiptMode{receiptModeBatch, receiptModeFallback}
}

func (evmSpec) server(t *testing.T, fixtures []chainFixture, mode receiptMode) *httptest.Server {
	evmFixtures := make([]*evmFixture, 0, len(fixtures))
	for _, fixture := range fixtures {
		receiptsByHash, err := decodeReceipts(fixture.receiptsRaw)
		require.NoError(t, err, "receipts fixture for block %d: not a receipt array", fixture.height)
		evmFixtures = append(evmFixtures, &evmFixture{chainFixture: fixture, receiptsByHash: receiptsByHash})
	}
	return newFixtureRPCServer(evmFixtures, mode.failBatchReceipts).Server
}

func (evmSpec) fetcher(t *testing.T, serverURL string) chains.Fetcher {
	// The HTTP dial is lazy, so an httptest URL works; no WebSocket is
	// configured, matching the plain-HTTP production profile.
	client, err := evm.NewEthereumClient(context.Background(), serverURL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	return evm.NewFetcher(client, 4)
}

func (evmSpec) converter(t *testing.T, cfg *config.Config) chains.Converter {
	converter, err := evm.NewConverter(cfg)
	require.NoError(t, err, "evm converter")
	return converter
}

func (evmSpec) assertCoverage(t *testing.T, raw any) {
	bundle, ok := raw.(*evm.BlockBundle)
	require.True(t, ok, "expected *evm.BlockBundle, got %T", raw)
	assertBundleCoverage(t, bundle)
}

func (evmSpec) goldens() []goldenCollectionSpec {
	return evmGoldenCollections
}

// parseEVMFixtureMeta reads the block's hex height and content hash, the
// keys the JSON-RPC mock server addresses fixtures by.
func parseEVMFixtureMeta(t *testing.T, blockRaw json.RawMessage) (int64, string) {
	t.Helper()

	var meta struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(blockRaw, &meta), "block fixture: missing or malformed number/hash")
	require.NotEmpty(t, meta.Number, "block fixture: no number field")
	require.NotEmpty(t, meta.Hash, "block fixture: no hash field")

	height, err := strconv.ParseInt(strings.TrimPrefix(meta.Number, "0x"), 16, 64)
	require.NoError(t, err, "block fixture: unparseable hex height %q", meta.Number)

	return height, strings.ToLower(meta.Hash)
}

// evmFixture wraps a chain fixture with the receipt index the per-tx
// fallback path requests by: receipts keyed by lowercased transaction hash.
type evmFixture struct {
	chainFixture
	receiptsByHash map[string]json.RawMessage
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
	byHeight          map[int64]*evmFixture
	byHash            map[string]*evmFixture
	failBatchReceipts bool
}

func newFixtureRPCServer(fixtures []*evmFixture, failBatchReceipts bool) *fixtureRPCServer {
	byHeight := make(map[int64]*evmFixture, len(fixtures))
	byHash := make(map[string]*evmFixture, len(fixtures))
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

// assertBundleCoverage pins the fixture coverage the freeze requires: the
// captured block must contain both outcomes and the merged transactions must
// carry the receipt-owned gas fields, so the goldens can only be generated
// from output that actually exercises the receipt merge.
func assertBundleCoverage(t *testing.T, bundle *evm.BlockBundle) {
	t.Helper()

	require.NotNil(t, bundle.Block, "fetched bundle has no block")
	require.NotEmpty(t, bundle.Transactions, "fixture block carries no transactions")

	for _, receipt := range bundle.Receipts {
		require.NotNil(t, receipt, "fixture block carries a missing receipt; the capture is incomplete")
	}

	failed, successful := 0, 0
	for _, tx := range bundle.Transactions {
		require.NotNil(t, tx)
		if tx.Status {
			successful++
		} else {
			failed++
		}
		require.NotEmpty(t, tx.GasUsed, "transaction %s has no gasUsed after the receipt merge; the capture is incomplete", tx.Hash)
		require.NotEmpty(t, tx.CumulativeGasUsed, "transaction %s has no cumulativeGasUsed after the receipt merge; the capture is incomplete", tx.Hash)
		require.NotEmpty(t, tx.EffectiveGasPrice, "transaction %s has no effectiveGasPrice after the receipt merge; pick a post-EIP-1559 block", tx.Hash)
	}
	require.NotZero(t, successful, "fixture block carries no successful transaction; pick a block with both outcomes")
	require.NotZero(t, failed, "fixture block carries no failed transaction; pick a block with both outcomes")
}

var evmGoldenCollections = []goldenCollectionSpec{
	{
		name:   "block",
		column: func(c chains.Collections) string { return c.(*evm.CollectionNames).Block },
		fields: []string{
			"hash", "number", "timestamp", "parentHash", "difficulty",
			"totalDifficulty", "gasUsed", "gasLimit", "baseFeePerGas", "nonce",
			"miner", "size", "stateRoot", "sha3Uncles", "transactionsRoot",
			"receiptsRoot", "logsBloom", "extraData", "mixHash", "uncles",
		},
	},
	{
		name:   "transaction",
		column: func(c chains.Collections) string { return c.(*evm.CollectionNames).Transaction },
		fields: []string{
			"hash", "blockHash", "blockNumber", "from", "to", "value", "gas",
			"gasPrice", "gasUsed", "maxFeePerGas", "maxPriorityFeePerGas",
			"input", "nonce", "transactionIndex", "type", "chainId", "v", "r",
			"s", "status", "cumulativeGasUsed", "effectiveGasPrice",
		},
	},
	{
		name:   "log",
		column: func(c chains.Collections) string { return c.(*evm.CollectionNames).Log },
		fields: []string{
			"address", "topics", "data", "transactionHash", "blockHash",
			"blockNumber", "transactionIndex", "logIndex", "removed",
		},
	},
	{
		name:   "accessListEntry",
		column: func(c chains.Collections) string { return c.(*evm.CollectionNames).AccessListEntry },
		fields: []string{"address", "blockNumber", "storageKeys"},
	},
}
