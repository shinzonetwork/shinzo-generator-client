//go:build integration
// +build integration

package golden

// The bitcoin chain spec: Bitcoin Core has no receipts endpoint at all, the
// per-transaction fee and the prevout undo data ride inline in the getblock
// verbosity-3 body, so the bitcoin case loads one block fixture per height
// and runs two passes that differ only in which endpoint serves that body.
// The primary pass freezes the plain fetch; the archive pass answers the
// primary's getblock with Core's pruned-data error, which routes the fetcher
// into the archive fallback endpoint, and the two passes must store
// identical documents. The mock speaks Core's JSON-RPC 1.0 dialect and serves
// exactly the two methods the pipeline calls (getblockhash, getblock); any
// other method fails loudly, so unexpected RPC traffic cannot pass silently.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin"
)

// bitcoinSpec implements chainSpec for the bitcoin chain family.
type bitcoinSpec struct{}

// RPC method names the fixture server answers. Any other method is rejected
// so unexpected RPC traffic fails loudly instead of returning junk.
const (
	methodGetBlockHash = "getblockhash"
	methodGetBlock     = "getblock"
)

// Bitcoin Core RPC error codes the mock needs: -1 misc (the pruned-data
// flavor is classified by message text), -5 block not found, and -8 invalid
// parameter for heights past the tip. btcjson stays behind the bitcoin
// package's boundary, so the numeric codes are restated here.
const (
	rpcCodeMisc             = -1
	rpcCodeBlockNotFound    = -5
	rpcCodeInvalidParameter = -8
)

// prunedDataMessage is Core's misc-error text for pruned block data. The
// client classifies the -1 answer by this fragment (wrapRPCError), so the
// mock must spell it exactly the way Core does or the archive pass would
// never route to the archive endpoint.
const prunedDataMessage = "Block not available (pruned data)"

// bitcoinCaptureHint is the failure hint for a missing or unreadable fixture
// set.
const bitcoinCaptureHint = "make bitcoin-golden-fetch captures a suitable mainnet block into this folder (requires BITCOIN_RPC_URL plus BITCOIN_RPC_USER and BITCOIN_RPC_PASSWORD; an archive node, prune=0, Bitcoin Core 23.0+ or newer)."

func (bitcoinSpec) fixtures(t *testing.T, folder string) []chainFixture {
	return loadBitcoinFixtures(t, folder)
}

func (bitcoinSpec) modes() []receiptMode {
	// failBatchReceipts is the harness's shared "force the fallback path"
	// flag; for bitcoin it flips the primary getblock to Core's pruned-data
	// answer so the fetch routes through the archive endpoint.
	return []receiptMode{
		{name: "primary node"},
		{name: "archive fallback", failBatchReceipts: true},
	}
}

func (bitcoinSpec) server(t *testing.T, fixtures []chainFixture, mode receiptMode) *httptest.Server {
	// The mode only changes the primary endpoint's behavior; the archive
	// endpoint exists in both runs and is called only when the primary
	// reports pruned data.
	return newBitcoinRPCServer(fixtures, mode.failBatchReceipts).Server
}

func (bitcoinSpec) fetcher(t *testing.T, serverURL string) chains.Fetcher {
	// Construction is network-free (lazy HTTP POST), so httptest URLs work.
	// The primary endpoint sits at the server root and the archive endpoint
	// at /archive, and both are wired in every run: the mode changes only the
	// primary's behavior, so the archive client is simply never called in the
	// primary run. Basic auth is the client config contract, so the
	// mock-facing client carries dummy credentials.
	client, err := bitcoin.NewClient(config.BitcoinConfig{
		RPCURL:        serverURL,
		ArchiveRPCURL: serverURL + "/archive",
		RPCUser:       "golden",
		RPCPassword:   "golden",
	})
	require.NoError(t, err, "bitcoin client")
	return bitcoin.NewFetcher(client, 6)
}

func (bitcoinSpec) converter(_ *testing.T, cfg *config.Config) chains.Converter {
	// NewConverter is lenient by design: no error return, and a nil or empty
	// config falls back to the Bitcoin default prefix.
	return bitcoin.NewConverter(cfg)
}

func (bitcoinSpec) assertCoverage(t *testing.T, raw any) {
	block, ok := raw.(*bitcoin.Block)
	require.True(t, ok, "expected *bitcoin.Block, got %T", raw)
	assertBitcoinBlockCoverage(t, block)
}

func (bitcoinSpec) goldens() []goldenCollectionSpec {
	return bitcoinGoldenCollections
}

// loadBitcoinFixtures loads every block fixture from a chain folder. Bitcoin
// has no receipts twin: the fee and the prevout undo data ride inline in the
// verbosity-3 body, so one file per decimal height stem is the whole
// capture. The loader stays spec-owned (the shared loader hard-requires a
// receipts twin) and reuses the shared envelope-unwrap, so both capture
// styles work: a verbatim curl response body and a bare bitcoin-cli
// getblock output.
func loadBitcoinFixtures(t *testing.T, chainDir string) []chainFixture {
	t.Helper()

	blocksDir := filepath.Join(chainDir, "testdata", "blocks")
	entries, err := os.ReadDir(blocksDir)
	require.NoError(t, err, "read block fixtures at %s (%s)", blocksDir, bitcoinCaptureHint)

	var fixtures []chainFixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		blockRaw := loadFixtureResult(t, filepath.Join(blocksDir, entry.Name()))

		height, hash := parseBitcoinFixtureMeta(t, blockRaw)
		fixtures = append(fixtures, chainFixture{
			height:   height,
			hash:     hash,
			blockRaw: blockRaw,
		})
	}
	require.NotEmpty(t, fixtures, "no block fixtures found in %s; %s", blocksDir, bitcoinCaptureHint)
	return fixtures
}

// parseBitcoinFixtureMeta reads the decimal height and the content hash out
// of the verbosity-3 block body. The hash is the block's addressing key: the
// mock's getblockhash answers it for the height, and the getblock lookup is
// keyed by it. A zero height also marks a truncated capture, since genesis
// carries no fee or prevout data to freeze.
func parseBitcoinFixtureMeta(t *testing.T, blockRaw json.RawMessage) (int64, string) {
	t.Helper()

	var meta struct {
		Height int64  `json:"height"`
		Hash   string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(blockRaw, &meta), "bitcoin block fixture: missing or malformed height/hash")
	require.NotZero(t, meta.Height, "bitcoin block fixture: height missing or zero")
	require.NotEmpty(t, meta.Hash, "bitcoin block fixture: no hash field")

	return meta.Height, strings.ToLower(meta.Hash)
}

// bitcoinRPCError mirrors one JSON-RPC error object on the wire.
type bitcoinRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcErrf builds one typed RPC error with the given code.
func rpcErrf(code int, format string, args ...any) *bitcoinRPCError {
	return &bitcoinRPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// bitcoinRPCServer replays the captured verbosity-3 fixtures over Core's
// JSON-RPC 1.0 dialect. The primary endpoint sits at the server root and the
// archive endpoint at /archive, mirroring how the production client points
// rpc_url and archive_rpc_url at (possibly path-addressed) endpoints on one
// host. failPrimaryGetBlock turns the primary's getblock into Core's
// pruned-data answer, which is what forces the fetcher onto the archive
// endpoint for the archive-fallback run.
type bitcoinRPCServer struct {
	*httptest.Server
	byHeight            map[int64]chainFixture
	byHash              map[string]chainFixture
	failPrimaryGetBlock bool
}

func newBitcoinRPCServer(fixtures []chainFixture, failPrimaryGetBlock bool) *bitcoinRPCServer {
	byHeight := make(map[int64]chainFixture, len(fixtures))
	byHash := make(map[string]chainFixture, len(fixtures))
	for _, fixture := range fixtures {
		byHeight[fixture.height] = fixture
		byHash[fixture.hash] = fixture
	}
	s := &bitcoinRPCServer{byHeight: byHeight, byHash: byHash, failPrimaryGetBlock: failPrimaryGetBlock}
	mux := http.NewServeMux()
	// The primary endpoint owns the server root, so the plain server.URL
	// stays a valid rpc_url without relying on path handling.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.handle(true, w, r) })
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { s.handle(false, w, r) })
	s.Server = httptest.NewServer(mux)
	return s
}

// handle decodes one JSON-RPC request and answers with Core's envelope. A
// typed error rides HTTP 500 plus an error object the way Core reports RPC
// failures, so rpcclient decodes it into a typed *btcjson.RPCError and the
// fetcher's error classification sees the real code.
func (s *bitcoinRPCServer) handle(primary bool, w http.ResponseWriter, r *http.Request) {
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

	result, rpcErr := s.serve(primary, req.Method, req.Params)
	w.Header().Set("Content-Type", "application/json")
	if rpcErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": rpcErr, "id": id})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": id})
}

// serve resolves one RPC method against the captured fixtures.
func (s *bitcoinRPCServer) serve(primary bool, method string, params json.RawMessage) (json.RawMessage, *bitcoinRPCError) {
	var args []json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil || len(args) == 0 {
		return nil, rpcErrf(rpcCodeMisc, "malformed params for %s", method)
	}

	switch method {
	case methodGetBlockHash:
		var height int64
		if err := json.Unmarshal(args[0], &height); err != nil {
			return nil, rpcErrf(rpcCodeInvalidParameter, "getblockhash: expected a numeric height parameter")
		}
		fixture, ok := s.byHeight[height]
		if !ok {
			return nil, rpcErrf(rpcCodeInvalidParameter, "Block height out of range")
		}
		return quotedHash(fixture.hash), nil

	case methodGetBlock:
		var hash string
		if err := json.Unmarshal(args[0], &hash); err != nil {
			return nil, rpcErrf(rpcCodeInvalidParameter, "getblock: expected a block hash parameter")
		}
		var verbosity int
		if len(args) > 1 {
			_ = json.Unmarshal(args[1], &verbosity)
		}
		if verbosity != 3 {
			return nil, rpcErrf(rpcCodeInvalidParameter, "unexpected getblock verbosity %d: the golden flow only exercises verbosity 3", verbosity)
		}
		// The archive pass: the primary endpoint has pruned the block, which
		// routes the fetcher into the archive endpoint. Pruning hides the
		// block body, never the height-to-hash index, so getblockhash keeps
		// answering on the primary in both runs.
		if primary && s.failPrimaryGetBlock {
			return nil, rpcErrf(rpcCodeMisc, prunedDataMessage)
		}
		fixture, ok := s.byHash[hash]
		if !ok {
			return nil, rpcErrf(rpcCodeBlockNotFound, "Block not found")
		}
		return fixture.blockRaw, nil

	default:
		return nil, rpcErrf(rpcCodeMisc, "unexpected RPC method %q: the golden flow only exercises block fetches", method)
	}
}

// quotedHash quotes one block hash for the getblockhash result. Marshaling a
// string cannot fail, so the error is discarded by construction.
func quotedHash(hash string) json.RawMessage {
	raw, _ := json.Marshal(hash)
	return raw
}

// assertBitcoinBlockCoverage pins the fixture coverage the freeze requires:
// the captured block must exercise every document path the converter maps,
// so the goldens can only be generated from output that covers the full
// verbosity-3 shape. The capture script enforces the same bar; this
// re-checks it through the typed decode, so a hand-edited fixture cannot
// sneak through.
func assertBitcoinBlockCoverage(t *testing.T, block *bitcoin.Block) {
	t.Helper()

	require.NotNil(t, block, "fetched block is nil")
	require.NotEmpty(t, block.Hash, "fetched block has no hash; the capture is incomplete")
	require.NotZero(t, block.Height, "fetched block has no height; the capture is incomplete")
	require.NotEmpty(t, block.Tx, "fixture block carries no transactions")

	coinbase, regular := 0, 0
	feeBearing, prevouts, witnesses := 0, 0, 0
	wtxidDivergent, taproot := 0, 0
	addressed, addressless, fractional := 0, 0, 0

	for i := range block.Tx {
		tx := &block.Tx[i]
		require.NotEmpty(t, tx.TxID, "fixture transaction %d carries no txid", i)

		// The coinbase branch mirrors the converter's own detection: the
		// first input carries the coinbase script. Fee data only counts as
		// coverage on a regular transaction, since the coinbase is never
		// priced.
		if len(tx.Vin) > 0 && tx.Vin[0].Coinbase != "" {
			coinbase++
		} else {
			regular++
			if tx.Fee != nil {
				feeBearing++
			}
		}
		if tx.Hash != tx.TxID {
			wtxidDivergent++
		}
		for j := range tx.Vin {
			vin := &tx.Vin[j]
			if len(vin.TxInWitness) > 0 {
				witnesses++
			}
			if vin.Prevout != nil && vin.Prevout.Value > 0 {
				prevouts++
			}
			if vin.Prevout != nil && vin.Prevout.ScriptPubKey.Type == "witness_v1_taproot" {
				taproot++
			}
		}
		for k := range tx.Vout {
			vout := &tx.Vout[k]
			if vout.ScriptPubKey.Address != "" {
				addressed++
			} else {
				addressless++
			}
			if vout.ScriptPubKey.Type == "witness_v1_taproot" {
				taproot++
			}
			if strings.Contains(strconv.FormatFloat(vout.Value, 'f', -1, 64), ".") {
				fractional++
			}
		}
	}

	require.NotZero(t, coinbase, "fixture block carries no coinbase transaction; the capture is incomplete")
	require.NotZero(t, regular, "fixture block carries no transaction beyond the coinbase; pick a block with regular transactions")
	require.NotZero(t, feeBearing, "no transaction carries a fee; the block's undo data is missing, capture from an archive node (prune=0)")
	require.NotZero(t, prevouts, "no input carries denormalized prevout data with a non-zero value; capture from an archive node (prune=0)")
	require.NotZero(t, witnesses, "no input carries witness data; pick a post-SegWit block")
	require.NotZero(t, wtxidDivergent, "no transaction has hash != txid; pick a post-SegWit block")
	require.NotZero(t, taproot, "no witness_v1_taproot script; pick a post-Taproot block")
	require.NotZero(t, addressed, "no output carries a scriptPubKey address; the address mapping stays unfrozen")
	require.NotZero(t, addressless, "no address-less output (OP_RETURN or nonstandard); pick a block with one, the omitted-address shape stays unfrozen")
	require.NotZero(t, fractional, "no output value carries a satoshi fraction; the fractional satoshi conversion stays unfrozen")
}

// bitcoinGoldenCollections lists the collections the bitcoin freeze reads
// back. The field lists mirror the SDL field sets with relations and _docID
// excluded. BlockSignature and SnapshotSignature are schema-only here: the
// golden handler carries no signing identity, so they stay empty and unfrozen.
var bitcoinGoldenCollections = []goldenCollectionSpec{
	{
		name:   "block",
		column: func(c chains.Collections) string { return c.(*bitcoin.CollectionNames).Block },
		fields: []string{
			"hash", "number", "parentHash", "version", "merkleRoot",
			"time", "bits", "nonce", "chainwork",
		},
	},
	{
		name:   "transaction",
		column: func(c chains.Collections) string { return c.(*bitcoin.CollectionNames).Transaction },
		fields: []string{
			"txid", "hash", "blockHash", "blockNumber", "txIndex", "version",
			"locktime", "feeSat", "isCoinbase",
		},
	},
	{
		name:   "txInput",
		column: func(c chains.Collections) string { return c.(*bitcoin.CollectionNames).TxInput },
		fields: []string{
			"txid", "blockNumber", "inputIndex", "prevTxid", "prevVout",
			"scriptSigHex", "sequence", "coinbase", "witness",
			"prevoutValueSat", "prevoutAddress", "prevoutType",
		},
	},
	{
		name:   "txOutput",
		column: func(c chains.Collections) string { return c.(*bitcoin.CollectionNames).TxOutput },
		fields: []string{
			"txid", "blockNumber", "n", "valueSat", "scriptPubKeyHex",
			"scriptPubKeyAddress", "scriptPubKeyType",
		},
	},
}
