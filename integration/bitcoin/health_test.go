//go:build integration
// +build integration

package bitcoin

// The health surface test: a full indexer StartIndexing run against a
// PRIVATE copy of the scripted chain fake (own store, own DefraDB port, own
// health port), with the health server enabled and the schema endpoint in
// no-auth mode. It polls the live endpoints over TCP until the run is
// healthy, pins the registration payload's source-chain wiring, and audits
// the authentication coverage on the recorded wire.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
)

func TestBitcoinHealthEndpoints(t *testing.T) {
	chain := newScriptedChain(104)
	serverFake := newCoreRPCServer(chain.respond)
	t.Cleanup(serverFake.Close)

	healthPort := freeHealthPort(t)
	cfg := btcIndexerConfig(serverFake.URL, t.TempDir(), config.IndexerConfig{
		StartHeight:      100,
		ConcurrentBlocks: 1,
		MaxDocsPerTxn:    100,
		HealthServerPort: healthPort,
		StartBuffer:      100,
		SchemaAuthMode:   constants.SchemaAuthModeNone,
	})
	// The registration probe rides the peer-info read: a P2P-disabled node's
	// PeerInfo call can error and flip /registration into its 503 path, so
	// this node runs with P2P enabled, the shape production deployments use.
	cfg.DefraDB.P2P = config.DefraDBP2PConfig{Enabled: true, ListenAddr: "/ip4/127.0.0.1/tcp/0"}
	// The api-key rides beside the basic auth on every request the client
	// sends; the wire audit below asserts full coverage of both headers.
	cfg.Bitcoin.APIKey = "test-btc-api-key"

	ix, err := indexer.CreateIndexer(cfg)
	require.NoError(t, err)
	errCh := startIndexingBackground(t, ix)
	t.Cleanup(ix.StopIndexing)

	waitForBlocks(t, ix, errCh, 104, 90*time.Second)
	base := fmt.Sprintf("http://127.0.0.1:%d", healthPort)

	t.Run("health becomes healthy with the committed height", func(t *testing.T) {
		pollFor(t, 60*time.Second, func() bool {
			health := getJSONBody(t, base+"/health")
			return health["status"] == "healthy" && healthCurrentBlock(health) >= 100
		}, "expected /health to become healthy with a committed height")

		health := getJSONBody(t, base+"/health")
		assert.Equal(t, "healthy", health["status"])
		assert.Equal(t, true, health["defradb_connected"])
		assert.GreaterOrEqual(t, healthCurrentBlock(health), int64(100))
	})

	t.Run("registration reports the bitcoin source chain", func(t *testing.T) {
		payload := getJSONBody(t, base+"/registration")
		require.NotNil(t, payload["registration"], "registration payload missing: %#v", payload)
		registration := payload["registration"].(map[string]any)
		assert.Equal(t, true, registration["enabled"])
		assert.Equal(t, "bitcoin", registration["source_chain"])
		// source_chain_id carries 0 for bitcoin and omitempty keeps it off
		// the wire: the family identity rides the name alone.
		_, present := registration["source_chain_id"]
		assert.False(t, present)
	})

	t.Run("metrics reach the chain head", func(t *testing.T) {
		pollFor(t, 60*time.Second, func() bool {
			return metricsCurrentBlock(t, base) == 104
		}, "expected /metrics current_block to reach the scripted head")
	})

	t.Run("schema endpoint serves the bitcoin sdl", func(t *testing.T) {
		payload := getJSONBody(t, base+"/api/v1/schema")
		assert.Equal(t, "Bitcoin__Mainnet", payload["network"])
		sdl, ok := payload["schema"].(string)
		require.True(t, ok, "schema must be a string: %#v", payload["schema"])
		assert.Contains(t, sdl, "Bitcoin__Mainnet__Block")
		assert.Contains(t, sdl, "Bitcoin__Mainnet__BlockSignature")
		assert.NotContains(t, sdl, "Ethereum__Mainnet", "the evm schema must not leak")
	})

	t.Run("schema collections list the bitcoin stems", func(t *testing.T) {
		payload := getJSONBody(t, base+"/api/v1/schema/collections")
		assert.Equal(t, "Bitcoin__Mainnet", payload["network"])
		raw, ok := payload["collections"].([]any)
		require.True(t, ok, "collections must be an array: %#v", payload["collections"])
		var entries []schema.CollectionEntry
		data, err := json.Marshal(raw)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &entries))
		require.Len(t, entries, 6)
		stems := map[string]bool{}
		for _, entry := range entries {
			stems[entry.Name] = true
			assert.True(t, strings.HasPrefix(entry.TypeName, "Bitcoin__Mainnet__"),
				"collection %s must carry the bitcoin prefix", entry.Name)
		}
		for _, stem := range []string{"block", "blockSignature", "snapshotSignature", "transaction", "txInput", "txOutput"} {
			assert.True(t, stems[stem], "missing collection stem %s", stem)
		}
	})

	t.Run("wire audit covers authentication on every request", func(t *testing.T) {
		calls := serverFake.recordedCalls()
		require.NotEmpty(t, calls)
		// The api-key header adds a second layer on top of Bitcoin Core's
		// basic auth: hosted providers receive both on every request, so the
		// audit counts any request missing either header. The count the fake
		// has to report is zero.
		basic := base64.StdEncoding.EncodeToString([]byte("testrpcuser:testrpcpass"))
		missing := 0
		for _, call := range calls {
			if call.Header.Get("Authorization") != "Basic "+basic {
				missing++
			}
			if call.Header.Get("x-api-key") != "test-btc-api-key" {
				missing++
			}
		}
		assert.Equal(t, 0, missing,
			"every wire request must carry both the basic-auth and the api-key headers")
	})
}

// getJSONBody GETs one URL and decodes the JSON body, failing on any
// non-200 status.
func getJSONBody(t *testing.T, url string) map[string]any {
	t.Helper()
	body := readFull(t, url)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload), "body: %s", body)
	return payload
}

// healthCurrentBlock pulls the committed height out of a decoded health or
// metrics payload.
func healthCurrentBlock(health map[string]any) int64 {
	value, ok := health["current_block"].(float64)
	if !ok {
		return 0
	}
	return int64(value)
}

// metricsCurrentBlock reads the /metrics JSON and returns current_block.
func metricsCurrentBlock(t *testing.T, base string) int64 {
	t.Helper()
	payload := getJSONBody(t, base+"/metrics")
	var metrics server.MetricsResponse
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &metrics))
	return metrics.CurrentBlock
}

// readFull performs the GET with the JSON accept header and returns the
// response body, failing on any non-200 status.
func readFull(t *testing.T, url string) []byte {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s failed: %s", url, body)
	return body
}
