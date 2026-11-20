//go:build integration
// +build integration

package bitcoin

// Shared plumbing for the Bitcoin integration suites: the package TestMain,
// the StartIndexing config builder pointed at the scripted fake, polling
// helpers for indexer progress, and collection read-back helpers over the
// embedded node's GraphQL API.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestMain(m *testing.M) {
	// Keep test binaries from writing a logs directory into the source tree,
	// and pin a noop meter provider so gauge registration stays silent.
	if err := os.Setenv("NO_LOG_FILES", "1"); err != nil {
		panic(err)
	}
	logger.InitConsoleOnly(true)
	otel.SetMeterProvider(noop.NewMeterProvider())
	os.Exit(m.Run())
}

var defraP2PDisabled = config.DefraDBP2PConfig{Enabled: false, ListenAddr: "/ip4/127.0.0.1/tcp/0"}

// defraTestHost is the loopback host the embedded node binds for the tests.
// The instance bootstrap rewrites the literal loopback addresses (127.0.0.1,
// localhost) into the machine's LAN IP for peer reachability, which would
// bind the API outside what a same-host HTTP client can reach, so the tests
// opt out with IPv6 loopback, a form the rewrite leaves untouched and every
// platform binds without extra interface setup (the 127/8 aliases beyond
// .1 are not preconfigured on darwin).
const defraTestHost = "[::1]"

// btcIndexerConfig builds the full StartIndexing config for the bitcoin
// adapter against the scripted fake: an embedded node on a random port, a
// store under the given directory, and signing enabled through the node
// keyring.
func btcIndexerConfig(rpcURL, storeDir string, idx config.IndexerConfig) *config.Config {
	return btcIndexerConfigWithBitcoinConfig(btcRPCConfig(rpcURL), storeDir, idx)
}

// btcRPCConfig fills the Bitcoin Core section the honest way: basic auth
// every request must carry, the six-confirmation finality floor, and the
// dial timeout the connect probe honors.
func btcRPCConfig(rpcURL string) config.BitcoinConfig {
	return config.BitcoinConfig{
		RPCURL:             rpcURL,
		RPCUser:            "testrpcuser",
		RPCPassword:        "testrpcpass",
		ConfirmationDepth:  6,
		DialTimeoutSeconds: 10,
	}
}

// btcIndexerConfigWithBitcoinConfig lets callers swap the bitcoin section
// (api-key coverage in the health suite) while keeping the generic shape.
func btcIndexerConfigWithBitcoinConfig(bc config.BitcoinConfig, storeDir string, idx config.IndexerConfig) *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Adapter: config.BitcoinAdapter,
			Name:    "Bitcoin",
			Network: "Mainnet",
		},
		DefraDB: config.DefraDBConfig{
			URL:           defraTestHost + ":0",
			KeyringSecret: "test-secret-for-keyring-12345678",
			P2P:           defraP2PDisabled,
			Store:         config.DefraDBStoreConfig{Path: storeDir},
		},
		Bitcoin: bc,
		Indexer: idx,
		Logger:  config.LoggerConfig{Development: true},
	}
}

// freeHealthPort returns a port that is free at the time of the call, for
// tests that need the health server enabled (hard-coded ports collide with
// -count=N reruns).
func freeHealthPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

// startIndexingBackground launches StartIndexing(false) in the background
// and returns the channel its result is sent to.
func startIndexingBackground(t *testing.T, ix *indexer.ChainIndexer) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- ix.StartIndexing(false)
	}()
	return errCh
}

// waitForBlocks polls the indexer's committed-height marker until it
// reaches the wanted height. A StartIndexing return (success or failure)
// before that point fails the test: a settled start means the run stopped.
func waitForBlocks(t *testing.T, ix *indexer.ChainIndexer, errCh <-chan error, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if current := ix.GetCurrentBlock(); current >= want {
			return
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for block %d (currently %d)", want, ix.GetCurrentBlock())
		case err := <-errCh:
			t.Fatalf("StartIndexing returned before block %d: %v", want, err)
		}
	}
}

// pollFor waits until cond holds or the deadline lapses.
func pollFor(t *testing.T, timeout time.Duration, cond func() bool, message string, args ...any) {
	t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf(message, args...)
		}
	}
}

// collectionRows reads one collection back through the GraphQL API and
// returns its rows as ordered maps.
func collectionRows(t *testing.T, port int, collection, filterClause, fieldList string) []map[string]any {
	t.Helper()
	query := fmt.Sprintf(`query { %s%s { %s } }`, collection, filterClause, fieldList)
	payload, err := json.Marshal(map[string]string{"query": query})
	require.NoError(t, err)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://%s:%d/api/v0/graphql", defraTestHost, port), "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "graphql query failed: %s", body)

	var envelope map[string]any
	require.NoError(t, json.Unmarshal(body, &envelope), "graphql body: %s", body)
	data, ok := envelope["data"].(map[string]any)
	require.True(t, ok, "graphql response has no data object: %s", body)

	raw, ok := data[collection]
	require.True(t, ok, "graphql response missing %s: %s", collection, body)

	var rows []map[string]any
	switch typed := raw.(type) {
	case []any:
		for _, entry := range typed {
			row, rowOK := entry.(map[string]any)
			require.True(t, rowOK, "row is not an object: %#v", entry)
			rows = append(rows, row)
		}
	case []map[string]any:
		rows = typed
	default:
		t.Fatalf("unexpected graphql row shape for %s: %T", collection, raw)
	}
	return rows
}

// rowNumber reads a numeric field out of one GraphQL row (JSON numbers
// decode as float64).
func rowNumber(t *testing.T, row map[string]any, field string) int64 {
	t.Helper()
	value, ok := row[field].(float64)
	require.True(t, ok, "field %s is not a number: %#v", field, row[field])
	return int64(value)
}
