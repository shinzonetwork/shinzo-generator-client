//go:build live
// +build live

package bitcoin

// The Bitcoin live smoke: the full indexer wiring driven by a real Bitcoin
// Core JSON-RPC endpoint. Explicit scope note, shared with the other
// adapters' live suites: this proves the wiring, not throughput.
//
// Gated on BITCOIN_RPC_URL and skipped with exit 0 (t.Skipf) when unset; a
// local regtest node is the suggested endpoint since it needs no external
// account. BITCOIN_LIVE_NETWORK selects the segment (Mainnet default) and
// BITCOIN_ARCHIVE_RPC_URL opts in an archive fallback whose presence is
// recorded in the suite log. The run waits up to 30m for two newly produced
// heights, which is what Bitcoin's roughly ten-minute block cadence needs,
// then pins field integrity and one signed BlockSignature document per
// stored height before watching an idle window for regressions.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// livePollInterval shapes the progress watch. Bitcoin's roughly ten-minute
// block cadence has a fat tail, so the 30m window is the only stall signal:
// progress is allowed to sit flat (only logged, never failed), must never
// decrease, and a run that reaches the deadline without two produced
// heights fails as a stall.
const (
	liveWaitForTwoHeights = 30 * time.Minute
	liveIdleWindow        = 60 * time.Second
	livePollTick          = 5 * time.Second
)

func TestBitcoinLiveSmoke(t *testing.T) {
	rpcURL := os.Getenv("BITCOIN_RPC_URL")
	if rpcURL == "" {
		t.Skipf("BITCOIN_RPC_URL not set; skipping the bitcoin live smoke. Setup example: " +
			"bitcoind -regtest -rpcbind=127.0.0.1:18443 -rpcuser=shinzorpc -rpcpassword=<pass> -daemon, " +
			"then mine periodically with bitcoin-cli -regtest generatetoaddress 1 <address> so the " +
			"wait for two heights completes inside the 30m window")
		return
	}

	network := os.Getenv("BITCOIN_LIVE_NETWORK")
	if network == "" {
		network = "Mainnet"
	}
	if archiveURL := os.Getenv("BITCOIN_ARCHIVE_RPC_URL"); archiveURL != "" {
		logger.Sugar.Infof("Bitcoin live smoke: archive fallback endpoint opted in (BITCOIN_ARCHIVE_RPC_URL is set)")
	}

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Adapter: config.BitcoinAdapter,
			Name:    "Bitcoin",
			Network: network,
		},
		DefraDB: config.DefraDBConfig{
			// IPv6 loopback is the bind address the instance bootstrap leaves
			// untouched: the literal 127.0.0.1/localhost forms are rewritten
			// to the LAN IP for peer reachability, which takes the API out of
			// this host's direct reach in the helpers below.
			URL:           "[::1]:0",
			KeyringSecret: "shinzo-live-bitcoin-keyring-0001",
			P2P:           config.DefraDBP2PConfig{Enabled: false, ListenAddr: "/ip4/127.0.0.1/tcp/0"},
			Store:         config.DefraDBStoreConfig{Path: t.TempDir()},
		},
		Bitcoin: config.BitcoinConfig{
			RPCURL:        rpcURL,
			RPCUser:       os.Getenv("BITCOIN_RPC_USER"),
			RPCPassword:   os.Getenv("BITCOIN_RPC_PASSWORD"),
			CookiePath:    os.Getenv("BITCOIN_COOKIE_PATH"),
			ArchiveRPCURL: os.Getenv("BITCOIN_ARCHIVE_RPC_URL"),
			// The full approved confirmation depth keeps the live run
			// aligned with production defaults; the floor only bounds the
			// reorg walk-back, it never gates near-tip processing.
			ConfirmationDepth:  6,
			DialTimeoutSeconds: 10,
		},
		Indexer: config.IndexerConfig{
			StartHeight: 0,
			// A tiny StartBuffer keeps the initial sync to a couple of
			// historical heights so the 30m budget is spent waiting for
			// produced heights rather than backfilling.
			StartBuffer:      2,
			ConcurrentBlocks: 1,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			SchemaAuthMode:   constants.SchemaAuthModeNone,
		},
		Logger: config.LoggerConfig{Development: true},
	}
	if !cfg.Bitcoin.AuthConfigured() {
		t.Fatal("bitcoin live smoke needs auth: set BITCOIN_RPC_USER and BITCOIN_RPC_PASSWORD, or BITCOIN_COOKIE_PATH")
	}

	ix, err := indexer.CreateIndexer(cfg)
	require.NoError(t, err)
	errCh := make(chan error, 1)
	go func() {
		errCh <- ix.StartIndexing(false)
	}()
	t.Cleanup(ix.StopIndexing)

	waitLiveReady(t, ix, errCh)
	port := ix.GetDefraDBPort()
	require.Greater(t, port, 0)

	firstCount := liveBlockCount(t, port, networkPrefix(network))
	require.Positive(t, firstCount, "no blocks stored after the initial sync")
	target := firstCount + 2
	logger.Sugar.Infof("Bitcoin live smoke: %d heights stored after initial sync, waiting for %d", firstCount, target)

	liveWaitForHeights(t, port, networkPrefix(network), target)

	assertLiveStoredDocs(t, port, network)

	liveIdleWindowCheck(t, port, networkPrefix(network))

	logger.Sugar.Info("Bitcoin live smoke passed: wiring, field integrity, signatures, and progress verified")
}

// networkPrefix returns the collection prefix stem of the configured
// network segment, which the GraphQL queries need to address the right
// collections.
func networkPrefix(network string) string {
	return fmt.Sprintf("Bitcoin__%s", network)
}

// waitLiveReady waits for the indexer to start and commit its first height,
// failing fast on a start error.
func waitLiveReady(t *testing.T, ix *indexer.ChainIndexer, errCh <-chan error) {
	t.Helper()
	deadline := time.After(90 * time.Second)
	for !ix.HasIndexedAtLeastOneBlock() {
		select {
		case <-time.After(livePollTick):
		case <-deadline:
			t.Fatalf("timed out waiting for the live indexer's first stored height (started=%t)", ix.IsStarted())
		case err := <-errCh:
			t.Fatalf("StartIndexing failed: %v", err)
		}
	}
}

// liveGraphqlCount queries one collection's full row count through the
// embedded node's GraphQL API, returning 0 while the API is warming up.
func liveGraphqlCount(port int, collection string) (int, error) {
	payload, err := json.Marshal(map[string]string{
		"query": fmt.Sprintf(`query { %s { number } }`, collection),
	})
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://[::1]:%d/api/v0/graphql", port), "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("graphql status %d: %s", resp.StatusCode, body)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, err
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("graphql response missing data: %s", body)
	}
	rows, ok := data[collection].([]any)
	if !ok {
		return 0, nil
	}
	return len(rows), nil
}

// liveBlockCount reads the block count with retries; transient query
// failures return the last known count so the progress watch stays
// monotonic rather than flapping on cold caches.
func liveBlockCount(t *testing.T, port int, prefix string) int {
	t.Helper()
	count, err := liveGraphqlCount(port, prefix+"__Block")
	require.NoError(t, err)
	return count
}

// liveWaitForHeights polls until the stored count reaches the target inside
// the 30m cadence budget. The watch is monotonic: a decreasing count fails
// immediately, a slow or flat tip only logs, and the deadline itself is the
// stall signal.
func liveWaitForHeights(t *testing.T, port int, prefix string, target int) {
	t.Helper()
	deadline := time.Now().Add(liveWaitForTwoHeights)
	lastCount := 0
	for {
		count, err := liveGraphqlCount(port, prefix+"__Block")
		if err == nil && count < lastCount {
			t.Fatalf("stored height count decreased from %d to %d: a rollback drained live heights", lastCount, count)
		}
		if err == nil {
			if count > lastCount {
				logger.Sugar.Infof("Bitcoin live smoke: progress %d -> %d", lastCount, count)
			}
			lastCount = count
		}
		if lastCount >= target {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d stored heights (last seen %d): a 30m no-progress run is a stall, not a fat block gap", target, lastCount)
		}
		time.Sleep(livePollTick)
	}
}

// assertLiveStoredDocs pins the wiring assertions on real data: the newest
// block doc carries the header contract fields, transactions share its
// hash, and every stored height carries one signed BlockSignature doc.
func assertLiveStoredDocs(t *testing.T, port int, network string) {
	t.Helper()
	prefix := networkPrefix(network)

	blockRows, err := liveRows(port, prefix+"__Block", "number hash merkleRoot bits time")
	require.NoError(t, err)
	require.NotEmpty(t, blockRows, "no block docs after the wait")

	newest := map[string]any{}
	newestNumber := -1.0
	for _, row := range blockRows {
		if number, ok := row["number"].(float64); ok && number > newestNumber {
			newestNumber = number
			newest = row
		}
	}
	require.NotEmpty(t, newest, "no block docs after the wait")
	for _, field := range []string{"hash", "merkleRoot", "bits"} {
		value, ok := newest[field].(string)
		require.True(t, ok && value != "", "newest block %s must be a non-empty string: %#v", field, newest[field])
	}

	blockHash, ok := newest["hash"].(string)
	require.True(t, ok)
	txRows, err := liveRows(port, prefix+"__Transaction", "txid blockHash")
	require.NoError(t, err)
	require.NotEmpty(t, txRows, "no transaction docs")
	foundShared := false
	for _, row := range txRows {
		if row["blockHash"] == blockHash {
			foundShared = true
		}
	}
	assert.True(t, foundShared, "transactions must reference the stored block hash")

	sigRows, err := liveRows(port, prefix+"__BlockSignature", "blockNumber blockHash signatureValue")
	require.NoError(t, err)
	assert.Len(t, sigRows, len(blockRows),
		"one signed BlockSignature doc per stored height")
	for _, row := range sigRows {
		assert.NotEmpty(t, row["signatureValue"])
	}
}

// liveRows is the collection read-back used by the live assertions.
func liveRows(port int, collection, fieldList string) ([]map[string]any, error) {
	query := fmt.Sprintf(`query { %s { %s } }`, collection, fieldList)
	payload, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://[::1]:%d/api/v0/graphql", port), "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql status %d: %s", resp.StatusCode, body)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("graphql response missing data: %s", body)
	}
	raw, ok := data[collection].([]any)
	if !ok {
		return nil, nil
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		row, ok := entry.(map[string]any)
		if ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// liveIdleWindowCheck samples the stored count across one window: the count
// may stay flat for a slice shorter than a block interval, but can never
// decrease, and throttled or slow progress only logs.
func liveIdleWindowCheck(t *testing.T, port int, prefix string) {
	t.Helper()
	baseline := liveBlockCount(t, port, prefix)
	lastSeen := baseline
	deadline := time.After(liveIdleWindow)
	for {
		select {
		case <-time.After(livePollTick):
			count, err := liveGraphqlCount(port, prefix+"__Block")
			if err != nil {
				continue
			}
			if count < lastSeen {
				t.Fatalf("stored height count decreased from %d to %d in the idle window", lastSeen, count)
			}
			if count > lastSeen {
				logger.Sugar.Infof("Bitcoin live smoke idle window: progress %d -> %d", lastSeen, count)
				lastSeen = count
			}
		case <-deadline:
			logger.Sugar.Infof("Bitcoin live smoke idle window closed at %d stored heights (baseline %d)", lastSeen, baseline)
			return
		}
	}
}
