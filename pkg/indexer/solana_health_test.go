package indexer

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	_ "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana" // registers the solana chain factories
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
)

// StartIndexing — solana adapter wiring: with CHAIN_ADAPTER=solana the running
// generator must serve a healthy /health status fed by slot progress, a ready
// /registration probe, and a schema endpoint exposing the solana collections.
// The RPC surface is faked with the canned confirmed-block fixture from the
// solana package, so this runs as an ordinary unit test (no build tag).

var solanaFixturePath = filepath.Join("..", "chains", "solana", "testdata", "getBlock_confirmed_full.json")

// solanaHealthView mirrors the fields of server.HealthResponse the test needs.
type solanaHealthView struct {
	Status           string `json:"status"`
	DefraDBConnected bool   `json:"defradb_connected"`
	CurrentBlock     int64  `json:"current_block"`
}

func solanaFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

// waitForHealthyHealth polls GET /health (JSON accept header) until the
// indexer reports healthy with a positive current slot, or fails the test.
func waitForSolanaHealth(t *testing.T, baseURL string) solanaHealthView {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var last solanaHealthView
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
		if err == nil {
			req.Header.Set("Accept", "application/json")
			resp, reqErr := http.DefaultClient.Do(req)
			if reqErr == nil {
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
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
	t.Fatalf("timed out waiting for the solana indexer to become healthy; last view: %+v", last)
	return solanaHealthView{}
}

// TestStartIndexing_SolanaHealthEndpoints starts the full indexer against a
// fake solana RPC and verifies the health server's endpoints end to end:
// /health reports healthy with slot progress, /registration is ready,
// /metrics counts processed slots, and the schema endpoints expose the
// solana collection SDLs.
func TestStartIndexing_SolanaHealthEndpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	logger.InitConsoleOnly(true)

	fixtureRaw, err := os.ReadFile(solanaFixturePath)
	require.NoError(t, err)

	// Tip well above the start height: resolveStartHeight indexes the last
	// start_buffer+1 slots, each served from the same fixture.
	const solanaTip = uint64(500_000_000)

	rpcServer := newMockRPCServer(func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "getSlot":
			return json.RawMessage(fmt.Sprintf(`%d`, solanaTip)), nil
		case "getBlock":
			return json.RawMessage(fixtureRaw), nil
		default:
			return "0x0", nil
		}
	})
	defer rpcServer.Close()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: "Mainnet",
			Adapter: "solana",
		},
		DefraDB: config.DefraDBConfig{
			URL:           testDefraRandomURL,
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           testDefraP2PDisabled,
			Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
		},
		Solana: config.SolanaConfig{
			RPCURL:     rpcServer.URL,
			Commitment: "confirmed",
		},
		Indexer: config.IndexerConfig{
			ConcurrentBlocks: 1,
			ReceiptWorkers:   1,
			MaxDocsPerTxn:    100,
			SchemaAuthMode:   "none",
			HealthServerPort: solanaFreePort(t),
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	indexerInstance, err := CreateIndexer(cfg)
	require.NoError(t, err)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			indexerInstance.shouldIndex = false
			indexerInstance.StopIndexing()
		}
	})

	go func() { _ = indexerInstance.StartIndexing(false) }()

	baseURL := fmt.Sprintf("http://localhost:%d", cfg.Indexer.HealthServerPort)
	health := waitForSolanaHealth(t, baseURL)
	assert.Equal(t, "healthy", health.Status)
	assert.True(t, health.DefraDBConnected)
	assert.Positive(t, health.CurrentBlock, "slot progress must drive the health status")

	// Readiness probe: register endpoint answers ready once blocks flow.
	resp, err := http.Get(baseURL + "/registration")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Metrics probe counts at least the processed slots.
	metricsResp, err := http.Get(baseURL + "/metrics")
	require.NoError(t, err)
	defer func() { _ = metricsResp.Body.Close() }()
	require.Equal(t, http.StatusOK, metricsResp.StatusCode)
	metricsBody, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)
	var metrics struct {
		BlocksProcessed int64 `json:"blocks_processed"`
		CurrentBlock    int64 `json:"current_block"`
	}
	require.NoError(t, json.Unmarshal(metricsBody, &metrics), "body: %s", string(metricsBody))
	assert.Positive(t, metrics.BlocksProcessed,
		"metrics must reflect the processed slots; body: %s", string(metricsBody))

	// The schema endpoint serves the solana SDL and collection list — the
	// wiring under test is initHealthServer feeding the adapter's schema.
	schemaResp, err := http.Get(baseURL + "/api/v1/schema")
	require.NoError(t, err)
	defer func() { _ = schemaResp.Body.Close() }()
	require.Equal(t, http.StatusOK, schemaResp.StatusCode)
	schemaBody, err := io.ReadAll(schemaResp.Body)
	require.NoError(t, err)
	schemaSDL := string(schemaBody)
	assert.Contains(t, schemaSDL, "Solana__Mainnet__Block")
	assert.Contains(t, schemaSDL, "Solana__Mainnet__Instruction")
	assert.NotContains(t, schemaSDL, "Ethereum__Mainnet", "EVM SDL must not leak into the schema endpoint")

	colsResp, err := http.Get(baseURL + "/api/v1/schema/collections")
	require.NoError(t, err)
	defer func() { _ = colsResp.Body.Close() }()
	require.Equal(t, http.StatusOK, colsResp.StatusCode)
	colsBody, err := io.ReadAll(colsResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(colsBody), "Solana__Mainnet")
	assert.NotContains(t, string(colsBody), "Ethereum__Mainnet")
}
