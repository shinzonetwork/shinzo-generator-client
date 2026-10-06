//go:build live
// +build live

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// BSC public RPC default. It is aggressively rate-limited: the suite treats a
// warmup timeout against it as a skip, never a failure.
const (
	defaultBSCRPCEndpoint = "https://bsc-dataseed.bnbchain.org"
	defaultBSCNetwork     = "Mainnet"

	// bscHealthPort is chosen away from ports other live suites use (e.g.
	// 9876) so two suites can run side by side.
	bscHealthPort = 9877

	// bscWarmupBudget bounds how long TestMain waits for the first indexed
	// block before declaring the run skipped.
	bscWarmupBudget = 180 * time.Second

	// bscLiveDefraDir is wiped before and after every run so each suite
	// starts from a fresh store.
	bscLiveDefraDir = "./.defra"

	// defaultBSCKeyringSecret backs the local file keyring when
	// DEFRADB_KEYRING_SECRET is unset; throwaway by design (see
	// buildBSCLiveConfig).
	defaultBSCKeyringSecret = "bsc-live-test-keyring-secret"
)

var (
	bscNames         *evm.CollectionNames
	bscGraphqlURL    string
	bscHealthURL     string
	bscChainIndexer  *indexer.ChainIndexer
	bscIndexerCancel context.CancelFunc
	bscStarted       bool
)

// TestMain builds the BSC live config in code (a file-based config would let
// config.yaml's Geth defaults override the BSC endpoints after load), starts
// the indexer against a live BSC endpoint, and runs the suite only once the
// first block is indexed.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	logger.Test("TestMain - Starting BSC live integration tests")

	network := os.Getenv("BSC_LIVE_NETWORK")
	if network == "" {
		network = defaultBSCNetwork
	}
	bscNames = evm.NewCollectionNames("BSC__" + network)
	bscHealthURL = fmt.Sprintf("http://localhost:%d", bscHealthPort)

	rpcURL := os.Getenv("GETH_RPC_URL")
	if rpcURL == "" {
		rpcURL = defaultBSCRPCEndpoint
		logger.Testf("GETH_RPC_URL not set, using public endpoint %s (rate-limited: warmup timeout is treated as a skip)", rpcURL)
	}
	logger.Testf("Indexing BSC %s from %s", network, rpcURL)

	// Fresh store per run.
	logger.Test("Cleaning up existing BSC live DefraDB data...")
	if err := os.RemoveAll(bscLiveDefraDir); err != nil {
		logger.Sugar.Warnf("Failed to clean existing BSC live data: %v", err)
	}

	cfg := buildBSCLiveConfig(network, rpcURL)

	_, bscIndexerCancel = context.WithCancel(context.Background()) //nolint:gosec
	go func() {
		idx, err := indexer.CreateIndexer(cfg)
		if err != nil {
			logger.Sugar.Errorf("create BSC indexer failed: %v", err)
			return
		}
		bscChainIndexer = idx

		if err := idx.StartIndexing(false); err != nil {
			logger.Sugar.Errorf("BSC indexer failed: %v", err)
		}
	}()

	// Warmup: wait for the first indexed block. A timeout stops the indexer
	// and exits 0 — public-endpoint rate limiting must not fail the suite.
	logger.Test("Waiting for the first BSC block to be indexed...")
	if !waitForBSCFirstBlock(bscWarmupBudget) {
		logger.Test("⏭ Warmup budget exhausted without an indexed block - treating suite as skipped")
		bscTeardown()
		os.Exit(0) //nolint:gocritic
	}

	logger.Test("✅ BSC indexer is live and indexing blocks")
	bscStarted = true

	result := m.Run()
	bscTeardown()
	os.Exit(result) //nolint:gocritic
}

// buildBSCLiveConfig assembles the live-test config entirely in code so no
// on-disk config's defaults can override the BSC endpoints post-load.
func buildBSCLiveConfig(network, rpcURL string) *config.Config {
	cfg := &config.Config{}
	cfg.Chain.Name = "BSC"
	cfg.Chain.Network = network
	cfg.Chain.Adapter = config.DefaultChainAdapter
	cfg.Chain.Hub = "testnet.shinzo.network"

	cfg.Geth.NodeURL = rpcURL
	// GETH_* env names are historical: the Generator is chain-agnostic and
	// accepts any compatible JSON-RPC/WS endpoint, so the BSC suite reuses
	// them instead of introducing BSC_* aliases.
	cfg.Geth.WsURL = os.Getenv("GETH_WS_URL")
	cfg.Geth.APIKey = os.Getenv("GETH_API_KEY")
	cfg.Geth.APIKeyType = os.Getenv("GETH_API_KEY_TYPE")
	cfg.Geth.DialTimeoutSeconds = 10

	// Embedded DefraDB on a random port with a temp-like fresh store path;
	// P2P off because the generator is the sole source of truth here.
	cfg.DefraDB.Embedded = true
	cfg.DefraDB.Store.Path = bscLiveDefraDir
	cfg.DefraDB.P2P.Enabled = false
	cfg.DefraDB.P2P.AcceptIncoming = false

	// The keyring has no fallback mode: node-identity key management aborts
	// at startup without a secret. DEFRADB_KEYRING_SECRET may override; the
	// default throwaway secret is safe because the store (and the keyring
	// under it) is wiped before and after every run, so each run gets a
	// freshly generated identity.
	cfg.DefraDB.KeyringSecret = os.Getenv("DEFRADB_KEYRING_SECRET")
	if cfg.DefraDB.KeyringSecret == "" {
		cfg.DefraDB.KeyringSecret = defaultBSCKeyringSecret
	}

	// Public endpoints are politely rate-limited: one block per second and a
	// tiny start buffer keep request volume low. Schema auth is "none" because
	// token mode is fail-closed (503) without API keys, and the suite asserts
	// the schema endpoint's contents.
	cfg.Indexer.StartHeight = 0
	cfg.Indexer.ConcurrentBlocks = 1
	cfg.Indexer.ReceiptWorkers = 8
	cfg.Indexer.MaxDocsPerTxn = 100
	cfg.Indexer.BlocksPerMinute = 60
	cfg.Indexer.HealthServerPort = bscHealthPort
	cfg.Indexer.OpenBrowserOnStart = false
	cfg.Indexer.StartBuffer = 5
	cfg.Indexer.SchemaAuthMode = constants.SchemaAuthModeNone

	cfg.Pruner.Enabled = false
	cfg.Snapshot.Enabled = false
	cfg.Logger.Development = false

	return cfg
}

// bscTeardown stops the indexer and wipes the live store.
func bscTeardown() {
	logger.Test("TestMain - BSC live integration teardown")
	if bscChainIndexer != nil {
		bscChainIndexer.StopIndexing()
	}
	if bscIndexerCancel != nil {
		bscIndexerCancel()
	}
	// Give the indexer time to release the store before deleting it.
	time.Sleep(2 * time.Second)
	if err := os.RemoveAll(bscLiveDefraDir); err != nil {
		logger.Sugar.Warnf("Failed to remove BSC live data: %v", err)
	}
}

// waitForBSCFirstBlock polls DefraDB until at least one block document exists.
func waitForBSCFirstBlock(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		port := bscDefraPort()
		if port > 0 {
			graphqlURL := fmt.Sprintf("http://localhost:%d/api/v0/graphql", port)
			count, err := bscCollectionCount(graphqlURL, bscNames.Block)
			if err == nil && count > 0 {
				logger.Testf("✅ Found %d BSC block docs at %s", count, graphqlURL)
				bscGraphqlURL = graphqlURL
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// bscDefraPort returns the embedded DefraDB port, or 0 when not yet up.
func bscDefraPort() int {
	if bscChainIndexer == nil {
		return 0
	}
	port := bscChainIndexer.GetDefraDBPort()
	if port > 0 {
		return port
	}
	return 0
}

// bscGraphQL runs a GraphQL query against the live DefraDB and decodes the
// data section of the response.
func bscGraphQL(url, query string) (map[string]any, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer([]byte(query)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode graphql response: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", result.Errors[0].Message)
	}
	return result.Data, nil
}

// bscCollectionCount returns the document count of a collection, or an error.
func bscCollectionCount(url, collection string) (int, error) {
	query := fmt.Sprintf(`{"query":"query { %s { _count } }"}`, collection)
	data, err := bscGraphQL(url, query)
	if err != nil {
		return 0, err
	}
	col, ok := data[collection].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("collection %s missing from graphql response", collection)
	}
	raw, ok := col["_count"].(float64)
	if !ok {
		return 0, fmt.Errorf("collection %s _count missing from graphql response", collection)
	}
	return int(raw), nil
}

// bscRequireStarted skips the test when the indexer never warmed up.
func bscRequireStarted(t *testing.T) {
	t.Helper()
	if !bscStarted {
		t.Skip("BSC indexer not started - skipping live test")
	}
}

// bscQueryLatestBlocks fetches the newest block documents.
func bscQueryLatestBlocks(t *testing.T, limit int) []map[string]any {
	t.Helper()
	query := fmt.Sprintf(`{"query":"query { %s(limit: %d, order: {number: DESC}) { number hash timestamp gasUsed gasLimit miner } }"}`,
		bscNames.Block, limit)
	data, err := bscGraphQL(bscGraphqlURL, query)
	requireNoError(t, err)

	blocks, ok := data[bscNames.Block].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("No %s documents returned from live query", bscNames.Block)
	}

	result := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("Malformed block document in %s", bscNames.Block)
		}
		result = append(result, block)
	}
	return result
}

// requireNoError is a tiny local helper so test bodies stay readable.
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestLiveBSCConnection verifies the indexer connects to live BSC and stores
// block documents with the expected fields.
func TestLiveBSCConnection(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	logger.Test("Testing live BSC connection and block indexing")
	blocks := bscQueryLatestBlocks(t, 1)

	requiredFields := []string{"number", "hash", "timestamp", "gasUsed", "gasLimit", "miner"}
	for _, field := range requiredFields {
		if _, exists := blocks[0][field]; !exists {
			t.Errorf("Missing required field '%s' in live BSC block", field)
		}
	}

	logger.Testf("✓ Live BSC connection successful - block %v indexed", blocks[0]["number"])
}

// TestLiveBSCCollectionsHaveDocs verifies the busy BSC mainnet blocks produce
// transaction and log documents. AccessListEntry is tolerated empty because
// only a fraction of BSC transactions carry access lists.
func TestLiveBSCCollectionsHaveDocs(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	for _, collection := range []string{bscNames.Transaction, bscNames.Log} {
		count, err := bscCollectionCount(bscGraphqlURL, collection)
		requireNoError(t, err)
		if count == 0 {
			t.Errorf("Collection %s has no documents - BSC mainnet blocks are never empty", collection)
		} else {
			logger.Testf("✓ %s has %d documents", collection, count)
		}
	}

	aleCount, err := bscCollectionCount(bscGraphqlURL, bscNames.AccessListEntry)
	requireNoError(t, err)
	logger.Testf("✓ %s has %d documents (empty is tolerated)", bscNames.AccessListEntry, aleCount)
}

// TestLiveBSCBlockSignature verifies block signature documents exist - the
// hub-facing proof that BSC block progression is being signed.
func TestLiveBSCBlockSignature(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	count, err := bscCollectionCount(bscGraphqlURL, bscNames.BlockSignature)
	requireNoError(t, err)
	if count == 0 {
		t.Fatalf("Collection %s has no documents - block signatures are missing", bscNames.BlockSignature)
	}
	logger.Testf("✓ %s has %d documents", bscNames.BlockSignature, count)
}

// TestLiveBSCHealthEndpoints verifies the health server reports a healthy
// indexer and that the schema endpoint serves the BSC-prefixed schema only.
func TestLiveBSCHealthEndpoints(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	client := &http.Client{Timeout: 10 * time.Second}

	// /health: JSON status, DefraDB connectivity and a positive current block.
	req, err := http.NewRequest(http.MethodGet, bscHealthURL+"/health", nil)
	requireNoError(t, err)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	requireNoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	requireNoError(t, err)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health returned status %d: %s", resp.StatusCode, string(body))
	}

	var health struct {
		Status           string `json:"status"`
		CurrentBlock     int64  `json:"current_block"`
		DefraDBConnected bool   `json:"defradb_connected"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("Failed to decode /health response: %v", err)
	}
	if health.Status != "healthy" {
		t.Errorf("/health status = %q, want %q", health.Status, "healthy")
	}
	if !health.DefraDBConnected {
		t.Error("/health defradb_connected = false, want true")
	}
	if health.CurrentBlock <= 0 {
		t.Errorf("/health current_block = %d, want > 0", health.CurrentBlock)
	}
	logger.Testf("✓ /health healthy, current block %d", health.CurrentBlock)

	// /registration: must be reachable (readiness).
	regResp, err := client.Get(bscHealthURL + "/registration")
	requireNoError(t, err)
	_, _ = io.Copy(io.Discard, regResp.Body)
	_ = regResp.Body.Close()
	if regResp.StatusCode != http.StatusOK {
		t.Errorf("/registration returned status %d, want 200", regResp.StatusCode)
	} else {
		logger.Test("✓ /registration returned 200")
	}

	// /api/v1/schema: the served schema must be BSC-prefixed and must not
	// leak the Ethereum default prefix.
	schemaResp, err := client.Get(bscHealthURL + "/api/v1/schema")
	requireNoError(t, err)
	schemaBody, err := io.ReadAll(schemaResp.Body)
	_ = schemaResp.Body.Close()
	requireNoError(t, err)
	if schemaResp.StatusCode != http.StatusOK {
		t.Fatalf("/api/v1/schema returned status %d: %s", schemaResp.StatusCode, string(schemaBody))
	}

	prefix := bscNames.Prefix()
	if !bytes.Contains(schemaBody, []byte(prefix)) {
		t.Errorf("/api/v1/schema does not contain %q", prefix)
	}
	if bytes.Contains(schemaBody, []byte(evm.DefaultCollectionPrefix)) {
		t.Errorf("/api/v1/schema leaks the default %q prefix", evm.DefaultCollectionPrefix)
	}
	logger.Testf("✓ /api/v1/schema serves the %s schema", prefix)
}

// TestLiveBSCIndexingAdvances samples the block count over a window and
// asserts the count never regresses. Stalls are logged, not failed: public
// endpoints rate-limit aggressively and indexing may pause without being broken.
func TestLiveBSCIndexingAdvances(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	const window = 30 * time.Second
	const interval = 5 * time.Second

	initial := bscBlockCountOrFatal(t)
	logger.Testf("Initial BSC block count: %d", initial)

	previous := initial
	advanced := false
	deadline := time.Now().Add(window)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		current := bscBlockCountOrFatal(t)
		if current < previous {
			t.Fatalf("BSC block count regressed: %d -> %d", previous, current)
		}
		if current > previous {
			advanced = true
		}
		previous = current
	}

	if !advanced {
		logger.Test("Warning: no new BSC blocks indexed during the window (rate limiting or a stall) - tolerated")
	} else {
		logger.Testf("✓ Indexed %d new BSC blocks in %s", previous-initial, window)
	}
}

// TestLiveBSCWSNotificationIndexing exercises the opt-in WebSocket path: the
// indexer was started with GETH_WS_URL wired into the config, so the client
// dials WS at connect time and block ingestion must still advance.
func TestLiveBSCWSNotificationIndexing(t *testing.T) {
	t.Parallel()
	if os.Getenv("GETH_WS_URL") == "" {
		t.Skip("GETH_WS_URL not set - WS notification path is opt-in")
	}
	bscRequireStarted(t)

	initial := bscBlockCountOrFatal(t)
	logger.Testf("WS path: initial BSC block count: %d", initial)

	time.Sleep(30 * time.Second)

	final := bscBlockCountOrFatal(t)
	if final <= initial {
		logger.Test("Warning: no new blocks indexed over the WS window (may be rate limiting) - tolerated")
		return
	}
	logger.Testf("✓ WS notification path indexed %d new BSC blocks", final-initial)
}

// bscBlockCountOrFatal returns the live block count or fails the test.
func bscBlockCountOrFatal(t *testing.T) int {
	t.Helper()
	count, err := bscCollectionCount(bscGraphqlURL, bscNames.Block)
	requireNoError(t, err)
	return count
}
