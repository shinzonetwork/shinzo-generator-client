//go:build integration
// +build integration

package golden

// The golden e2e freeze: the real fetcher, converter, and BlockHandler run
// against captured chain fixtures served by a local mock RPC endpoint,
// documents land in an embedded DefraDB, and the stored output is read back
// and compared with the committed golden files. Every chain case runs twice,
// once through the batch receipt path and once through the per-tx fallback,
// and both runs must freeze identical documents. The freeze is the
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

func TestMain(m *testing.M) {
	// Initialize logging before any production code path runs: the fetcher
	// logs through logger.Sugar without a nil guard on the fallback path.
	logger.InitConsoleOnly(true)
	flag.Parse()
	os.Exit(m.Run())
}

// startGoldenNode boots a fresh headless DefraDB node in a temp store dir
// and returns it with a teardown func. Every run gets its own store because
// the freeze reads the whole store back: the fallback run must not sit on
// top of the batch run's accumulated documents, or the two freezes could
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

// receiptMode picks which receipt path a run exercises: the batch
// eth_getBlockReceipts call, or the per-tx eth_getTransactionReceipt
// fallback the fetcher degrades to when the batch call fails. Both paths
// must freeze identical documents.
type receiptMode struct {
	name              string
	failBatchReceipts bool
}

var (
	receiptModeBatch    = receiptMode{name: "batch receipts"}
	receiptModeFallback = receiptMode{name: "per-tx receipt fallback", failBatchReceipts: true}
)

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
			batch := runChainGolden(t, tc, receiptModeBatch)
			fallback := runChainGolden(t, tc, receiptModeFallback)
			require.Equal(t, batch, fallback,
				"the per-tx receipt fallback must freeze the same documents as the batch path")
		})
	}
}

// runChainGolden drives the full production path for one chain under one
// receipt mode: load the captured fixtures, replay them through a local RPC,
// fetch through the real fetcher, convert, store, then read the store back
// and freeze it. The returned map holds the rendered golden bytes per
// collection, so the caller can prove the batch and fallback receipt paths
// store identical documents.
func runChainGolden(t *testing.T, tc chainCase, mode receiptMode) map[string][]byte {
	t.Helper()
	ctx := context.Background()

	fixtures := loadChainFixtures(t, tc.folder)

	defraNode, teardown := startGoldenNode(t)
	defer teardown()

	converter := evm.NewConverter(tc.cfg)
	require.NoError(t, defradb.ApplyCollectionSchemas(ctx, defraNode, converter.Collections()), "%s: apply schema for %s", mode.name, tc.folder)

	server := newFixtureRPCServer(fixtures, mode.failBatchReceipts)
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
		require.NoError(t, err, "%s: fetch block %d through the typed pipeline", mode.name, fixture.height)

		bundle, ok := raw.(*evm.BlockBundle)
		require.True(t, ok, "%s: fetch block %d: expected *evm.BlockBundle, got %T", mode.name, fixture.height, raw)
		assertBundleCoverage(t, bundle)

		result, err := converter.Convert(ctx, bundle)
		require.NoError(t, err, "%s: convert block %d", mode.name, fixture.height)

		creation, err := handler.Store(ctx, result)
		require.NoError(t, err, "%s: store block %d", mode.name, fixture.height)
		require.NotEmpty(t, creation.BlockID, "%s: block %d stored without a docID", mode.name, fixture.height)
	}

	// Read-back happens once after every block is stored, so the goldens
	// freeze the whole store state of the chain, the way production
	// accumulates it.
	names := converter.Collections().(*evm.CollectionNames)
	frozen := make(map[string][]byte, len(goldenCollections))
	for _, collection := range goldenCollections {
		rows := readCollectionDocs(ctx, t, defraNode, collection.column(names), collection.fields)
		actual, err := stableJSON(rows)
		require.NoError(t, err, "%s: serialize %s golden", mode.name, collection.name)
		frozen[collection.name] = actual
		// Goldens are generated from the batch path only: the fallback run
		// compares against the same bytes, so a divergence between the two
		// receipt paths fails loudly instead of being written over.
		compareOrUpdateGolden(t, filepath.Join(tc.folder, "testdata", "golden", collection.name+".json"), actual, *update && !mode.failBatchReceipts)
	}
	return frozen
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
