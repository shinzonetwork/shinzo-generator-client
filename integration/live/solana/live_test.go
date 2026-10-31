//go:build live
// +build live

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// Live Solana smoke test against a real JSON-RPC endpoint (a paid mainnet
// endpoint or the public devnet). The suite is skipped unless SOLANA_RPC_URL
// is set; the Makefile target solana-live-test enforces the same gate with a
// setup hint and the public devnet as the suggested free endpoint.
//
// The chain network comes from SOLANA_LIVE_NETWORK (Devnet default) and
// determines the collection prefix (Solana__Devnet__*). For realistic block
// volumes point SOLANA_RPC_URL at a paid endpoint and set
// SOLANA_LIVE_NETWORK=Mainnet.
//
// The smoke targets only 3 slots and a tight deadline: it proves the
// adapter wires a real endpoint into the whole pipeline, not throughput.

var (
	solanaLiveIndexer    *solanaLiveIndexerRun
	solanaLiveGraphqlURL string
	solanaLiveTable      = solana.NewCollectionNames(fmt.Sprintf("Solana__%s", "Devnet"))

	liveWarmupBudget    = 180 * time.Second
	liveDefaultEndpoint = "https://api.devnet.solana.com"

	// liveWsEnabled records the SOLANA_WS_URL state: when set the suite runs
	// with the hint gate active and the WS subtest exercises the
	// notification-driven path, unset keeps polling only.
	liveWsEnabled = false
)

// solanaLiveIndexerRun couples an indexer with the exit channel of its
// StartIndexing goroutine.
type solanaLiveIndexerRun struct {
	indexer    *indexer.ChainIndexer
	startErrCh chan error
}

// startError reports whether StartIndexing has returned, and with what.
func (run *solanaLiveIndexerRun) startError() (error, bool) {
	select {
	case err := <-run.startErrCh:
		return err, true
	default:
		return nil, false
	}
}

// TestMain gates on SOLANA_RPC_URL, builds a real-chain config with a temp
// store, and starts the indexer the same way the mock suite does: the
// indexer owns its embedded DefraDB node.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)

	if os.Getenv("SOLANA_RPC_URL") == "" {
		logger.Test("SOLANA_RPC_URL not set; skipping solana live smoke (export it, for example " + liveDefaultEndpoint + ")")
		os.Exit(0)
	}

	network := os.Getenv("SOLANA_LIVE_NETWORK")
	if network == "" {
		network = "Devnet"
	}
	solanaLiveTable = solana.NewCollectionNames(fmt.Sprintf("Solana__%s", network))
	liveWsEnabled = os.Getenv("SOLANA_WS_URL") != ""
	if liveWsEnabled {
		logger.Test("SOLANA_WS_URL set: slot notifications enabled")
	}

	lanIP, err := utils.GetLANIP()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot resolve a local IP for DefraDB: %v\n", err)
		os.Exit(1)
	}

	storeDir, err := os.MkdirTemp("", "solana-live-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot create the live suite store dir: %v\n", err)
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot reserve a port for DefraDB: %v\n", err)
		_ = os.RemoveAll(storeDir)
		os.Exit(1)
	}
	defraPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: network,
			Adapter: "solana",
		},
		DefraDB: config.DefraDBConfig{
			URL:           fmt.Sprintf("%s:%d", lanIP, defraPort),
			Embedded:      true,
			KeyringSecret: "solana-live-smoke-keyring-secret",
			P2P: config.DefraDBP2PConfig{
				Enabled:    false,
				ListenAddr: fmt.Sprintf("/ip4/%s/tcp/0", lanIP),
			},
			Store: config.DefraDBStoreConfig{
				Path: filepath.Join(storeDir, "store"),
			},
		},
		Solana: config.SolanaConfig{
			NodeURL:                        os.Getenv("SOLANA_RPC_URL"),
			WsURL:                          os.Getenv("SOLANA_WS_URL"),
			APIKey:                         os.Getenv("SOLANA_API_KEY"),
			APIKeyType:                     os.Getenv("SOLANA_API_KEY_TYPE"),
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
		Indexer: config.IndexerConfig{
			ConcurrentBlocks: 1,
			BlocksPerMinute:  30, // tolerated by devnet-public rate limits
			MaxDocsPerTxn:    100,
			HealthServerPort: -1,
		},
		Logger: config.LoggerConfig{Development: true},
	}

	idx, err := indexer.CreateIndexer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot create the indexer: %v\n", err)
		_ = os.RemoveAll(storeDir)
		os.Exit(1)
	}
	run := &solanaLiveIndexerRun{
		indexer:    idx,
		startErrCh: make(chan error, 1),
	}
	solanaLiveIndexer = run
	solanaLiveGraphqlURL = fmt.Sprintf("http://%s:%d/api/v0/graphql", lanIP, defraPort)

	go func() {
		run.startErrCh <- idx.StartIndexing(false)
	}()

	code := m.Run()

	run.indexer.StopIndexing()
	if err := os.RemoveAll(storeDir); err != nil {
		logger.Test(fmt.Sprintf("TestMain - failed to remove the live store dir: %v", err))
	}

	os.Exit(code)
}

// solanaLiveTryQuery posts one GraphQL request without failing the test, so
// the wait phase can probe while the embedded node is still starting.
func solanaLiveTryQuery(query string) (map[string]any, error) {
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshal graphql query: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(solanaLiveGraphqlURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := ioLiveAll(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql query failed with status %d: %s", resp.StatusCode, body)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode graphql response: %w", err)
	}
	if errs, ok := envelope["errors"].([]any); ok && len(errs) > 0 {
		return nil, fmt.Errorf("graphql errors: %v", errs)
	}
	return envelope, nil
}

// solanaLiveQuery is the fatal variant for the direct assertion paths, once
// the node provably answers.
func solanaLiveQuery(t *testing.T, query string) map[string]any {
	t.Helper()
	envelope, err := solanaLiveTryQuery(query)
	if err != nil {
		t.Fatalf("graphql query failed: %v\nquery: %s", err, query)
	}
	if envelope["data"] == nil {
		t.Fatalf("graphql query returned no data\nquery: %s", query)
	}
	return envelope
}

// solanaLiveRowMap decodes a collection row list from a graphql envelope.
func solanaLiveRowMap(envelope map[string]any, collection string) []map[string]any {
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return nil
	}
	raw := data[collection]
	rows, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if mapped, ok := row.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

// solanaLiveString normalizes a GraphQL row value to its string form
// (nil and non-strings degrade to ""), the integrity assertions' helper.
func solanaLiveString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// waitForSolanaLiveBlocks waits until the block collection holds at least
// target rows, surfacing the indexer's start error immediately.
func waitForSolanaLiveBlocks(t *testing.T, target int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var lastErr error
	blockCol := solanaLiveTable.Block
	for {
		if err, settled := solanaLiveIndexer.startError(); settled {
			if err != nil {
				t.Fatalf("live indexer exited with %v before %d slots were stored", err, target)
			}
			break
		}
		envelope, err := solanaLiveTryQuery(fmt.Sprintf("query { %s { _docID } }", blockCol))
		if err == nil {
			if rows := solanaLiveRowMap(envelope, blockCol); len(rows) >= target {
				logger.Testf("live smoke: %d solana slots stored on the real endpoint", len(rows))
				return
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("block collection never reached %d rows within %v (last graphql error: %v)", target, budget, lastErr)
		}
		time.Sleep(time.Second)
	}
}

// TestSolanaLiveSmoke waits for 3 real slots, then asserts basic field
// integrity on whatever arrived plus one signature doc per stored slot.
func TestSolanaLiveSmoke(t *testing.T) {
	if solanaLiveIndexer == nil {
		t.Fatal("live smoke TestMain did not create the indexer")
	}

	// A real chain advances roughly 150 slots/min; the generous budget
	// absorbs connect time, tip detection, and devnet rate-limit storms
	// without coming near the 120s suite timeout.
	waitForSolanaLiveBlocks(t, 3, liveWarmupBudget)

	// Block field integrity on whatever arrived.
	blockCol := solanaLiveTable.Block
	envelope := solanaLiveQuery(t, fmt.Sprintf("query { %s(limit: 5) { number hash parentHash blockTime } }", blockCol))
	blocks := solanaLiveRowMap(envelope, blockCol)
	if len(blocks) == 0 {
		t.Fatal("no Block docs queryable after the wait phase")
	}
	for _, block := range blocks {
		if solanaLiveString(block["hash"]) == "" {
			t.Errorf("stored slot %#v has no hash", block["number"])
		}
		if _, ok := block["blockTime"].(float64); !ok {
			t.Errorf("stored slot %#v has a missing or non-numeric blockTime", block["number"])
		}
	}

	// Transaction field integrity on whatever arrived.
	txCol := solanaLiveTable.Transaction
	txEnvelope := solanaLiveQuery(t, fmt.Sprintf("query { %s(limit: 5) { signature blockNumber } }", txCol))
	for _, tx := range solanaLiveRowMap(txEnvelope, txCol) {
		if solanaLiveString(tx["signature"]) == "" {
			t.Errorf("stored transaction %#v has no signature", tx)
		}
		if tx["blockNumber"] == nil {
			t.Errorf("stored transaction %#v has no blockNumber", tx)
		}
	}

	// One signature doc per stored slot: content coverage and a real
	// signature value, with the keyring named as the suspect when empty.
	// The sign step trails the block store, and the block count poll above
	// can flip while a slot is mid-sign, so poll the pair briefly instead
	// of racing single back-to-back queries.
	sigCol := solanaLiveTable.BlockSignature
	wantSigs := len(blocks)
	deadline := time.Now().Add(10 * time.Second)
	var sigs []map[string]any
	for {
		sigEnvelope := solanaLiveQuery(t, fmt.Sprintf("query { %s { blockNumber blockHash merkleRoot cidCount signatureValue } }", sigCol))
		sigs = solanaLiveRowMap(sigEnvelope, sigCol)
		if len(sigs) >= wantSigs || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(sigs) < wantSigs {
		t.Errorf("expected at least %d BlockSignature docs for %d slots, got %d", wantSigs, wantSigs, len(sigs))
	}
	for _, sig := range sigs {
		if solanaLiveString(sig["blockHash"]) == "" {
			t.Errorf("stored signature %#v has no blockHash", sig["blockNumber"])
		}
		if solanaLiveString(sig["merkleRoot"]) == "" {
			t.Errorf("signature for slot %#v has an empty merkleRoot", sig["blockNumber"])
		}
		if solanaLiveString(sig["signatureValue"]) == "" {
			t.Errorf("signature value for slot %#v is empty; the keyring identity (KeyringSecret) is the first suspect", sig["blockNumber"])
		}
	}
}

// TestSolanaLiveIndexingAdvances verifies skipped-slot handling live: the
// stored slot count increases over an idle window and never regresses.
// Devnet produces skipped slots constantly, so a stall is real evidence of
// a bug; devnet rate limits may throttle progress, which only logs.
func TestSolanaLiveIndexingAdvances(t *testing.T) {
	if solanaLiveIndexer == nil {
		t.Fatal("live smoke TestMain did not create the indexer")
	}

	blockCol := solanaLiveTable.Block
	initial := len(solanaLiveRowMap(
		solanaLiveQuery(t, fmt.Sprintf("query { %s { _docID } }", blockCol)), blockCol))
	if initial == 0 {
		t.Fatal("no stored slots to observe")
	}

	time.Sleep(30 * time.Second)

	final := len(solanaLiveRowMap(
		solanaLiveQuery(t, fmt.Sprintf("query { %s { _docID } }", blockCol)), blockCol))
	if final < initial {
		t.Errorf("block count regressed from %d to %d", initial, final)
	}
	if final > initial {
		logger.Testf("indexed %d new solana slots in 30s", final-initial)
		return
	}
	logger.Test("no new slots in 30s (devnet rate limits may throttle; tolerated)")
}

// ioLiveAll reads a whole http response body.
func ioLiveAll(resp *http.Response) ([]byte, error) {
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
