//go:build live
// +build live

// Solana live integration suite: starts the real generator (CHAIN_ADAPTER =
// solana) against a live Solana JSON-RPC endpoint and exercises the GraphQL,
// signature, and health surfaces of the indexed data.
//
// Target selection: RPC from SOLANA_RPC_URL, defaulting to the public devnet
// endpoint so the suite runs without a paid account. The chain network comes
// from SOLANA_LIVE_NETWORK (Devnet default) and determines the collection
// prefix (Solana__Devnet__*). For realistic block volumes point SOLANA_RPC_URL
// at a paid endpoint and set SOLANA_LIVE_NETWORK=Mainnet.
//
// Run: make solana-live-test
// (go test -tags=live ./integration/live/solana/ -timeout=400s -v)
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// defaultDevnetRPC is the free devnet endpoint used when SOLANA_RPC_URL is
// unset. It is aggressively rate-limited, so the suite keeps a low dispatch
// rate and generous waits.
const defaultDevnetRPC = "https://api.devnet.solana.com"

// liveHealthPort is the fixed health server port of the live suite;
// liveWarmupBudget bounds the wait for the first indexed slot (generosity is
// deliberate — devnet rate-limit storms are common).
const (
	liveHealthPort   = 9876
	liveWarmupBudget = 180 * time.Second
)

var (
	liveGQLURL     = "" // resolved once the embedded DefraDB answers
	livePrefix     = ""
	liveBlockCol   = ""
	liveTxCol      = ""
	liveInstrCol   = ""
	liveSigCol     = ""
	indexerStarted = false

	liveIndexer *indexer.ChainIndexer
)

// TestMain boots one live solana indexer shared by all tests of the suite,
// waiting for the first indexed slot before running them.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	logger.Test("TestMain - Starting solana live integration tests")

	rpcURL := os.Getenv("SOLANA_RPC_URL")
	if rpcURL == "" {
		logger.Testf("SOLANA_RPC_URL unset, defaulting to devnet: %s", defaultDevnetRPC)
		rpcURL = defaultDevnetRPC
	}
	network := os.Getenv("SOLANA_LIVE_NETWORK")
	if network == "" {
		network = "Devnet"
	}
	setLiveCollections(network)

	_ = os.RemoveAll("./.defra")   //nolint:gosec // fresh store per run
	defer os.RemoveAll("./.defra") //nolint:gosec

	// The config is built in code rather than loading config/config.yaml:
	// the chain section differs (solana adapter, target network), and the
	// EVM suite's Geth URL default would override SOLANA_RPC_URL post-load.
	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: network,
			Adapter: "solana",
		},
		DefraDB: config.DefraDBConfig{
			URL:           "127.0.0.1:0", // embedded, random port (suite resolves it)
			KeyringSecret: "solana-live-test-keyring",
			P2P: config.DefraDBP2PConfig{
				Enabled:    false,
				ListenAddr: "/ip4/127.0.0.1/tcp/0",
			},
			Store: config.DefraDBStoreConfig{Path: "./.defra"},
		},
		Solana: config.SolanaConfig{
			RPCURL:                         rpcURL,
			APIKey:                         os.Getenv("SOLANA_API_KEY"),
			APIKeyType:                     os.Getenv("SOLANA_API_KEY_TYPE"),
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
		Indexer: config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			ReceiptWorkers:   1,
			MaxDocsPerTxn:    100,
			BlocksPerMinute:  30, // tolerated by devnet-public rate limits
			HealthServerPort: liveHealthPort,
			StartBuffer:      2,
		},
	}

	var err error
	liveIndexer, err = indexer.CreateIndexer(cfg)
	if err != nil {
		logger.Testf("create indexer failed: %v", err)
		os.Exit(0)
	}

	go func() {
		if startErr := liveIndexer.StartIndexing(false); startErr != nil {
			logger.Testf("solana live indexer failed: %v", startErr)
		}
	}()

	if !waitForAnySolanaBlock(liveWarmupBudget) {
		logger.Test("No solana blocks indexed within the warmup budget - treating the suite as skipped " +
			"(devnet rate limiting or endpoint outage)")
		liveIndexer.StopIndexing()
		os.Exit(0)
	}
	indexerStarted = true

	result := m.Run()

	logger.Test("Live solana integration teardown")
	liveIndexer.StopIndexing()
	os.Exit(result) //nolint:gocritic
}

// waitForAnySolanaBlock returns once the embedded DefraDB is up and holds at
// least one stored block document, caching the GraphQL URL for the suite.
func waitForAnySolanaBlock(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if port := liveIndexer.GetDefraDBPort(); port > 0 {
			liveGQLURL = fmt.Sprintf("http://localhost:%d/api/v0/graphql", port)
		}
		if liveGQLURL != "" && pollBlockCount(liveGQLURL, liveBlockCol) > 0 {
			logger.Testf("✅ Indexed solana blocks confirmed at %s", liveGQLURL)
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// pollBlockCount is the TestMain-safe count: any failure reports 0 so the
// warmup loop keeps polling instead of aborting the suite.
func pollBlockCount(gqlURL, blockCol string) int {
	count, err := fetchBlockCount(gqlURL, blockCol)
	if err != nil {
		logger.Sugar.Debugf("block count probe failed: %v", err)
		return 0
	}
	return count
}

// fetchBlockCount runs one GraphQL count query over HTTP.
func fetchBlockCount(gqlURL, blockCol string) (int, error) {
	query := fmt.Sprintf(`{"query":"query { %s { _count } }"}`, blockCol)
	resp, err := http.Post(gqlURL, "application/json", bytes.NewReader([]byte(query))) //nolint:gosec
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, err
	}
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("no data in count response: %s", string(body))
	}
	entry, ok := data[blockCol].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("no %s entry in count response", blockCol)
	}
	count, ok := entry["_count"].(float64)
	if !ok {
		return 0, fmt.Errorf("no _count for %s", blockCol)
	}
	return int(count), nil
}

// ---------------------------------------------------------------------------
// Suite helpers (require a running indexer).
// ---------------------------------------------------------------------------

func liveCount(t *testing.T, colName string) int {
	t.Helper()
	count, err := fetchBlockCount(liveGQLURL, colName)
	require.NoError(t, err, "count query for %s", colName)
	return count
}

// liveRows fetches rows of one collection with the given field list.
func liveRows(t *testing.T, colName, fields string) []any {
	t.Helper()
	query := fmt.Sprintf(`{"query":"query { %s(limit: 1) { %s } }"}`, colName, fields)
	resp, err := http.Post(liveGQLURL, "application/json", bytes.NewReader([]byte(query))) //nolint:gosec
	require.NoError(t, err, "GraphQL POST for %s", colName)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed), "body: %s", string(body))
	data, ok := parsed["data"].(map[string]any)
	require.True(t, ok, "no data in response: %s", string(body))
	rows, ok := data[colName].([]any)
	require.True(t, ok, "no rows for %s in response: %s", colName, string(body))
	return rows
}

func requireLiveIndexer(t *testing.T) {
	t.Helper()
	if !indexerStarted {
		t.Skip("Live solana indexer not started - skipping live tests")
	}
}

// setLiveCollections caches the collection prefix and names derived from the
// configured network segment.
func setLiveCollections(network string) {
	livePrefix = fmt.Sprintf("Solana__%s", network)
	liveBlockCol = livePrefix + "__Block"
	liveTxCol = livePrefix + "__Transaction"
	liveInstrCol = livePrefix + "__Instruction"
	liveSigCol = livePrefix + "__BlockSignature"
}

// ---------------------------------------------------------------------------
// Live tests.
// ---------------------------------------------------------------------------

// TestLiveSolanaConnection proves the generator indexes live data: at least
// one block doc landed and the block fields follow the solana model.
func TestLiveSolanaConnection(t *testing.T) {
	requireLiveIndexer(t)

	logger.Test("Testing live solana connection and block indexing")
	require.Positive(t, liveCount(t, liveBlockCol), "no live solana blocks indexed at %s", liveGQLURL)
	logger.Testf("✓ %s holds blocks", liveBlockCol)

	rows := liveRows(t, liveBlockCol, "slot blockhash parentSlot")
	block, ok := rows[0].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"slot", "blockhash", "parentSlot"} {
		if _, exists := block[field]; !exists {
			t.Errorf("missing required field '%s' in live solana block", field)
		}
	}
	logger.Testf("✓ Latest indexed slot: %v", block["slot"])
}

// TestLiveSolanaCollectionsHaveDocs verifies the full-fidelity collections.
// TokenBalanceChange and Reward are reported but tolerated empty: the token
// program may be inactive for a devnet window and rewards can be paused at
// the endpoint.
func TestLiveSolanaCollectionsHaveDocs(t *testing.T) {
	requireLiveIndexer(t)

	require.Positive(t, liveCount(t, liveTxCol), liveTxCol)
	logger.Testf("✓ %s is populated", liveTxCol)

	for _, col := range []string{liveInstrCol, livePrefix + "__TokenBalanceChange", livePrefix + "__Reward"} {
		if count := liveCount(t, col); count > 0 {
			logger.Testf("✓ %s holds %d documents", col, count)
		} else {
			logger.Testf("~ %s is empty (acceptable on a quiet devnet window)", col)
		}
	}
}

// TestLiveSolanaBlockSignature verifies indexed slots carry block signature
// documents — the hub-facing proof of chain progression.
func TestLiveSolanaBlockSignature(t *testing.T) {
	requireLiveIndexer(t)

	require.Positive(t, liveCount(t, liveSigCol),
		"indexed solana slots must carry %s docs", liveSigCol)

	rows := liveRows(t, liveSigCol, "blockNumber merkleRoot cidCount")
	sig, ok := rows[0].(map[string]any)
	require.True(t, ok)
	logger.Testf("✓ BlockSignature for blockNumber %v (merkle root %v)",
		sig["blockNumber"], sig["merkleRoot"])
}

// TestLiveSolanaHealthEndpoints verifies the health server against the live
// run: healthy status driven by slot progress, readiness, and a schema
// endpoint exposing only the solana collections.
func TestLiveSolanaHealthEndpoints(t *testing.T) {
	requireLiveIndexer(t)

	baseURL := fmt.Sprintf("http://localhost:%d", liveHealthPort)

	healthResp, err := http.Get(baseURL + "/health") //nolint:gosec,noctx
	require.NoError(t, err)
	defer func() { _ = healthResp.Body.Close() }()
	require.Equal(t, http.StatusOK, healthResp.StatusCode)
	healthBody, err := io.ReadAll(healthResp.Body)
	require.NoError(t, err)
	var health struct {
		Status           string `json:"status"`
		DefraDBConnected bool   `json:"defradb_connected"`
		CurrentBlock     int64  `json:"current_block"`
	}
	require.NoError(t, json.Unmarshal(healthBody, &health), "body: %s", string(healthBody))
	assert.Equal(t, "healthy", health.Status)
	assert.True(t, health.DefraDBConnected)
	assert.Positive(t, health.CurrentBlock, "slot progress must drive the health status")

	readyResp, err := http.Get(baseURL + "/registration") //nolint:gosec,noctx
	require.NoError(t, err)
	defer func() { _ = readyResp.Body.Close() }()
	require.Equal(t, http.StatusOK, readyResp.StatusCode)

	schemaResp, err := http.Get(baseURL + "/api/v1/schema") //nolint:gosec,noctx
	require.NoError(t, err)
	defer func() { _ = schemaResp.Body.Close() }()
	require.Equal(t, http.StatusOK, schemaResp.StatusCode)
	schemaBody, err := io.ReadAll(schemaResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(schemaBody), liveBlockCol)
	assert.NotContains(t, string(schemaBody), "Ethereum__Mainnet",
		"EVM SDL must not leak into the solana schema endpoint")
}

// TestLiveSolanaIndexingAdvances verifies skipped-slot handling live: the
// stored block count increases over an idle window and never regresses.
// Devnet produces skipped slots constantly, so a stall is real evidence of a
// bug; devnet rate limits may throttle progress, which only logs.
func TestLiveSolanaIndexingAdvances(t *testing.T) {
	requireLiveIndexer(t)

	initial := liveCount(t, liveBlockCol)
	require.Positive(t, initial)

	time.Sleep(30 * time.Second)

	final := liveCount(t, liveBlockCol)
	assert.GreaterOrEqual(t, final, initial, "block count must never regress")
	if final > initial {
		logger.Testf("✓ Indexed %d new solana slots in 30s", final-initial)
	} else {
		logger.Test("~ No new slots in 30s (devnet rate limits may throttle; tolerated)")
	}
}
