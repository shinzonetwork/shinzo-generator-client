//go:build integration
// +build integration

package golden

// The tron chain spec: the wallet API has no JSON-RPC envelope and a single
// receipts endpoint (gettransactioninfobyblocknum), so the tron case runs
// one pass against a mock wallet server that replays the captured fixture
// bodies verbatim. There is no batch/fallback split to force, unlike evm.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron"
)

// tronSpec implements chainSpec for the tron chain family.
type tronSpec struct{}

func (tronSpec) fixtures(t *testing.T, folder string) []chainFixture {
	return loadChainFixtures(t, folder, parseTronFixtureMeta,
		"make tron-golden-fetch captures a suitable mainnet block into this folder (requires TRON_RPC_URL; TRON_API_KEY is optional)")
}

func (tronSpec) modes() []receiptMode {
	return []receiptMode{{name: "block receipts"}}
}

func (tronSpec) server(t *testing.T, fixtures []chainFixture, _ receiptMode) *httptest.Server {
	// The mode is ignored on purpose: the wallet API has a single receipts
	// endpoint, so there is no batch/fallback split to force.
	return newTronWalletServer(fixtures).Server
}

func (tronSpec) fetcher(t *testing.T, serverURL string) chains.Fetcher {
	// The wallet client dials lazily per request, so an httptest URL works.
	return tron.NewFetcher(tron.NewClient(serverURL, "", "", "", 0))
}

func (tronSpec) converter(t *testing.T, cfg *config.Config) chains.Converter {
	// NewConverter is lenient by design: no error return, and a nil or
	// empty config falls back to the Tron default prefix.
	return tron.NewConverter(cfg)
}

func (tronSpec) assertCoverage(t *testing.T, raw any) {
	bundle, ok := raw.(*tron.BlockBundle)
	require.True(t, ok, "expected *tron.BlockBundle, got %T", raw)
	assertTronBundleCoverage(t, bundle)
}

func (tronSpec) goldens() []goldenCollectionSpec {
	return tronGoldenCollections
}

// parseTronFixtureMeta reads the blockID and the decimal height out of the
// wallet-API block body. blockID is the block's addressing key; the height
// lives at block_header.raw_data.number. An empty blockID is also what the
// wallet API's not-found marker decodes to, so a capture of an unknown
// block cannot sneak through as a valid fixture.
func parseTronFixtureMeta(t *testing.T, blockRaw json.RawMessage) (int64, string) {
	t.Helper()

	var meta struct {
		BlockID     string `json:"blockID"`
		BlockHeader struct {
			RawData struct {
				Number int64 `json:"number"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	require.NoError(t, json.Unmarshal(blockRaw, &meta), "tron block fixture: missing or malformed blockID/block_header")
	require.NotEmpty(t, meta.BlockID, "tron block fixture: no blockID field")
	require.NotZero(t, meta.BlockHeader.RawData.Number, "tron block fixture: block_header.raw_data.number missing or zero")

	return meta.BlockHeader.RawData.Number, meta.BlockID
}

// newTronWalletServer replays captured wallet-API fixtures verbatim over
// the five endpoints the tron client speaks: getblockbynum,
// gettransactioninfobyblocknum, getnowblock, getblockbyid, and the
// solidity getnowblock. Unknown heights answer the empty object, matching
// how a real node classifies an unknown block as not-found, and the mux
// serves 404 for unexpected paths so stray traffic fails loudly.
type tronWalletServer struct {
	*httptest.Server
	byHeight map[int64]chainFixture
	tip      int64
}

func newTronWalletServer(fixtures []chainFixture) *tronWalletServer {
	byHeight := make(map[int64]chainFixture, len(fixtures))
	tip := int64(0)
	for _, fixture := range fixtures {
		byHeight[fixture.height] = fixture
		if fixture.height > tip {
			tip = fixture.height
		}
	}
	s := &tronWalletServer{byHeight: byHeight, tip: tip}
	s.Server = httptest.NewServer(s.routes())
	return s
}

func (s *tronWalletServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/wallet/getblockbynum", s.serveBlockByNum)
	mux.HandleFunc("/wallet/gettransactioninfobyblocknum", s.serveReceipts)
	mux.HandleFunc("/wallet/getblockbyid", s.serveBlockByID)
	mux.HandleFunc("/wallet/getnowblock", func(w http.ResponseWriter, _ *http.Request) { writeTronJSON(w, s.blockBody(s.tip)) })
	mux.HandleFunc("/walletsolidity/getnowblock", func(w http.ResponseWriter, _ *http.Request) { writeTronJSON(w, s.blockBody(s.tip)) })
	return mux
}

// serveBlockByNum answers a height-keyed block request with the fixture
// body verbatim, or the empty object for an unknown height.
func (s *tronWalletServer) serveBlockByNum(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Num int64 `json:"num"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeTronJSON(w, map[string]string{"Error": "invalid request body"})
		return
	}
	writeTronJSON(w, s.blockBody(body.Num))
}

// serveBlockByID answers a blockID-keyed block request by matching the
// fixture addressing key, or the empty object for an unknown blockID.
func (s *tronWalletServer) serveBlockByID(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeTronJSON(w, map[string]string{"Error": "invalid request body"})
		return
	}
	for _, fixture := range s.byHeight {
		if fixture.hash == body.Value {
			writeTronJSON(w, fixture.blockRaw)
			return
		}
	}
	writeTronJSON(w, struct{}{})
}

// serveReceipts answers a height-keyed receipts request with the captured
// gettransactioninfobyblocknum body verbatim, or an empty array for an
// unknown height, matching the node's no-receipts shape.
func (s *tronWalletServer) serveReceipts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Num int64 `json:"num"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeTronJSON(w, map[string]string{"Error": "invalid request body"})
		return
	}
	fixture, ok := s.byHeight[body.Num]
	if !ok {
		writeTronJSON(w, []any{})
		return
	}
	writeTronJSON(w, fixture.receiptsRaw)
}

func (s *tronWalletServer) blockBody(height int64) json.RawMessage {
	fixture, ok := s.byHeight[height]
	if !ok {
		return json.RawMessage("{}")
	}
	return fixture.blockRaw
}

func writeTronJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// assertTronBundleCoverage pins the fixture coverage the freeze requires:
// the captured block must carry both contract outcomes and the derived
// document inputs (logs, internal transfers, at least one carrying a
// non-zero callValue), and the receipts must pin to the block, so the
// goldens can only be generated from output that exercises the full
// wallet-API path.
func assertTronBundleCoverage(t *testing.T, bundle *tron.BlockBundle) {
	t.Helper()

	require.NotNil(t, bundle.Block, "fetched bundle has no block")
	require.NotEmpty(t, bundle.Block.BlockID, "fetched block has no blockID; the capture is incomplete")
	require.NotEmpty(t, bundle.Transactions, "fixture block carries no transactions")

	require.Len(t, bundle.Receipts, len(bundle.Transactions),
		"fixture block receipts are incomplete: every transaction needs its gettransactioninfobyblocknum entry")

	blockNumber := bundle.Block.BlockHeader.RawData.Number
	blockTimestamp := bundle.Block.BlockHeader.RawData.Timestamp

	successful, failed := 0, 0
	transfer, smart := false, false
	for _, tx := range bundle.Transactions {
		require.NotNil(t, tx)
		// A missing ret entry counts as failed: the ret list carries the
		// VM outcome, and a captured transaction always has one.
		ret := ""
		if len(tx.Ret) > 0 {
			ret = tx.Ret[0].ContractRet
		}
		if ret == "SUCCESS" {
			successful++
		} else {
			failed++
		}
		// raw_data stays verbatim on the wire type, so the contract view is
		// decoded on demand exactly like the converter does; an empty body
		// means a synthetic transaction with no contracts to inspect.
		if len(tx.RawData) == 0 {
			continue
		}
		var view tron.TransactionRawData
		require.NoError(t, json.Unmarshal(tx.RawData, &view),
			"fixture transaction %s: raw_data does not decode", tx.TxID)
		for _, contract := range view.Contract {
			switch contract.Type {
			case "TransferContract":
				transfer = true
			case "TriggerSmartContract":
				smart = true
			}
		}
	}
	for _, receipt := range bundle.Receipts {
		require.NotNil(t, receipt, "fixture block carries a missing receipt; the capture is incomplete")
		require.Equal(t, blockNumber, receipt.BlockNumber, "receipt pin broken: receipt blockNumber must match the block")
		require.Equal(t, blockTimestamp, receipt.BlockTimeStamp, "receipt pin broken: receipt blockTimeStamp must match the block")
	}

	require.NotZero(t, successful, "fixture block carries no successful transaction; pick a block with both outcomes")
	require.NotZero(t, failed, "fixture block carries no failed transaction; pick a block with both outcomes")
	require.True(t, transfer, "fixture block carries no TransferContract; pick a block with a plain transfer")
	require.True(t, smart, "fixture block carries no TriggerSmartContract; pick a block with a smart-contract call")

	logs, internal, valuedInternal := 0, 0, 0
	for _, receipt := range bundle.Receipts {
		logs += len(receipt.Log)
		internal += len(receipt.InternalTransactions)
		for _, itx := range receipt.InternalTransactions {
			// The document builder reads the first callValueInfo entry, so
			// the freeze demands the value exactly where the frozen amount
			// will come from; a TRC-10 movement qualifies too, its token
			// amount is the callValue.
			if len(itx.CallValueInfo) > 0 && itx.CallValueInfo[0].CallValue > 0 {
				valuedInternal++
			}
		}
	}
	require.NotZero(t, logs, "fixture block carries no event log; pick a block with contract output")
	require.NotZero(t, internal, "fixture block carries no internal transfer; pick a block with internal movements")
	require.NotZero(t, valuedInternal, "fixture block carries no internal transfer with a non-zero callValue; pick a block with real asset movement")
}

// tronGoldenCollections lists the collections the tron freeze reads back.
// The field lists mirror the SDL field sets with relations and _docID
// excluded.
var tronGoldenCollections = []goldenCollectionSpec{
	{
		name:   "block",
		column: func(c chains.Collections) string { return c.(*tron.CollectionNames).Block },
		fields: []string{
			"hash", "number", "timestamp", "parentHash", "witnessAddress",
			"version", "txTrieRoot", "witnessSignature", "transactionCount",
		},
	},
	{
		name:   "transaction",
		column: func(c chains.Collections) string { return c.(*tron.CollectionNames).Transaction },
		fields: []string{
			"hash", "blockHash", "blockNumber", "type", "from", "to",
			"amount", "fee", "rawData", "ret", "energyUsage",
		},
	},
	{
		name:   "log",
		column: func(c chains.Collections) string { return c.(*tron.CollectionNames).Log },
		fields: []string{
			"address", "topics", "data", "transactionHash", "blockHash",
			"blockNumber", "transactionIndex", "logIndex",
		},
	},
	{
		name:   "internalTransfer",
		column: func(c chains.Collections) string { return c.(*tron.CollectionNames).InternalTransfer },
		fields: []string{
			"from", "to", "amount", "tokenId", "rejected",
			"transactionHash", "blockNumber", "transferIndex",
		},
	},
}
