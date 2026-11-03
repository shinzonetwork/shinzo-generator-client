//go:build integration
// +build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
)

// Health endpoints phase: a dedicated full indexer (own scripted chain, own
// embedded DefraDB node, own store, own health port) must serve a healthy
// /health status fed by slot progress, a ready /registration probe, a
// /metrics view of slot progress, and schema endpoints exposing only the
// solana collections.
//
// This coverage moved here from pkg/indexer (2026-10-31): a real
// StartIndexing run against a real DefraDB behind a live HTTP server is an
// integration test in everything but name, and the indexer package's test
// binary should keep its dependency surface chain-agnostic. The chain fake
// is a private instance on purpose: the suite's shared chain records every
// RPC call, and the restart phase tolerates no stray getBlock below its
// resume height.

// TestSolanaHealthEndpoints starts a full solana-configured indexer against
// a private copy of the scripted chain and asserts on the health server.
func TestSolanaHealthEndpoints(t *testing.T) {
	storeDir, err := os.MkdirTemp("", "solana-health-*")
	if err != nil {
		t.Fatalf("cannot create health store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeDir) })

	// One reserved port for the embedded DefraDB HTTP API and one for the
	// indexer's health server, both bound only after the listeners close.
	defraPort := solanaReservePort(t)
	healthPort := solanaReservePort(t)

	chain := newSolanaMockChain()
	server := chain.startServer()
	defer server.Close()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: "Mainnet",
			Adapter: "solana",
		},
		DefraDB: config.DefraDBConfig{
			URL:           fmt.Sprintf("%s:%d", solanaDefraIP, defraPort),
			Embedded:      true,
			KeyringSecret: "solana-health-test-keyring",
			P2P: config.DefraDBP2PConfig{
				Enabled:    false,
				ListenAddr: fmt.Sprintf("/ip4/%s/tcp/0", solanaDefraIP),
			},
			Store: config.DefraDBStoreConfig{
				Path: filepath.Join(storeDir, "store"),
			},
		},
		Solana: config.SolanaConfig{
			NodeURL:                        server.URL,
			APIKey:                         solanaTestAPIKey,
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
		Indexer: config.IndexerConfig{
			StartHeight:      int(solanaStartSlot),
			ConcurrentBlocks: 1,
			BlocksPerMinute:  0,
			MaxDocsPerTxn:    100,
			SchemaAuthMode:   "none",
			HealthServerPort: healthPort,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	idx, err := indexer.CreateIndexer(cfg)
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	t.Cleanup(func() { idx.StopIndexing() })

	run := &solanaIndexerRun{
		indexer:    idx,
		startErrCh: make(chan error, 1),
	}
	go func() {
		run.startErrCh <- idx.StartIndexing(false)
	}()

	baseURL := fmt.Sprintf("http://localhost:%d", healthPort)

	// /health must go healthy under slot progress from the embedded node.
	health := solanaWaitForHealth(t, baseURL, run)
	if health.Status != "healthy" {
		t.Errorf("status mismatch: got %s want healthy, body: %+v", health.Status, health)
	}
	if !health.DefraDBConnected {
		t.Error("/health must report the embedded DefraDB connection")
	}
	if health.CurrentBlock <= 0 {
		t.Errorf("slot progress must drive the health status, got current_block %d", health.CurrentBlock)
	}

	// Readiness probe: the registration endpoint answers 200 once blocks
	// flow.
	resp, err := http.Get(baseURL + "/registration") //nolint:gosec,noctx
	if err != nil {
		t.Fatalf("GET /registration: %v", err)
	}
	body, _ := ioAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /registration status: got %d, body: %s", resp.StatusCode, string(body))
	}

	// Metrics: poll until current_block reaches the chain head so the read
	// cannot land mid-run; the indexer skips nothing here that would cap it
	// below the produced range (the skipped slot advances without a store).
	metrics := solanaWaitForMetrics(t, baseURL, solanaHeadInitial, run)
	if metrics.BlocksProcessed <= 0 {
		t.Errorf("metrics must reflect the processed slots, got blocks_processed %d", metrics.BlocksProcessed)
	}
	if metrics.CurrentBlock < int64(solanaHeadInitial) {
		t.Errorf("current_block must stand at the chain head %d, got %d",
			solanaHeadInitial, metrics.CurrentBlock)
	}

	// The API key header must ride every request the run made to the mock.
	if missing := chain.missingAuthCount(); missing != 0 {
		t.Errorf("%d RPC requests reached the mock without the configured %s header",
			missing, solanaDefaultAuthHdr)
	}

	// The schema endpoint serves the solana SDL and collection list: the
	// wiring under test is initHealthServer feeding the adapter's schema.
	schemaResp, err := http.Get(baseURL + "/api/v1/schema") //nolint:gosec,noctx
	if err != nil {
		t.Fatalf("GET /api/v1/schema: %v", err)
	}
	schemaBody, err := ioAll(schemaResp.Body)
	if err != nil {
		t.Fatalf("read schema body: %v", err)
	}
	if schemaResp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/schema status: got %d", schemaResp.StatusCode)
	}
	schemaSDL := string(schemaBody)
	if !containsAll(schemaSDL, "Solana__Mainnet__Block", "Solana__Mainnet__Instruction") {
		t.Errorf("schema SDL must name the solana collections, got: %s", truncateBody(schemaSDL))
	}
	if containsAll(schemaSDL, "Ethereum__Mainnet") {
		t.Errorf("EVM SDL must not leak into the schema endpoint, got: %s", truncateBody(schemaSDL))
	}

	colsResp, err := http.Get(baseURL + "/api/v1/schema/collections") //nolint:gosec,noctx
	if err != nil {
		t.Fatalf("GET /api/v1/schema/collections: %v", err)
	}
	colsBody, err := ioAll(colsResp.Body)
	if err != nil {
		t.Fatalf("read collections body: %v", err)
	}
	if colsResp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/schema/collections status: got %d", colsResp.StatusCode)
	}
	colsText := string(colsBody)
	if !containsAll(colsText, "Solana__Mainnet") {
		t.Errorf("collections list must contain the solana prefix, got: %s", truncateBody(colsText))
	}
	if containsAll(colsText, "Ethereum__Mainnet") {
		t.Errorf("collections list must not leak the evm prefix, got: %s", truncateBody(colsText))
	}
}

// solanaReservePort grabs a free TCP port the test hands to a config that
// binds it later.
func solanaReservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("cannot release the reserved port: %v", err)
	}
	return port
}

// solanaHealthView mirrors the /health fields the test needs.
type solanaHealthView struct {
	Status           string `json:"status"`
	DefraDBConnected bool   `json:"defradb_connected"`
	CurrentBlock     int64  `json:"current_block"`
}

// solanaWaitForHealth polls GET /health (JSON accept header) until the
// indexer reports healthy with a positive current slot, or fails the test.
// The indexer's start error short-circuits the wait.
func solanaWaitForHealth(t *testing.T, baseURL string, run *solanaIndexerRun) solanaHealthView {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var last solanaHealthView
	for time.Now().Before(deadline) {
		if err, settled := run.startError(); settled && err != nil {
			t.Fatalf("indexer exited with a start error before /health went healthy: %v", err)
		}
		req, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
		if err == nil {
			req.Header.Set("Accept", "application/json")
			resp, reqErr := http.DefaultClient.Do(req)
			if reqErr == nil {
				body, readErr := ioAll(resp.Body)
				var view solanaHealthView
				if readErr == nil && json.Unmarshal(body, &view) == nil {
					last = view
					if view.Status == "healthy" && view.DefraDBConnected && view.CurrentBlock > 0 {
						return view
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for /health to go healthy; last view: %+v", last)
	return solanaHealthView{}
}

// solanaHealthMetrics mirrors the /metrics fields the test needs.
type solanaHealthMetrics struct {
	BlocksProcessed int64 `json:"blocks_processed"`
	CurrentBlock    int64 `json:"current_block"`
}

// solanaWaitForMetrics polls GET /metrics until current_block reaches the
// target slot, or fails the test with the last observed body. The read must
// never assert a mid-run snapshot: the pipeline advances slot by slot while
// the test is already on the endpoint.
func solanaWaitForMetrics(t *testing.T, baseURL string, target int64, run *solanaIndexerRun) solanaHealthMetrics {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var lastBody []byte
	for time.Now().Before(deadline) {
		if err, settled := run.startError(); settled && err != nil {
			t.Fatalf("indexer exited with a start error before /metrics reached slot %d: %v", target, err)
		}
		resp, err := http.Get(baseURL + "/metrics") //nolint:gosec,noctx
		if err == nil {
			body, readErr := ioAll(resp.Body)
			var view solanaHealthMetrics
			if readErr == nil && json.Unmarshal(body, &view) == nil {
				lastBody = body
				if view.CurrentBlock >= target && view.BlocksProcessed > 0 {
					return view
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for metrics current_block to reach %d; last body: %s", target, string(lastBody))
	return solanaHealthMetrics{}
}

// containsAll reports whether the text carries every needle.
func containsAll(text string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}

// truncateBody caps long bodies for error output.
func truncateBody(text string) string {
	const max = 512
	if len(text) > max {
		return text[:max] + "..."
	}
	return text
}
