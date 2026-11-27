//go:build integration
// +build integration

package golden

// The golden e2e freeze harness: the real fetcher, converter, and
// BlockHandler run against captured chain fixtures served by a local mock
// endpoint, documents land in an embedded DefraDB, and the stored output is
// read back and compared with the committed golden files. Each chain case
// runs once per receipt mode its spec declares: the evm spec exercises both
// the batch receipt path and the per-tx fallback and the two runs must
// freeze identical documents, while a chain with a single receipts path
// (tron) runs once. The freeze is the no-unintended-change gate for the
// typed pipeline: regenerate deliberately with -golden-update and review
// the diff before committing it.
//
// This file carries the chain-agnostic half of the harness: the suite entry
// (case table, shared runner) plus the fixture loading, read-back, and
// serialization plumbing every chain spec shares. Chain-specific plumbing
// (meta parsing, mock endpoint, coverage assertions, golden collection
// lists) lives in the per-chain spec files. Everything stays deterministic:
// the only network is the in-process mock server each chain spec brings,
// and every compared value derives from the fixtures rather than
// wall-clock state.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

var update = flag.Bool("golden-update", false, "regenerate the golden files from the current pipeline output")

func TestMain(m *testing.M) {
	// Initialize logging before any production code path runs: the fetcher
	// logs through logger.Sugar without a nil guard on the fallback path.
	logger.InitConsoleOnly(true)
	flag.Parse()
	os.Exit(m.Run())
}

// startGoldenNode boots a fresh headless DefraDB node in a temp store dir
// and returns it with a teardown func. Every run gets its own store because
// the freeze reads the whole store back: a second receipt-mode run must not
// sit on top of the first run's accumulated documents, or the freezes could
// never agree. The t.Cleanup removal covers a failed boot, where the
// caller's deferred teardown never registers.
func startGoldenNode(t *testing.T) (*node.Node, func()) {
	t.Helper()

	storeDir, err := os.MkdirTemp("", "golden-defra")
	require.NoError(t, err, "create store dir")
	t.Cleanup(func() { _ = os.RemoveAll(storeDir) })

	ctx := context.Background()
	opts := options.Node().
		SetDisableAPI(true).
		SetDisableP2P(true)
	opts.Store().SetPath(storeDir)

	defraNode, err := node.New(ctx, opts)
	require.NoError(t, err, "create DefraDB node")
	require.NoError(t, defraNode.Start(ctx), "start DefraDB node")

	return defraNode, func() {
		_ = defraNode.Close(ctx)
		_ = os.RemoveAll(storeDir)
	}
}

// receiptMode picks which receipt path a run exercises. For evm that is the
// batch eth_getBlockReceipts call or the per-tx fallback the fetcher
// degrades to when the batch call fails; a chain with a single receipts
// path declares one mode and ignores failBatchReceipts.
type receiptMode struct {
	name              string
	failBatchReceipts bool
}

var (
	receiptModeBatch    = receiptMode{name: "batch receipts"}
	receiptModeFallback = receiptMode{name: "per-tx receipt fallback", failBatchReceipts: true}
)

// chainSpec is the per-chain plug the shared runner drives: every chain
// family describes how its fixtures load and address, which receipt modes
// run, how the mock endpoint serves them, which fetcher and converter pair
// consume them, what coverage the fixtures must show, and which golden
// collections the store is frozen into. A new chain is one spec
// implementation plus one entry in goldenCases.
type chainSpec interface {
	fixtures(t *testing.T, folder string) []chainFixture
	modes() []receiptMode
	server(t *testing.T, fixtures []chainFixture, mode receiptMode) *httptest.Server
	fetcher(t *testing.T, serverURL string) chains.Fetcher
	converter(t *testing.T, cfg *config.Config) chains.Converter
	assertCoverage(t *testing.T, raw any)
	goldens() []goldenCollectionSpec
}

// chainCase binds a fixture folder to its chain config and its per-chain
// spec: a new chain is a fixture folder under golden/ plus one entry here.
type chainCase struct {
	folder string
	cfg    *config.Config
	spec   chainSpec
}

func goldenCases() []chainCase {
	return []chainCase{
		{
			folder: "ethereum",
			cfg: &config.Config{
				Chain: config.ChainConfig{Name: "Ethereum", Network: "Mainnet"},
				Geth:  config.GethConfig{},
				Indexer: config.IndexerConfig{
					MaxDocsPerTxn:      1000,
					MaxTxDocsPerBatch:  100,
					MaxLogDocsPerBatch: 100,
					MaxALEDocsPerBatch: 100,
					ReceiptWorkers:     8,
				},
			},
			spec: evmSpec{},
		},
		{
			folder: "tron",
			cfg: &config.Config{
				Chain: config.ChainConfig{Name: "Tron", Network: "Mainnet"},
				Indexer: config.IndexerConfig{
					MaxDocsPerTxn:      1000,
					MaxTxDocsPerBatch:  100,
					MaxLogDocsPerBatch: 100,
				},
			},
			spec: tronSpec{},
		},
	}
}

func TestGoldenTypedPath(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.folder, func(t *testing.T) {
			var first map[string][]byte
			for i, mode := range tc.spec.modes() {
				// Goldens are generated from the chain's primary run (run
				// 0) only: any later run must reproduce the first run's
				// bytes, so a divergence between receipt paths fails loudly
				// instead of being written over. For evm that primary run
				// is the batch path; tron has a single run.
				writeGoldens := i == 0 && *update
				frozen := runChainGolden(t, tc, mode, writeGoldens)
				if i == 0 {
					first = frozen
					continue
				}
				require.Equal(t, first, frozen,
					"the receipt paths must freeze identical documents")
			}
		})
	}
}

// runChainGolden drives the full production path for one chain under one
// receipt mode: load the captured fixtures, replay them through the chain's
// mock endpoint, fetch through the real fetcher, convert, store, then read
// the store back and freeze it. The returned map holds the rendered golden
// bytes per collection, so the caller can prove every receipt path stores
// identical documents.
func runChainGolden(t *testing.T, tc chainCase, mode receiptMode, writeGoldens bool) map[string][]byte {
	t.Helper()
	ctx := context.Background()

	fixtures := tc.spec.fixtures(t, tc.folder)

	defraNode, teardown := startGoldenNode(t)
	defer teardown()

	converter := tc.spec.converter(t, tc.cfg)
	require.NoError(t, defradb.ApplyCollectionSchemas(ctx, defraNode, converter.Collections()), "%s: apply schema for %s", mode.name, tc.folder)

	server := tc.spec.server(t, fixtures, mode)
	defer server.Close()

	fetcher := tc.spec.fetcher(t, server.URL)
	defer func() { _ = fetcher.Close() }()

	handler, err := defra.NewBlockHandler(defraNode, 1000)
	require.NoError(t, err)

	for _, fixture := range fixtures {
		raw, err := fetcher.FetchBlock(ctx, fixture.height)
		require.NoError(t, err, "%s: fetch block %d through the typed pipeline", mode.name, fixture.height)

		tc.spec.assertCoverage(t, raw)

		result, err := converter.Convert(ctx, raw)
		require.NoError(t, err, "%s: convert block %d", mode.name, fixture.height)

		creation, err := handler.Store(ctx, result)
		require.NoError(t, err, "%s: store block %d", mode.name, fixture.height)
		require.NotEmpty(t, creation.BlockID, "%s: block %d stored without a docID", mode.name, fixture.height)
	}

	// Read-back happens once after every block is stored, so the goldens
	// freeze the whole store state of the chain, the way production
	// accumulates it.
	collections := converter.Collections()
	frozen := make(map[string][]byte, len(tc.spec.goldens()))
	for _, collection := range tc.spec.goldens() {
		rows := readCollectionDocs(ctx, t, defraNode, collection.column(collections), collection.fields)
		actual, err := stableJSON(rows)
		require.NoError(t, err, "%s: serialize %s golden", mode.name, collection.name)
		frozen[collection.name] = actual
		compareOrUpdateGolden(t, filepath.Join(tc.folder, "testdata", "golden", collection.name+".json"), actual, writeGoldens)
	}
	return frozen
}

// chainFixture is one captured block with its receipts, loaded from a
// chain's testdata folder. blockRaw and receiptsRaw keep the captured wire
// shape verbatim so served responses carry the same payload a real node
// returns. hash holds the chain's block addressing key: the EVM block hash
// or the tron blockID, derived by the chain's meta parser.
type chainFixture struct {
	height      int64
	hash        string
	blockRaw    json.RawMessage
	receiptsRaw json.RawMessage
}

// loadChainFixtures loads every block fixture (with its receipts twin) from
// a chain folder. Blocks are named by decimal height: blocks/HEIGHT.json
// plus receipts/HEIGHT.json; the stem keeps the two files paired without
// parsing the addressing key out of the filename. captureHint tells a
// missing or unreadable fixture set how to fix itself, so a chain that has
// not been captured yet fails with its own capture instructions.
func loadChainFixtures(t *testing.T, chainDir string, parseMeta func(*testing.T, json.RawMessage) (int64, string), captureHint string) []chainFixture {
	t.Helper()

	blocksDir := filepath.Join(chainDir, "testdata", "blocks")
	entries, err := os.ReadDir(blocksDir)
	require.NoError(t, err, "read block fixtures at %s (%s)", blocksDir, captureHint)

	var fixtures []chainFixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".json")
		fixtures = append(fixtures, loadChainFixture(t, chainDir, stem, parseMeta))
	}
	require.NotEmpty(t, fixtures, "no block fixtures found in %s; %s", blocksDir, captureHint)
	return fixtures
}

// loadChainFixture loads one block plus its receipts twin and derives the
// height and addressing key through the chain's meta parser.
func loadChainFixture(t *testing.T, chainDir, stem string, parseMeta func(*testing.T, json.RawMessage) (int64, string)) chainFixture {
	t.Helper()

	blockRaw := loadFixtureResult(t, filepath.Join(chainDir, "testdata", "blocks", stem+".json"))
	receiptsRaw := loadFixtureResult(t, filepath.Join(chainDir, "testdata", "receipts", stem+".json"))

	height, hash := parseMeta(t, blockRaw)
	return chainFixture{
		height:      height,
		hash:        hash,
		blockRaw:    blockRaw,
		receiptsRaw: receiptsRaw,
	}
}

// loadFixtureResult reads a captured fixture file and unwraps the JSON-RPC
// response envelope when one is present, so both save styles work: a full
// curl response body or a bare result payload. Chain APIs that serve no
// envelope (tron's wallet API) carry bare bodies, which pass through here
// unchanged.
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

// goldenCollectionSpec binds a golden file stem to the collection that
// provides its rows and to the chain-data fields the goldens compare. The
// field lists mirror the SDL field sets with the DefraDB artifacts
// excluded: no _docID and no relation link fields, so the freeze targets
// conversion output, not Defra's docID scheme. column resolves the stored
// collection name from whatever the chain's converter produced, so a spec
// never has to expose its concrete collection-name type to the runner.
type goldenCollectionSpec struct {
	name   string
	column func(chains.Collections) string
	fields []string
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
// file, or rewrites it when allowUpdate is set. Only the chain's primary run
// passes -golden-update through, so regeneration always freezes the primary
// receipt path and any later run can never overwrite the goldens with
// diverging bytes. Missing goldens without the flag are a hard failure
// pointing at the regeneration command.
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
