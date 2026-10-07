//go:build live
// +build live

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// BSC public RPC default. It is aggressively rate-limited: when the suite
// falls back to it (GETH_RPC_URL unset), a warmup timeout is a skip, never a
// failure. Against an explicitly configured endpoint a timeout fails hard.
const (
	defaultBSCRPCEndpoint = "https://bsc-dataseed.bnbchain.org"

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
	bscNames        *evm.CollectionNames
	bscGraphqlURL   string
	bscHealthURL    string
	bscChainIndexer atomic.Pointer[indexer.ChainIndexer]
	bscStarted      bool

	// bscUsingPublicRPC records that the configured endpoint came from the
	// built-in fallback rather than an explicit GETH_RPC_URL (or a literal
	// yaml URL). It decides whether a warmup timeout is a skip or a failure.
	bscUsingPublicRPC bool
)

// bscWarmupResult distinguishes how the warmup window ended: the two failure
// kinds have different exit semantics (a startup error is always fatal, a
// timeout only on a configured endpoint).
type bscWarmupResult int

const (
	bscWarmupReady bscWarmupResult = iota
	bscWarmupStartupFailed
	bscWarmupTimedOut
)

// TestMain boots the indexer against a live BSC endpoint using the shipped
// config/config_bsc.yaml — the same file operators run via -config — and runs
// the suite only once the first block is indexed. A config that fails to load
// is a bug in the bundled file, not endpoint flakiness, so it fails the run
// immediately instead of going through the warmup/skip path.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	logger.Test("TestMain - Starting BSC live integration tests")

	bscHealthURL = fmt.Sprintf("http://localhost:%d", bscHealthPort)

	cfg, err := buildBSCLiveConfig()
	if err != nil {
		logger.Sugar.Errorf("Failed to load BSC live config: %v", err)
		os.Exit(1)
	}

	bscNames = evm.NewCollectionNames(cfg.Chain.Name + "__" + cfg.Chain.Network)
	logger.Testf("Indexing BSC %s from %s", cfg.Chain.Network, cfg.Geth.NodeURL)

	// Fresh store per run.
	logger.Test("Cleaning up existing BSC live DefraDB data...")
	if err := os.RemoveAll(bscLiveDefraDir); err != nil {
		logger.Sugar.Warnf("Failed to clean existing BSC live data: %v", err)
	}

	// Startup errors travel on a buffered channel (the goroutine sends at most
	// one per lifetime, and a post-warmup send must never block on an unread
	// receiver — nothing selects on the channel once tests are running).
	bscIndexerErrs := make(chan error, 1)
	go func() {
		idx, err := indexer.CreateIndexer(cfg)
		if err != nil {
			bscIndexerErrs <- fmt.Errorf("create BSC indexer: %w", err)
			return
		}
		bscChainIndexer.Store(idx)

		if err := idx.StartIndexing(false); err != nil {
			bscIndexerErrs <- fmt.Errorf("BSC indexer exited: %w", err)
		}
	}()

	// Warmup: wait for the first indexed block. A startup failure always
	// fails the run — a bad schema, keyring or port clash is a bug, not
	// endpoint flakiness. A timeout fails only when an endpoint was explicitly
	// configured; against the rate-limited public fallback it stays a skip.
	logger.Test("Waiting for the first BSC block to be indexed...")
	res, startupErr := waitForBSCFirstBlock(bscWarmupBudget, bscIndexerErrs)
	switch res {
	case bscWarmupReady:
		// Proceed below.
	case bscWarmupStartupFailed:
		logger.Sugar.Errorf("BSC indexer failed during startup: %v", startupErr)
		bscTeardown()
		os.Exit(1) //nolint:gocritic
	case bscWarmupTimedOut:
		if bscUsingPublicRPC {
			logger.Test("⏭ Warmup budget exhausted without an indexed block - public endpoint is rate-limited, treating suite as skipped")
			bscTeardown()
			os.Exit(0) //nolint:gocritic
		}
		logger.Sugar.Errorf("Warmup budget exhausted without an indexed block on the configured endpoint (real endpoint, credentials or pipeline bug)")
		bscTeardown()
		os.Exit(1) //nolint:gocritic
	}

	logger.Test("✅ BSC indexer is live and indexing blocks")
	bscStarted = true

	result := m.Run()
	bscTeardown()
	os.Exit(result) //nolint:gocritic
}

// buildBSCLiveConfig loads the shipped config/config_bsc.yaml so the suite
// exercises the exact file operators run, then applies only test-specific
// overrides. Everything production-tuned (badger settings, batch sizes, the
// blocks_per_minute rate limit, pruner/snapshot lifecycle) flows through
// unmodified, together with the same environment-override machinery the real
// binary uses.
func buildBSCLiveConfig() (*config.Config, error) {
	cfg, err := config.LoadConfig("../../../config/config_bsc.yaml")
	if err != nil {
		return nil, fmt.Errorf("load config/config_bsc.yaml: %w", err)
	}

	// CHAIN_NAME is the one env override that must not leak in from a
	// developer .env: it would silently retarget the suite at another chain
	// and corrupt every collection-name assertion. Refuse loudly instead.
	if cfg.Chain.Name != "BSC" {
		return nil, fmt.Errorf("config chain name is %q, want %q: a CHAIN_NAME environment override is leaking into the suite", cfg.Chain.Name, "BSC")
	}

	// BSC_LIVE_NETWORK is the suite's network knob (e.g. Testnet); it applies
	// after load so the same on-disk config serves every network.
	if network := os.Getenv("BSC_LIVE_NETWORK"); network != "" {
		cfg.Chain.Network = network
	}

	// The yaml carries ${GETH_*} placeholders and the yaml library never
	// expands them: LoadConfig only fills these fields when the env var is
	// non-empty, so an unset variable leaves the literal placeholder behind.
	// Resolve env-first and clear the residue so the fields behave as unset.
	cfg.Geth.NodeURL = bscEnvOrConfig(cfg.Geth.NodeURL, "GETH_RPC_URL")
	cfg.Geth.WsURL = bscEnvOrConfig(cfg.Geth.WsURL, "GETH_WS_URL")
	cfg.Geth.APIKey = bscEnvOrConfig(cfg.Geth.APIKey, "GETH_API_KEY")
	cfg.Geth.APIKeyType = bscEnvOrConfig(cfg.Geth.APIKeyType, "GETH_API_KEY_TYPE")
	if cfg.Geth.NodeURL == "" {
		// Without a configured endpoint the suite defaults to the free public
		// one. Its rate limiting is why warmup timeouts against this fallback
		// are skips; an explicitly configured endpoint (even this same host)
		// never gets that treatment.
		cfg.Geth.NodeURL = defaultBSCRPCEndpoint
		bscUsingPublicRPC = true
		logger.Testf("GETH_RPC_URL not set, using public endpoint %s (rate-limited: warmup timeout is treated as a skip)", cfg.Geth.NodeURL)
	}

	// The store path is pinned to the same directory TestMain wipes before and
	// after every run, so store and wipe target can never drift apart.
	cfg.DefraDB.Store.Path = bscLiveDefraDir

	// Production enables P2P; the suite runs on dev machines and CI with no
	// bootstrap peers and nothing to exchange documents with, so no listener
	// is started and nothing may connect in.
	cfg.DefraDB.P2P.Enabled = false
	cfg.DefraDB.P2P.AcceptIncoming = false

	// The keyring has no fallback mode: node-identity key management aborts at
	// startup without a secret. DEFRADB_KEYRING_SECRET is already honored by
	// LoadConfig; the default throwaway secret is safe because the store (and
	// the keyring under it) is wiped before and after every run, so each run
	// gets a freshly generated identity.
	if cfg.DefraDB.KeyringSecret == "" {
		cfg.DefraDB.KeyringSecret = defaultBSCKeyringSecret
	}

	// The suite asserts fixed health URLs, and an INDEXER_HEALTH_SERVER_PORT
	// override in .env would move the server away from them.
	cfg.Indexer.HealthServerPort = bscHealthPort

	// Schema auth stays "none": token mode is fail-closed (503) without
	// SCHEMA_API_KEYS, and the suite asserts the schema endpoint's contents.
	cfg.Indexer.SchemaAuthMode = constants.SchemaAuthModeNone

	return cfg, nil
}

// bscEnvOrConfig resolves a config field that may still hold a literal
// "${VAR}" placeholder from the yaml when VAR was unset: the environment wins,
// otherwise placeholder or empty values are cleared so the field reads as
// unset.
func bscEnvOrConfig(value, envName string) string {
	if v := os.Getenv(envName); v != "" {
		return v
	}
	if value == "" || strings.HasPrefix(value, "${") {
		return ""
	}
	return value
}

// bscTeardown stops the indexer and wipes the live store.
func bscTeardown() {
	logger.Test("TestMain - BSC live integration teardown")
	if idx := bscChainIndexer.Load(); idx != nil {
		idx.StopIndexing()
	}
	// Give the indexer time to release the store before deleting it.
	time.Sleep(2 * time.Second)
	if err := os.RemoveAll(bscLiveDefraDir); err != nil {
		logger.Sugar.Warnf("Failed to remove BSC live data: %v", err)
	}
}

// waitForBSCFirstBlock polls DefraDB until the block collection reports a
// tip. The URL only resolves once the embedded node has bound its listener,
// and an error here (node still booting, transient transport) is retried,
// never fatal. Poll errors are logged (first, then every 10th attempt): a
// silently spinning poll hid a dead-URL failure mode for entire runs.
//
// startupErrs carries the indexer goroutine's failure; it is checked between
// and during every poll pause so a fast startup error ends the wait
// immediately. The error is returned verbatim for the caller to log and act
// on — reading it twice would deadlock on this one-element channel.
func waitForBSCFirstBlock(timeout time.Duration, startupErrs <-chan error) (bscWarmupResult, error) {
	deadline := time.Now().Add(timeout)
	attempts := 0

	for time.Now().Before(deadline) {
		select {
		case err := <-startupErrs:
			return bscWarmupStartupFailed, err
		default:
		}

		attempts++
		graphqlURL := bscDefraGraphQLURL()
		if graphqlURL != "" {
			tip, found, err := bscLatestNumber(graphqlURL, bscNames.Block, constants.NumberFieldName)
			if err == nil && found {
				logger.Testf("✅ BSC block collection live at %s, tip block %d", graphqlURL, tip)
				bscGraphqlURL = graphqlURL
				return bscWarmupReady, nil
			}
			if attempts == 1 || attempts%10 == 0 {
				if err != nil {
					logger.Testf("BSC warmup attempt %d failed: %v", attempts, err)
				} else if attempts%10 == 0 {
					logger.Testf("BSC warmup attempt %d: block collection still empty", attempts)
				}
			}
		} else if attempts == 1 || attempts%10 == 0 {
			logger.Testf("BSC warmup attempt %d: embedded DefraDB API URL not available yet", attempts)
		}

		select {
		case err := <-startupErrs:
			return bscWarmupStartupFailed, err
		case <-time.After(2 * time.Second):
		}
	}
	return bscWarmupTimedOut, nil
}

// bscDefraGraphQLURL returns the embedded node's GraphQL endpoint, or "" until
// the node has bound its listener. The node's APIURL is used as-is because an
// embedded node binds a non-loopback address with a random port; appending the
// GraphQL path mirrors how the health server and WaitForDefraDB build it.
func bscDefraGraphQLURL() string {
	idx := bscChainIndexer.Load()
	if idx == nil {
		return ""
	}
	url := idx.GetDefraDBURL()
	if url == "" {
		return ""
	}
	return strings.TrimSuffix(url, "/") + "/api/v0/graphql"
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

// bscDocExists reports whether a collection holds at least one document,
// querying rows for a number field instead of an aggregate: DefraDB v1's
// aggregate response shape proved unstable, while plain row queries are the
// shape the production converter and pruner rely on.
//
// Over HTTP the rows decode as []any of map[string]any (nil data = empty
// collection); []map[string]any is handled defensively to mirror the in-process
// shape, as in the replay acceptance harness.
func bscDocExists(url, collection, numberField string) (bool, error) {
	query := fmt.Sprintf(`{"query":"query { %s(filter: {%s: {_geq: 0}}, limit: 1) { %s } }"}`,
		collection, numberField, numberField)
	rows, err := bscQueryRows(url, query, collection)
	return len(rows) > 0, err
}

// bscLatestNumber returns the highest value of a collection's number field,
// using the same filter/order/limit query shape production code uses for its
// lowest/highest block-range reads. found is false when the collection is
// empty.
func bscLatestNumber(url, collection, numberField string) (int64, bool, error) {
	query := fmt.Sprintf(`{"query":"query { %s(filter: {%s: {_geq: 0}}, order: {%s: DESC}, limit: 1) { %s } }"}`,
		collection, numberField, numberField, numberField)
	rows, err := bscQueryRows(url, query, collection)
	if err != nil || len(rows) == 0 {
		return 0, false, err
	}
	raw, ok := rows[0][numberField].(float64)
	if !ok {
		return 0, false, fmt.Errorf("collection %s: field %s is not a number in response", collection, numberField)
	}
	return int64(raw), true, nil
}

// bscQueryRows runs a GraphQL row query and returns the collection's rows,
// tolerating both []any (over JSON) and []map[string]any (in-process) shapes,
// and treating null data as an empty collection.
func bscQueryRows(url, query, collection string) ([]map[string]any, error) {
	data, err := bscGraphQL(url, query)
	if err != nil {
		return nil, err
	}
	switch arr := data[collection].(type) {
	case []any:
		rows := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("collection %s: malformed row in graphql response", collection)
			}
			rows = append(rows, row)
		}
		return rows, nil
	case []map[string]any:
		return arr, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("collection %s: unexpected graphql response type %T", collection, data[collection])
	}
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
		exists, err := bscDocExists(bscGraphqlURL, collection, constants.BlockNumberFieldName)
		requireNoError(t, err)
		if !exists {
			t.Errorf("Collection %s has no documents - BSC mainnet blocks are never empty", collection)
		} else {
			logger.Testf("✓ %s has documents", collection)
		}
	}

	aleExists, err := bscDocExists(bscGraphqlURL, bscNames.AccessListEntry, constants.BlockNumberFieldName)
	requireNoError(t, err)
	if !aleExists {
		logger.Testf("%s is empty (empty is tolerated)", bscNames.AccessListEntry)
	} else {
		logger.Testf("✓ %s has documents", bscNames.AccessListEntry)
	}
}

// TestLiveBSCBlockSignature verifies block signature documents exist - the
// hub-facing proof that BSC block progression is being signed.
func TestLiveBSCBlockSignature(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	exists, err := bscDocExists(bscGraphqlURL, bscNames.BlockSignature, constants.BlockNumberFieldName)
	requireNoError(t, err)
	if !exists {
		t.Fatalf("Collection %s has no documents - block signatures are missing", bscNames.BlockSignature)
	}
	logger.Testf("✓ %s has documents", bscNames.BlockSignature)
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

// TestLiveBSCIndexingAdvances samples the indexed tip block number over a
// window and asserts it never regresses. The tip is monotonically increasing
// under normal operation, so it detects stalls while staying immune to the
// pruner legitimately shrinking the stored block range. Stalls are logged,
// not failed: public endpoints rate-limit aggressively and indexing may pause
// without being broken.
func TestLiveBSCIndexingAdvances(t *testing.T) {
	t.Parallel()
	bscRequireStarted(t)

	const window = 30 * time.Second
	const interval = 5 * time.Second

	initial := bscLatestTipOrFatal(t)
	logger.Testf("Initial BSC tip block: %d", initial)

	previous := initial
	advanced := false
	deadline := time.Now().Add(window)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		current := bscLatestTipOrFatal(t)
		if current < previous {
			t.Fatalf("BSC tip block regressed: %d -> %d", previous, current)
		}
		if current > previous {
			advanced = true
		}
		previous = current
	}

	if !advanced {
		logger.Test("Warning: no new BSC blocks indexed during the window (rate limiting or a stall) - tolerated")
	} else {
		logger.Testf("✓ Indexed to block %d (+%d blocks) in %s", previous, previous-initial, window)
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

	initial := bscLatestTipOrFatal(t)
	logger.Testf("WS path: initial BSC tip block: %d", initial)

	time.Sleep(30 * time.Second)

	final := bscLatestTipOrFatal(t)
	if final <= initial {
		logger.Test("Warning: no new blocks indexed over the WS window (may be rate limiting) - tolerated")
		return
	}
	logger.Testf("✓ WS notification path indexed to block %d (+%d blocks)", final, final-initial)
}

// bscLatestTipOrFatal returns the newest stored block number or fails the
// test.
func bscLatestTipOrFatal(t *testing.T) int64 {
	t.Helper()
	tip, found, err := bscLatestNumber(bscGraphqlURL, bscNames.Block, constants.NumberFieldName)
	requireNoError(t, err)
	if !found {
		t.Fatalf("No block documents in %s despite a warmed-up indexer", bscNames.Block)
	}
	return tip
}
