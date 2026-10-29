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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// Live Tron smoke test against a real wallet-API endpoint (Nile testnet or
// TronGrid mainnet). The suite is skipped unless TRON_RPC_URL is set; the
// Makefile target tron-live-test enforces the same gate with a setup hint.
//
// The smoke targets only 3 blocks and a tight deadline: it proves the
// adapter wires a real endpoint into the whole pipeline, not throughput.

var (
	tronLiveIndexer    *tronLiveIndexerRun
	tronLiveGraphqlURL string
	tronLiveTable      = tron.NewCollectionNames(tron.DefaultCollectionPrefix)
)

type tronLiveIndexerRun struct {
	indexer    *indexer.ChainIndexer
	startErrCh chan error
}

func (run *tronLiveIndexerRun) startError() (error, bool) {
	select {
	case err := <-run.startErrCh:
		return err, true
	default:
		return nil, false
	}
}

// TestMain gates on TRON_RPC_URL, builds a real-chain config with a temp
// store, and runs the indexer the same way the mock suite does: the
// indexer owns its embedded DefraDB node.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)

	if os.Getenv("TRON_RPC_URL") == "" {
		logger.Test("TRON_RPC_URL not set; skipping tron live smoke (export it, for example https://nile.trongrid.io)")
		os.Exit(0)
	}

	lanIP, err := utils.GetLANIP()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot resolve a local IP for DefraDB: %v\n", err)
		os.Exit(1)
	}

	storeDir, err := os.MkdirTemp("", "tron-live-*")
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

	cfg := tronLiveConfig(storeDir, lanIP, defraPort)

	idx, err := indexer.CreateIndexer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot create the indexer: %v\n", err)
		_ = os.RemoveAll(storeDir)
		os.Exit(1)
	}
	run := &tronLiveIndexerRun{
		indexer:    idx,
		startErrCh: make(chan error, 1),
	}
	tronLiveIndexer = run
	tronLiveGraphqlURL = fmt.Sprintf("http://%s:%d/api/v0/graphql", lanIP, defraPort)

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

// tronLiveConfig mirrors the mock suite's config but with the real
// endpoint: start height unset so the real chain's tip detection runs,
// pruner and snapshotter off, the dynamic-port P2P address.
func tronLiveConfig(storeDir, lanIP string, defraPort int) *config.Config {
	cfg := &config.Config{
		Chain: config.ChainConfig{
			Name:    "Tron",
			Network: "Mainnet",
			Adapter: "tron",
		},
		DefraDB: config.DefraDBConfig{
			URL:           fmt.Sprintf("%s:%d", lanIP, defraPort),
			Embedded:      true,
			KeyringSecret: "tron-live-smoke-keyring-secret",
			P2P: config.DefraDBP2PConfig{
				Enabled:    false,
				ListenAddr: fmt.Sprintf("/ip4/%s/tcp/0", lanIP),
			},
			Store: config.DefraDBStoreConfig{
				Path: filepath.Join(storeDir, "store"),
			},
		},
		Tron: config.TronConfig{
			NodeURL:     os.Getenv("TRON_RPC_URL"),
			SolidityURL: os.Getenv("TRON_SOLIDITY_RPC_URL"),
			APIKey:      os.Getenv("TRON_API_KEY"),
		},
		Indexer: config.IndexerConfig{
			ConcurrentBlocks: 1,
			BlocksPerMinute:  0,
			MaxDocsPerTxn:    100,
			HealthServerPort: -1,
		},
		Logger: config.LoggerConfig{Development: true},
	}
	return cfg
}

// tronLiveTryQuery posts one GraphQL request without failing the test, so
// the wait phase can probe while the embedded node is still starting.
func tronLiveTryQuery(query string) (map[string]any, error) {
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return nil, fmt.Errorf("marshal graphql query: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(tronLiveGraphqlURL, "application/json", bytes.NewReader(payload))
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

// tronLiveQuery is the fatal variant for the direct assertion paths, once
// the node provably answers.
func tronLiveQuery(t *testing.T, query string) map[string]any {
	t.Helper()
	envelope, err := tronLiveTryQuery(query)
	if err != nil {
		t.Fatalf("graphql query failed: %v\nquery: %s", err, query)
	}
	if envelope["data"] == nil {
		t.Fatalf("graphql query returned no data\nquery: %s", query)
	}
	return envelope
}

// tronLiveRowMap decodes a collection row list from a graphql envelope.
func tronLiveRowMap(envelope map[string]any, collection string) []map[string]any {
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

// TestTronLiveSmoke waits for 3 real blocks, then asserts basic field
// integrity on whatever arrived plus one signature doc per stored block.
func TestTronLiveSmoke(t *testing.T) {
	if tronLiveIndexer == nil {
		t.Fatal("live smoke TestMain did not create the indexer")
	}

	// A real chain advances a block roughly every 3s; the generous budget
	// absorbs connect time, tip detection, and head lag without ever
	// coming near the 120s suite timeout.
	target := 3
	budget := 100 * time.Second
	deadline := time.Now().Add(budget)
	var lastErr error
	blockCol := tronLiveTable.Block
	for {
		if err, settled := tronLiveIndexer.startError(); settled {
			if err != nil {
				t.Fatalf("live indexer exited with %v before %d blocks were stored", err, target)
			}
			break
		}
		envelope, err := tronLiveTryQuery(fmt.Sprintf("query { %s { _docID } }", blockCol))
		if err == nil {
			if rows := tronLiveRowMap(envelope, blockCol); len(rows) >= target {
				logger.Testf("live smoke: %d tron blocks stored on the real endpoint", len(rows))
				break
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("block collection never reached %d rows within %v (last graphql error: %v)", target, budget, lastErr)
		}
		time.Sleep(time.Second)
	}

	// Block field integrity on whatever arrived.
	envelope := tronLiveQuery(t, fmt.Sprintf("query { %s(limit: 5) { number hash timestamp parentHash witnessAddress transactionCount } }", blockCol))
	blocks := tronLiveRowMap(envelope, blockCol)
	if len(blocks) == 0 {
		t.Fatal("no Block docs queryable after the wait phase")
	}
	for _, block := range blocks {
		if block["hash"] == "" {
			t.Errorf("stored block %#v has no hash", block["number"])
		}
		if _, ok := block["timestamp"].(float64); !ok {
			t.Errorf("stored block %#v has a missing or non-numeric timestamp", block["number"])
		}
	}

	// Transaction field integrity on whatever arrived.
	txCol := tronLiveTable.Transaction
	txEnvelope := tronLiveQuery(t, fmt.Sprintf("query { %s(limit: 5) { hash blockNumber } }", txCol))
	for _, tx := range tronLiveRowMap(txEnvelope, txCol) {
		if tronLiveString(tx["hash"]) == "" {
			t.Errorf("stored transaction %#v has no hash", tx)
		}
		if tx["blockNumber"] == nil {
			t.Errorf("stored transaction %#v has no blockNumber", tx)
		}
	}

	// One signature doc per stored block: content coverage and a real
	// signature value, with the keyring named as the suspect when empty.
	sigCol := tronLiveTable.BlockSignature
	sigEnvelope := tronLiveQuery(t, fmt.Sprintf("query { %s { blockNumber blockHash merkleRoot cidCount signatureValue } }", sigCol))
	sigs := tronLiveRowMap(sigEnvelope, sigCol)
	if len(sigs) < len(blocks) {
		t.Errorf("expected at least %d BlockSignature docs for %d blocks, got %d", len(blocks), len(blocks), len(sigs))
	}
	for _, sig := range sigs {
		if sig["blockHash"] == "" {
			t.Errorf("stored signature %#v has no blockHash", sig["blockNumber"])
		}
		if sig["merkleRoot"] == "" {
			t.Errorf("stored signature for block %#v has an empty merkleRoot", sig["blockNumber"])
		}
		if sig["signatureValue"] == "" {
			t.Errorf("signature value for block %#v is empty; the keyring identity (KeyringSecret) is the first suspect", sig["blockNumber"])
		}
	}
}

// tronLiveString normalizes a decoded GraphQL string value.
func tronLiveString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// ioLiveAll reads a whole http response body.
func ioLiveAll(resp *http.Response) ([]byte, error) {
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
