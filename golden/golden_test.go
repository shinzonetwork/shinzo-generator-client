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
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defradb"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client/options"
	"github.com/sourcenetwork/defradb/crypto"
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
// fixture folder under golden/ plus one entry here. captures is how many
// supply captures a tokens-mode case's block carries.
type chainCase struct {
	folder   string
	cfg      *config.Config
	captures int
}

// usd1 is the USD1 contract on Ethereum and BSC; its total supply is in
// storage slot 0x35 on both.
const usd1 = "0x8d0d000ee44948fc98c9b98a4fa4921476f08b0d"

// tokensConfig is a tokens-mode config listing USD1, capturing its supply at
// every height that is a multiple of interval.
func tokensConfig(name string, chainID uint64, interval int64) *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name: name, Network: "Mainnet", Mode: config.ChainModeTokens, ChainID: chainID,
			Tokens:               []config.Token{{Address: usd1, SupplySlot: "0x35"}},
			StateCaptureInterval: interval,
		},
		Indexer: config.IndexerConfig{MaxDocsPerTxn: 1000, ReceiptWorkers: 8},
	}
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
		// Ethereum 26378186 is not a multiple of 4, so it carries no capture.
		{folder: "ethereum-tokens", cfg: tokensConfig("Ethereum", 1, 4)},
		// BSC 132610290 is a multiple of 10, so it carries USD1's supply.
		{folder: "bsc-tokens", cfg: tokensConfig("Bsc", 56, 10), captures: 1},
	}
}

// goldenIdentity is a fixed secp256k1 identity, so the attestations a
// tokens-mode case signs are the same on every run.
func goldenIdentity(t *testing.T) identity.FullIdentity {
	t.Helper()
	key, err := crypto.PrivateKeyFromBytes(crypto.KeyTypeSecp256k1, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	ident, err := identity.FromPrivateKey(key)
	require.NoError(t, err)
	return ident
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

	converter, err := evm.NewConverter(tc.cfg)
	require.NoError(t, err, "%s: converter for %s", mode.name, tc.folder)
	require.NoError(t, defradb.ApplyCollectionSchemas(ctx, defraNode, converter.Collections()), "%s: apply schema for %s", mode.name, tc.folder)

	server := newFixtureRPCServer(fixtures, mode.failBatchReceipts, tc.cfg.Chain.ChainID)
	defer server.Close()

	// The HTTP dial is lazy, so an httptest URL works; no WebSocket is
	// configured, matching the plain-HTTP production profile. A tokens-mode
	// fetcher is built from its config, as production builds it, and signs
	// with a fixed identity.
	tokens := tc.cfg.Chain.Mode == config.ChainModeTokens
	var fetcher *evm.Fetcher
	if tokens {
		cfg := *tc.cfg
		cfg.Geth.NodeURL = server.URL
		fetcher, err = evm.NewFetcherFromConfig(&cfg)
		require.NoError(t, err)
		require.NoError(t, fetcher.Connect(ctx), "%s: connect to the fixture server", mode.name)
		defer func() { _ = fetcher.Close() }()
		ctx = defracontext.WithIdentity(ctx, goldenIdentity(t))
	} else {
		client, err := evm.NewEthereumClient(ctx, server.URL, "", "", "X-Api-Key", 0)
		require.NoError(t, err)
		defer func() { _ = client.Close() }()
		fetcher = evm.NewFetcher(client, 4)
	}

	handler, err := defra.NewBlockHandler(defraNode, 1000)
	require.NoError(t, err)

	for _, fixture := range fixtures {
		raw, err := fetcher.FetchBlock(ctx, fixture.height)
		require.NoError(t, err, "%s: fetch block %d through the typed pipeline", mode.name, fixture.height)

		bundle, ok := raw.(*evm.BlockBundle)
		require.True(t, ok, "%s: fetch block %d: expected *evm.BlockBundle, got %T", mode.name, fixture.height, raw)
		if tokens {
			require.Len(t, bundle.StateCaptures, tc.captures, "%s: supply captures at block %d", mode.name, fixture.height)
		} else {
			assertBundleCoverage(t, bundle)
		}

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
	collections := goldenCollections
	if tokens {
		collections = tokenCollections
	}
	frozen := make(map[string][]byte, len(collections))
	for _, collection := range collections {
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
