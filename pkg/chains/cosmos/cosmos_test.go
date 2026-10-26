package cosmos

// Tests for the cosmos-evm chain family against blocks captured from
// ShinzoHub testnet (testdata/): Cosmos REST GetBlockWithTxs responses,
// CometBFT block_results, and the canonical tx hashes from tx_search.

import (
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
)

// fixtureHeights: a MsgEthereumTx (indexerregistry precompile) block, a
// MsgIndexerAssertion block, an IBC MsgUpdateClient block, and an empty block.
var fixtureHeights = []int64{5708560, 5888399, 6032603, 5708559} //nolint:gochecknoglobals // read-only fixture index

var fixtureTypeURLs = map[int64][]string{ //nolint:gochecknoglobals // read-only fixture expectations
	5708560: {"/cosmos.evm.vm.v1.MsgEthereumTx"},
	5888399: {"/shinzonetwork.indexer.v1.MsgIndexerAssertion"},
	6032603: {"/ibc.core.client.v1.MsgUpdateClient"},
	5708559: nil,
}

const finalizeEventsPerFixtureBlock = 20

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

// wantTxHashes returns the tx_search hashes of a fixture block in block order.
func wantTxHashes(t *testing.T, height int64) []string {
	t.Helper()
	var txs []struct {
		Hash  string `json:"hash"`
		Index int    `json:"index"`
	}
	require.NoError(t, json.Unmarshal(readFixture(t, fmt.Sprintf("tx_hashes_%d.json", height)), &txs))
	sort.Slice(txs, func(i, j int) bool { return txs[i].Index < txs[j].Index })
	hashes := make([]string, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash
	}
	return hashes
}

// newFixtureServer serves the REST and CometBFT endpoints for the fixture
// heights; mutate lets a test tamper with block_results.
func newFixtureServer(t *testing.T, mutate func(results map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/cosmos/tx/v1beta1/txs/block/"):
			height := strings.TrimPrefix(r.URL.Path, "/cosmos/tx/v1beta1/txs/block/")
			data, err := os.ReadFile(filepath.Join("testdata", "lcd_block_"+height+".json"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(data)
		case r.URL.Path == "/block_results":
			data, err := os.ReadFile(filepath.Join("testdata", "block_results_"+r.URL.Query().Get("height")+".json"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			var results map[string]any
			if err := json.Unmarshal(data, &results); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if mutate != nil {
				mutate(results)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": -1, "result": results})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func fetchFixtureBlock(t *testing.T, height int64) *Block {
	t.Helper()
	server := newFixtureServer(t, nil)
	block, err := NewClient(server.URL, server.URL, 0).FetchBlock(context.Background(), height)
	require.NoError(t, err)
	return block
}

// evmBundleFor builds the EVM side of a cosmos-evm block: same height and
// hash, no EVM transactions (EVM conversion is covered by pkg/chains/evm).
func evmBundleFor(block *Block) *evm.BlockBundle {
	return &evm.BlockBundle{Block: &evm.Block{
		Number:    strconv.FormatInt(block.Height, 10),
		Hash:      block.Hash,
		Timestamp: "1760028839",
	}}
}

func shinzohubConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Chain.Name = "Shinzohub"
	cfg.Chain.Network = "Testnet"
	cfg.Chain.Adapter = config.CosmosEVMChainAdapter
	return cfg
}

func newTestConverter(t *testing.T) *Converter {
	t.Helper()
	conv, err := NewConverter(shinzohubConfig())
	require.NoError(t, err)
	return conv
}

// --- Client ---

func TestClientFetchBlock_TestnetFixtures(t *testing.T) {
	t.Parallel()
	for _, height := range fixtureHeights {
		t.Run(strconv.FormatInt(height, 10), func(t *testing.T) {
			t.Parallel()
			block := fetchFixtureBlock(t, height)

			assert.Equal(t, height, block.Height)
			assert.True(t, strings.HasPrefix(block.Hash, "0x") && len(block.Hash) == 66, "hash %s", block.Hash)
			assert.Len(t, block.FinalizeEvents, finalizeEventsPerFixtureBlock)

			want := wantTxHashes(t, height)
			require.Len(t, block.Transactions, len(want))
			for i, tx := range block.Transactions {
				assert.Equal(t, want[i], tx.Hash, "tx %d hash must equal CometBFT's sha256 tx hash", i)
				assert.Equal(t, i, tx.Index)
				assert.NotEmpty(t, tx.GasUsed)
				assert.NotEmpty(t, tx.Events)
				assert.True(t, json.Valid([]byte(tx.Fee)), "fee must be JSON")
			}

			var typeURLs []string
			for _, tx := range block.Transactions {
				for _, msg := range tx.Messages {
					typeURLs = append(typeURLs, msg.TypeURL)
					assert.True(t, json.Valid([]byte(msg.Body)), "message body must be JSON")
				}
			}
			assert.Equal(t, fixtureTypeURLs[height], typeURLs)
		})
	}
}

func TestClientFetchBlock_MessageBodyIsDecodedJSON(t *testing.T) {
	t.Parallel()
	block := fetchFixtureBlock(t, 5888399)
	var body struct {
		SourceChain   string `json:"source_chain"`
		SourceChainID string `json:"source_chain_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(block.Transactions[0].Messages[0].Body), &body))
	assert.Equal(t, "ethereum", body.SourceChain)
	assert.Equal(t, "1", body.SourceChainID)
}

func TestClientFetchBlock_TxCountMismatchFails(t *testing.T) {
	t.Parallel()
	server := newFixtureServer(t, func(results map[string]any) { results["txs_results"] = []any{} })
	_, err := NewClient(server.URL, server.URL, 0).FetchBlock(context.Background(), 5708560)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tx count mismatch")
}

func TestClientFetchBlock_HeightMismatchFails(t *testing.T) {
	t.Parallel()
	server := newFixtureServer(t, func(results map[string]any) { results["height"] = "1" })
	_, err := NewClient(server.URL, server.URL, 0).FetchBlock(context.Background(), 5708560)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block_results returned height")
}

func TestClientFetchBlock_HTTPErrorFails(t *testing.T) {
	t.Parallel()
	server := newFixtureServer(t, nil)
	_, err := NewClient(server.URL, server.URL, 0).FetchBlock(context.Background(), 42)
	require.Error(t, err)
}

// --- Converter ---

func TestConvert_GroupsFollowBlockAndEVMGroups(t *testing.T) {
	t.Parallel()
	block := fetchFixtureBlock(t, 5708560)
	conv := newTestConverter(t)

	result, err := conv.Convert(context.Background(), &Bundle{EVM: evmBundleFor(block), Cosmos: block})
	require.NoError(t, err)

	collections := make([]string, len(result.Groups))
	for i, g := range result.Groups {
		collections[i] = g.Collection
		assert.NotEmpty(t, g.BlockNumField, "group %s", g.Collection)
	}
	assert.Equal(t, []string{
		"Shinzohub__Testnet__Block",
		"Shinzohub__Testnet__CosmosTransaction",
		"Shinzohub__Testnet__CosmosMessage",
		"Shinzohub__Testnet__CosmosEvent",
	}, collections)
	assert.Len(t, result.Groups[1].Docs, 1)
	assert.Len(t, result.Groups[2].Docs, 1)
	assert.Len(t, result.Groups[3].Docs, len(block.Transactions[0].Events)+finalizeEventsPerFixtureBlock)
	assert.Equal(t, "Shinzohub__Testnet__BlockSignature", result.SignatureCollection)
}

func TestConvert_EmptyBlockKeepsFinalizeEvents(t *testing.T) {
	t.Parallel()
	block := fetchFixtureBlock(t, 5708559)
	result, err := newTestConverter(t).Convert(context.Background(), &Bundle{EVM: evmBundleFor(block), Cosmos: block})
	require.NoError(t, err)
	require.Len(t, result.Groups, 2)
	assert.Equal(t, "Shinzohub__Testnet__CosmosEvent", result.Groups[1].Collection)
	for _, doc := range result.Groups[1].Docs {
		assert.Equal(t, eventPhaseFinalize, doc["phase"])
		assert.NotContains(t, doc, transactionHashFieldName, "block-level events have no parent tx")
	}
}

func TestConvert_RejectsMismatchedViews(t *testing.T) {
	t.Parallel()
	block := fetchFixtureBlock(t, 5708560)
	conv := newTestConverter(t)

	wrongHash := evmBundleFor(block)
	wrongHash.Block.Hash = "0x" + strings.Repeat("ab", 32)
	_, err := conv.Convert(context.Background(), &Bundle{EVM: wrongHash, Cosmos: block})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash mismatch")

	wrongHeight := evmBundleFor(block)
	wrongHeight.Block.Number = "1"
	_, err = conv.Convert(context.Background(), &Bundle{EVM: wrongHeight, Cosmos: block})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "joined with Cosmos block")

	_, err = conv.Convert(context.Background(), evmBundleFor(block))
	require.Error(t, err)
}

// --- Registry and collections ---

func TestRegistry_CosmosEVMAdapter(t *testing.T) {
	t.Parallel()
	cfg := shinzohubConfig()
	cfg.Cosmos.RPCURL, cfg.Cosmos.LCDURL = "http://127.0.0.1:1", "http://127.0.0.1:1"

	conv, err := chains.NewConverter(cfg)
	require.NoError(t, err)
	assert.IsType(t, &Converter{}, conv)

	cols, err := chains.NewCollections(cfg)
	require.NoError(t, err)
	assert.Equal(t, "Shinzohub__Testnet", cols.Prefix())
	assert.Equal(t, "Shinzohub__Testnet__Block", cols.BlockCollection())
	assert.Subset(t, cols.AllCollections(), []string{
		"Shinzohub__Testnet__Transaction", "Shinzohub__Testnet__Log",
		"Shinzohub__Testnet__CosmosTransaction", "Shinzohub__Testnet__CosmosMessage", "Shinzohub__Testnet__CosmosEvent",
	})

	files, err := cols.CollectionFiles()
	require.NoError(t, err)
	require.Len(t, files, len(cols.AllCollections()))
	for _, f := range files {
		assert.NotContains(t, f.SDL, sdlPrefix, "%s must have the placeholder prefix replaced", f.File)
		assert.NotContains(t, f.SDL, evm.DefaultCollectionPrefix, "%s must have the EVM placeholder replaced", f.File)
	}
}

// --- End to end: write testnet blocks into DefraDB ---

func TestStore_TestnetBlocksIntoDefra(t *testing.T) {
	t.Parallel()
	conv := newTestConverter(t)
	sdl, err := conv.GetSchema()
	require.NoError(t, err)
	td := testutils.SetupTestDefraDBWithSchema(t, sdl)
	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)
	ctx := context.Background()

	wantEvents := 0
	for _, height := range fixtureHeights {
		block := fetchFixtureBlock(t, height)
		result, err := conv.Convert(ctx, &Bundle{EVM: evmBundleFor(block), Cosmos: block})
		require.NoError(t, err)
		_, err = handler.Store(ctx, result)
		require.NoError(t, err, "store block %d", height)
		wantEvents += finalizeEventsPerFixtureBlock
		for _, tx := range block.Transactions {
			wantEvents += len(tx.Events)
		}
	}

	query := func(q string) map[string]any {
		res := td.Node.DB.ExecRequest(ctx, q)
		require.Empty(t, res.GQL.Errors, q)
		data, ok := res.GQL.Data.(map[string]any)
		require.True(t, ok)
		return data
	}
	rows := func(data map[string]any, col string) []map[string]any {
		switch typed := data[col].(type) {
		case []map[string]any:
			return typed
		case []any:
			out := make([]map[string]any, 0, len(typed))
			for _, r := range typed {
				out = append(out, r.(map[string]any))
			}
			return out
		}
		return nil
	}

	// Messages are linked to their transaction and the shared block.
	msgs := rows(query(`query { Shinzohub__Testnet__CosmosMessage(filter: {typeUrl: {_eq: "/shinzonetwork.indexer.v1.MsgIndexerAssertion"}}) {
		blockNumber typeUrl transaction { hash code } block { number hash } } }`), "Shinzohub__Testnet__CosmosMessage")
	require.Len(t, msgs, 1)
	tx, ok := msgs[0]["transaction"].(map[string]any)
	require.True(t, ok, "message must link to its CosmosTransaction")
	assert.Equal(t, wantTxHashes(t, 5888399)[0], tx["hash"])
	blk, ok := msgs[0]["block"].(map[string]any)
	require.True(t, ok, "message must link to the shared Block")
	assert.EqualValues(t, 5888399, blk["number"])

	assert.Len(t, rows(query(`query { Shinzohub__Testnet__CosmosTransaction { hash } }`), "Shinzohub__Testnet__CosmosTransaction"), 3)
	assert.Len(t, rows(query(`query { Shinzohub__Testnet__CosmosEvent { eventIndex } }`), "Shinzohub__Testnet__CosmosEvent"), wantEvents)

	// Progress and pruning queries see the Cosmos collections.
	highest, err := conv.GetHighestStoredBlockNumber(ctx, td.Node)
	require.NoError(t, err)
	assert.EqualValues(t, 6032603, highest)

	ids, err := conv.GetDocIDsByBlockRange(ctx, td.Node, 5888399, 5888399)
	require.NoError(t, err)
	assert.Len(t, ids["Shinzohub__Testnet__CosmosTransaction"], 1)
	assert.Len(t, ids["Shinzohub__Testnet__CosmosMessage"], 1)
	assert.NotEmpty(t, ids["Shinzohub__Testnet__CosmosEvent"])
	assert.Len(t, ids["Shinzohub__Testnet__Block"], 1)
}

// --- Kava (CometBFT v0.37) ---

// kavaFixtureHeights: a MsgEthereumTx block, a mixed EVM + MsgDelegate
// block, a native pricefeed block, and an empty block, captured from Kava
// mainnet. Their block_results carry begin/end block events, no finalize.
var kavaFixtureHeights = []int64{23209054, 23209012, 23208969, 23209032} //nolint:gochecknoglobals // read-only fixture index

var kavaFixtureExpect = map[int64]struct { //nolint:gochecknoglobals // read-only fixture expectations
	typeURLs  []string
	endEvents int
}{
	23209054: {[]string{"/ethermint.evm.v1.MsgEthereumTx"}, 2},
	23209012: {[]string{"/ethermint.evm.v1.MsgEthereumTx", "/cosmos.staking.v1beta1.MsgDelegate", "/ethermint.evm.v1.MsgEthereumTx"}, 2},
	23208969: {[]string{"/kava.pricefeed.v1beta1.MsgPostPrice"}, 3},
	23209032: {nil, 2},
}

const kavaBeginEventsPerFixtureBlock = 142

func kavaConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Chain.Name = "Kava"
	cfg.Chain.Network = "Mainnet"
	cfg.Chain.Adapter = config.CosmosEVMChainAdapter
	return cfg
}

// kavaEVMHash reads the EVM block hash captured for the same height.
func kavaEVMHash(t *testing.T, height int64) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "evm", "testdata", "kava", fmt.Sprintf("block_%d.json", height)))
	require.NoError(t, err)
	var b struct {
		Hash string `json:"hash"`
	}
	require.NoError(t, json.Unmarshal(data, &b))
	return b.Hash
}

func TestClientFetchBlock_KavaFixtures(t *testing.T) {
	t.Parallel()
	for _, height := range kavaFixtureHeights {
		t.Run(strconv.FormatInt(height, 10), func(t *testing.T) {
			t.Parallel()
			block := fetchFixtureBlock(t, height)
			want := kavaFixtureExpect[height]

			assert.Equal(t, kavaEVMHash(t, height), block.Hash, "Cosmos block hash must equal the EVM block hash")
			assert.Empty(t, block.FinalizeEvents, "v0.37 has no finalize_block_events")
			assert.Len(t, block.BeginEvents, kavaBeginEventsPerFixtureBlock)
			assert.Len(t, block.EndEvents, want.endEvents)
			for i, ev := range block.BeginEvents {
				assert.Equal(t, i, ev.Index, "begin event index is its position within BeginBlock")
			}

			hashes := wantTxHashes(t, height)
			require.Len(t, block.Transactions, len(hashes))
			var typeURLs []string
			for i, tx := range block.Transactions {
				assert.Equal(t, hashes[i], tx.Hash, "tx %d hash", i)
				for _, msg := range tx.Messages {
					typeURLs = append(typeURLs, msg.TypeURL)
				}
			}
			assert.Equal(t, want.typeURLs, typeURLs)
		})
	}
}

func TestConvert_KavaBlockEventPhases(t *testing.T) {
	t.Parallel()
	block := fetchFixtureBlock(t, 23209012)
	conv, err := NewConverter(kavaConfig())
	require.NoError(t, err)
	result, err := conv.Convert(context.Background(), &Bundle{EVM: evmBundleFor(block), Cosmos: block})
	require.NoError(t, err)

	events := result.Groups[len(result.Groups)-1]
	require.Equal(t, "Kava__Mainnet__CosmosEvent", events.Collection)
	phases := map[any]int{}
	for _, doc := range events.Docs {
		phases[doc["phase"]]++
		if doc["phase"] != eventPhaseTx {
			assert.NotContains(t, doc, transactionHashFieldName, "block-level events have no parent tx")
		}
	}
	txEvents := 0
	for _, tx := range block.Transactions {
		txEvents += len(tx.Events)
	}
	assert.Equal(t, map[any]int{
		eventPhaseTx:    txEvents,
		eventPhaseBegin: kavaBeginEventsPerFixtureBlock,
		eventPhaseEnd:   kavaFixtureExpect[23209012].endEvents,
	}, phases)
}
