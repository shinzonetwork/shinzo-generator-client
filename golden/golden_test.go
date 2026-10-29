//go:build integration
// +build integration

package golden

// The golden e2e freeze: the real fetcher, converter, and BlockHandler run
// against captured chain fixtures served by a local mock RPC endpoint,
// documents land in an embedded DefraDB, and the stored output is read back
// and compared with the committed golden files. The freeze is the
// no-unintended-change gate for the typed pipeline: regenerate deliberately
// with -golden-update and review the diff before committing it.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/node"
)

var update = flag.Bool("golden-update", false, "regenerate the golden files from the current pipeline output")

var defraNode *node.Node

func TestMain(m *testing.M) {
	// Initialize logging before any production code path runs: the fetcher
	// logs through logger.Sugar without a nil guard on the fallback path.
	logger.InitConsoleOnly(true)
	flag.Parse()

	ctx := context.Background()
	storeDir, err := os.MkdirTemp("", "golden-defra")
	if err != nil {
		logger.Sugar.Fatalf("golden: create store dir: %v", err)
	}

	// One headless node serves the whole suite; each chain case applies its
	// own prefix's schema on top. A fresh store per run keeps re-runs
	// hermetic: stored blocks never leak across invocations.
	opts := options.Node().
		SetDisableAPI(true).
		SetDisableP2P(true)
	opts.Store().SetPath(storeDir)

	defraNode, err = node.New(ctx, opts)
	if err != nil {
		logger.Sugar.Fatalf("golden: create DefraDB node: %v", err)
	}
	if err := defraNode.Start(ctx); err != nil {
		logger.Sugar.Fatalf("golden: start DefraDB node: %v", err)
	}

	code := m.Run()

	_ = defraNode.Close(ctx)
	_ = os.RemoveAll(storeDir)
	os.Exit(code)
}

// chainCase binds a fixture folder to its chain config: a new chain is a
// fixture folder under golden/ plus one entry here.
type chainCase struct {
	folder string
	cfg    *config.Config
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
		},
	}
}

func TestGoldenTypedPath(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.folder, func(t *testing.T) {
			runChainGolden(t, tc)
		})
	}
}

// runChainGolden drives the full production path for one chain: load the
// captured fixtures, replay them through a local RPC, fetch through the real
// fetcher, convert, store, then read the store back and freeze it.
func runChainGolden(t *testing.T, tc chainCase) {
	t.Helper()
	ctx := context.Background()

	fixtures := loadChainFixtures(t, tc.folder)

	converter := evm.NewConverter(tc.cfg)
	require.NoError(t, defradb.ApplyCollectionSchemas(ctx, defraNode, converter.Collections()), "apply schema for %s", tc.folder)

	server := newFixtureRPCServer(fixtures)
	defer server.Close()

	// The HTTP dial is lazy, so an httptest URL works; no WebSocket is
	// configured, matching the plain-HTTP production profile.
	client, err := evm.NewEthereumClient(ctx, server.URL, "", "", "X-Api-Key", 0)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	fetcher := evm.NewFetcher(client, 4)

	handler, err := defra.NewBlockHandler(defraNode, 1000)
	require.NoError(t, err)

	for _, fixture := range fixtures {
		raw, err := fetcher.FetchBlock(ctx, fixture.height)
		require.NoError(t, err, "fetch block %d through the typed pipeline", fixture.height)

		bundle, ok := raw.(*evm.BlockBundle)
		require.True(t, ok, "fetch block %d: expected *evm.BlockBundle, got %T", fixture.height, raw)
		assertBundleCoverage(t, bundle)

		result, err := converter.Convert(ctx, bundle)
		require.NoError(t, err, "convert block %d", fixture.height)

		creation, err := handler.Store(ctx, result)
		require.NoError(t, err, "store block %d", fixture.height)
		require.NotEmpty(t, creation.BlockID, "block %d stored without a docID", fixture.height)
	}

	// Read-back happens once after every block is stored, so the goldens
	// freeze the whole store state of the chain, the way production
	// accumulates it.
	names := converter.Collections().(*evm.CollectionNames)
	for _, collection := range goldenCollections {
		rows := readCollectionDocs(ctx, t, collection.column(names), collection.fields)
		actual, err := stableJSON(rows)
		require.NoError(t, err, "serialize %s golden", collection.name)
		compareOrUpdateGolden(t, filepath.Join(tc.folder, "testdata", "golden", collection.name+".json"), actual)
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
