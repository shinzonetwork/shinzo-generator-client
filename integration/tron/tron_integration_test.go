//go:build integration
// +build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// Mock Tron integration suite: a scripted two-branch wallet API plus a
// couple of real indexers, each owning its own embedded DefraDB node. The
// suite does not stand up a node itself: the enterprise DefraDB-via-HTTP
// path is gone, so indexers are created and started the way the live suite
// does it, and assertions poll GraphQL over HTTP on the embedded node's
// port.
//
// The lifecycle runs as one ordered parent test with sequential subtests:
// startup, field assertions, reorg (reorg_test.go), restart
// (restart_test.go). Phases communicate through package state because each
// subtest depends on the previous one having run.

var (
	tronChain        *mockChain
	tronMockURL      string
	tronStoreDir     string
	tronDefraIP      string
	tronDefraPort    int
	tronGraphqlURLs  []string
	tronFirstIndexer *tronIndexerRun
	tronNextIndexer  *tronIndexerRun
)

// tronIndexerRun couples an indexer with the exit channel of its
// StartIndexing goroutine, so phases can notice an init failure without
// waiting out the full polling budget.
type tronIndexerRun struct {
	indexer    *indexer.ChainIndexer
	startErrCh chan error
}

// startError reports whether StartIndexing has returned, and with what.
func (run *tronIndexerRun) startError() (error, bool) {
	select {
	case err := <-run.startErrCh:
		return err, true
	default:
		return nil, false
	}
}

// TestMain owns the suite-scoped resources: logger, temp store dir, a
// reserved port for the embedded DefraDB HTTP API, and the mock wallet
// server. It never starts a DefraDB node: the indexers own their nodes.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	logger.Test("TestMain - starting tron mock integration suite")

	lanIP, err := utils.GetLANIP()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot resolve a local IP for DefraDB: %v\n", err)
		os.Exit(1)
	}
	tronDefraIP = lanIP

	tronStoreDir, err = os.MkdirTemp("", "tron-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot create suite store dir: %v\n", err)
		os.Exit(1)
	}

	// Reserve one free port for the embedded node's HTTP API before any
	// indexer starts; reuse it across both restarts so GraphQL polling has
	// a stable candidate address.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot reserve a port for DefraDB: %v\n", err)
		_ = os.RemoveAll(tronStoreDir)
		os.Exit(1)
	}
	tronDefraPort = listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	tronGraphqlURLs = []string{
		fmt.Sprintf("http://%s:%d/api/v0/graphql", tronDefraIP, tronDefraPort),
		fmt.Sprintf("http://127.0.0.1:%d/api/v0/graphql", tronDefraPort),
	}

	tronChain = newMockChain()
	server := tronChain.startServer()
	tronMockURL = server.URL

	code := m.Run()

	// Teardown: stop whatever indexer is still running, close the mock
	// server, drop the suite store dir.
	if tronFirstIndexer != nil {
		tronFirstIndexer.indexer.StopIndexing()
	}
	if tronNextIndexer != nil {
		tronNextIndexer.indexer.StopIndexing()
	}
	server.Close()
	if err := os.RemoveAll(tronStoreDir); err != nil {
		logger.Test(fmt.Sprintf("TestMain - failed to remove suite store dir: %v", err))
	}

	os.Exit(code)
}

// testTronConfig builds the suite config directly, no YAML round-trip.
// The DefraDB URL is host:port of the reserved port: the embedded node
// binds that address and the suite knows exactly where GraphQL lives. The
// keyring sits in a keys dir beside the store path, and the same secret on
// restart keeps the signing identity stable.
func testTronConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name:    "Tron",
			Network: "Mainnet",
			Adapter: "tron",
		},
		DefraDB: config.DefraDBConfig{
			URL:           fmt.Sprintf("%s:%d", tronDefraIP, tronDefraPort),
			Embedded:      true,
			KeyringSecret: tronTestKeyringSecret,
			P2P: config.DefraDBP2PConfig{
				Enabled:    false,
				ListenAddr: fmt.Sprintf("/ip4/%s/tcp/0", tronDefraIP),
			},
			Store: config.DefraDBStoreConfig{
				Path: filepath.Join(tronStoreDir, "store"),
			},
		},
		Tron: config.TronConfig{
			NodeURL: tronMockURL,
			APIKey:  tronTestAPIKey,
		},
		Indexer: config.IndexerConfig{
			StartHeight:      int(tronStartHeight),
			ConcurrentBlocks: 1,
			BlocksPerMinute:  0,
			MaxDocsPerTxn:    100,
			HealthServerPort: -1,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}
}

// startTronIndexer creates an indexer and starts it in the background the
// way the live suite does: StartIndexing(false) lets the indexer own its
// embedded DefraDB node. Init failures are captured for the polling loops.
func startTronIndexer(t *testing.T) *tronIndexerRun {
	idx, err := indexer.CreateIndexer(testTronConfig())
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	run := &tronIndexerRun{
		indexer:    idx,
		startErrCh: make(chan error, 1),
	}
	go func() {
		run.startErrCh <- idx.StartIndexing(false)
	}()
	return run
}

// tronGraphQLRun posts one GraphQL request to the first responsive
// candidate URL and returns the decoded response envelope.
func tronGraphQLRun(query string) (map[string]any, error) {
	var lastErr error
	for _, url := range tronGraphqlURLs {
		payload, err := json.Marshal(map[string]any{"query": query})
		if err != nil {
			return nil, fmt.Errorf("marshal graphql query: %w", err)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		body, err := ioAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("graphql query failed with status %d: %s", resp.StatusCode, body)
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			lastErr = fmt.Errorf("decode graphql response: %w", err)
			continue
		}
		return envelope, nil
	}
	return nil, lastErr
}

// tronGraphQLOnce posts once to the first candidate URL and fails the test
// on any error or GraphQL errors field. Use once the node provably answers
// (direct assertion paths); the polling loops use the try variant.
func tronGraphQLQuery(t *testing.T, query string) map[string]any {
	t.Helper()
	envelope, err := tronGraphQLRun(query)
	if err != nil {
		t.Fatalf("graphql query failed: %v\nquery: %s", err, query)
	}
	if errs, ok := envelope["errors"].([]any); ok && len(errs) > 0 {
		t.Fatalf("graphql query returned errors %v\nquery: %s", errs, query)
	}
	if envelope["data"] == nil {
		t.Fatalf("graphql query returned no data\nquery: %s", query)
	}
	return envelope
}

// tronQueryRows runs a collection query and returns the rows as maps. The
// response key is the collection name itself, never the graphql field form
// of the live suite's wrinkle.
func tronQueryRows(t *testing.T, collection, filter, fields string) []map[string]any {
	t.Helper()
	query := fmt.Sprintf("query { %s%s { %s } }", collection, filter, fields)
	envelope := tronGraphQLQuery(t, query)
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("graphql response has no data object\nquery: %s", query)
	}
	raw, ok := data[collection]
	if !ok || raw == nil {
		return nil
	}
	rows, ok := raw.([]any)
	if !ok {
		t.Fatalf("graphql collection %s did not return an array (got %T)\nquery: %s", collection, raw, query)
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		mapped, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("graphql row is not an object: %T", row)
		}
		result = append(result, mapped)
	}
	return result
}

// tryTronQueryRows is tronQueryRows without the test dependency, for the
// polling loops that probe while the node is still starting up.
func tryTronQueryRows(collection, filter, fields string) ([]map[string]any, error) {
	query := fmt.Sprintf("query { %s%s { %s } }", collection, filter, fields)
	envelope, err := tronGraphQLRun(query)
	if err != nil {
		return nil, err
	}
	if errs, ok := envelope["errors"].([]any); ok && len(errs) > 0 {
		return nil, fmt.Errorf("graphql error: %v", errs)
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("graphql response has no data object")
	}
	raw := data[collection]
	rows, ok := raw.([]any)
	if !ok {
		if raw == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("graphql collection %s returned %T", collection, raw)
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		mapped, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("graphql row is not an object: %T", row)
		}
		result = append(result, mapped)
	}
	return result, nil
}

// pollTronBlockCount waits until the block collection holds at least target
// rows, checking the indexer's start error so a failed init fails the
// waiting phase immediately instead of after the budget.
func pollTronBlockCount(t *testing.T, run *tronIndexerRun, target int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		if run != nil {
			if err, settled := run.startError(); settled && err != nil {
				t.Fatalf("indexer exited with a start error before %d blocks were stored: %v", target, err)
			}
		}
		rows, err := tryTronQueryRows(tron.DefaultCollectionBlock, "", "_docID")
		if err == nil && len(rows) >= target {
			return
		}
		if err != nil {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("block collection never reached %d rows within %v (last graphql error: %v)", target, budget, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// tronInt normalizes a decoded GraphQL numeric value to int64. Numbers
// surface as float64 or int64 depending on the query shape, and rarely as
// json.Number from a strict decoder.
func tronInt(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return n
	case string:
		var n int64
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}

// tronString normalizes a decoded GraphQL string value.
func tronString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// tronFieldInt reports whether the row carries the field equal to want.
func tronFieldInt(row map[string]any, field string) int64 {
	return tronInt(row[field])
}

// suitePhaseStartup creates the first indexer, drives it to the 10 stored
// blocks of branch A, and makes sure every wallet request carried the
// configured API key header.
func suitePhaseStartup(t *testing.T) {
	t.Helper()
	if tronFirstIndexer != nil {
		t.Fatal("startup phase already ran")
	}
	run := startTronIndexer(t)
	tronFirstIndexer = run
	pollTronBlockCount(t, run, 10, 90*time.Second)

	if missing := tronChain.missingAuthCount(); missing != 0 {
		t.Errorf("%d wallet requests reached the mock without the configured %s header",
			missing, tronWalletAPIKeyHdr)
	}
}

// suitePhaseFieldAssertions verifies the stored documents field by field
// against the scripted branch-A content.
func suitePhaseFieldAssertions(t *testing.T) {
	t.Helper()

	// Block 100: identity, lineage, header fields, transaction count.
	blockA100 := tronChain.blockByNum(tronBranchA, 100)
	if blockA100 == nil {
		t.Fatal("mock chain lost branch-A block 100")
	}
	blocks := tronQueryRows(t, tron.DefaultCollectionBlock,
		"(filter: {number: {_eq: 100}})",
		"hash number parentHash witnessAddress timestamp transactionCount")
	if len(blocks) != 1 {
		t.Fatalf("expected exactly 1 Block doc for number 100, got %d", len(blocks))
	}
	b100 := blocks[0]
	if got, want := tronString(b100["hash"]), blockA100.BlockID; got != want {
		t.Errorf("block 100 hash mismatch: got %s want %s", got, want)
	}
	if got, want := tronString(b100["parentHash"]), blockA100.BlockHeader.RawData.ParentHash; got != want {
		t.Errorf("block 100 parentHash mismatch: got %s want %s", got, want)
	}
	if got, want := tronString(b100["witnessAddress"]), tronWitnessA; got != want {
		t.Errorf("block 100 witnessAddress mismatch: got %s want %s", got, want)
	}
	if got, want := tronFieldInt(b100, "timestamp"), blockA100.BlockHeader.RawData.Timestamp; got != want {
		t.Errorf("block 100 timestamp mismatch: got %d want %d", got, want)
	}
	if got, want := tronFieldInt(b100, "transactionCount"), int64(1); got != want {
		t.Errorf("block 100 transactionCount mismatch: got %d want %d", got, want)
	}

	// Transfer transaction of block 100: identities, amount, receipt
	// accounting, raw payload.
	txs := tronQueryRows(t, tron.DefaultCollectionTransaction,
		fmt.Sprintf("(filter: {hash: {_eq: %q}})", tronTx100ID),
		"hash blockNumber from to amount fee energyUsage rawData ret")
	if len(txs) != 1 {
		t.Fatalf("expected exactly 1 Transaction doc for %s, got %d", tronTx100ID, len(txs))
	}
	tx := txs[0]
	if got, want := tronString(tx["from"]), tronOwnerAddr; got != want {
		t.Errorf("transfer tx from mismatch: got %s want %s", got, want)
	}
	if got, want := tronString(tx["to"]), tronRecipientAddr; got != want {
		t.Errorf("transfer tx to mismatch: got %s want %s", got, want)
	}
	if got, want := tronFieldInt(tx, "amount"), int64(1500000); got != want {
		t.Errorf("transfer tx amount mismatch: got %d want %d", got, want)
	}
	if got, want := tronFieldInt(tx, "fee"), tronTransferFee; got != want {
		t.Errorf("transfer tx fee mismatch (receipt join): got %d want %d", got, want)
	}
	if got, want := tronFieldInt(tx, "energyUsage"), tronTransferFuel; got != want {
		t.Errorf("transfer tx energyUsage mismatch (receipt join): got %d want %d", got, want)
	}
	if got, want := tronFieldInt(tx, "blockNumber"), int64(100); got != want {
		t.Errorf("transfer tx blockNumber mismatch: got %d want %d", got, want)
	}
	rawData := tronString(tx["rawData"])
	if rawData == "" {
		t.Error("transfer tx rawData is empty; the whole raw_data payload must survive verbatim")
	} else if !bytes.Contains([]byte(rawData), []byte("TransferContract")) {
		t.Errorf("transfer tx rawData does not carry the contract type: %s", rawData)
	}

	// Empty block 102: the block doc exists with zero transactions.
	empties := tronQueryRows(t, tron.DefaultCollectionBlock,
		"(filter: {number: {_eq: 102}})",
		"number transactionCount")
	if len(empties) != 1 {
		t.Fatalf("expected exactly 1 Block doc for empty block 102, got %d", len(empties))
	}
	if got, want := tronFieldInt(empties[0], "transactionCount"), int64(0); got != want {
		t.Errorf("empty block 102 transactionCount mismatch: got %d want %d", got, want)
	}
	txs102 := tronQueryRows(t, tron.DefaultCollectionTransaction,
		"(filter: {blockNumber: {_eq: 102}})", "_docID")
	if len(txs102) != 0 {
		t.Errorf("empty block 102 must have no Transaction docs, got %d", len(txs102))
	}

	// Duplicated event of block 101: two Log docs with identical topics but
	// different synthetic logIndex ordinals and distinct dockeys.
	logs := tronQueryRows(t, tron.DefaultCollectionLog,
		"(filter: {blockNumber: {_eq: 101}})",
		"_docID topics logIndex transactionHash")
	if len(logs) != 2 {
		t.Fatalf("expected exactly 2 Log docs for the duplicated event, got %d", len(logs))
	}
	if got, want := tronInt(logs[0]["logIndex"]), int64(0); got != want {
		t.Errorf("duplicated event first logIndex: got %d want %d", got, want)
	}
	if got, want := tronInt(logs[1]["logIndex"]), int64(1); got != want {
		t.Errorf("duplicated event second logIndex: got %d want %d", got, want)
	}
	if logs[0]["_docID"] == logs[1]["_docID"] {
		t.Error("duplicated event wrote a single Log doc; the two logIndex ordinals must stay distinct documents")
	}
	firstTopics, ok := logs[0]["topics"].([]any)
	if !ok || len(firstTopics) != 1 || tronString(firstTopics[0]) != tronEventTopic {
		t.Errorf("log topics mismatch: got %#v want [%s]", logs[0]["topics"], tronEventTopic)
	}
	if !sameAnySlice(logs[0]["topics"], logs[1]["topics"]) {
		t.Error("the duplicated event produced Log docs with differing topics")
	}

	// Internal transfer of block 101: identity fields plus the
	// numeric-typed asset name normalized to its string form.
	transfers := tronQueryRows(t, tron.DefaultCollectionInternalTransfer,
		"(filter: {blockNumber: {_eq: 101}})",
		"_docID from to amount assetName rejected transferIndex")
	if len(transfers) != 1 {
		t.Fatalf("expected exactly 1 InternalTransfer doc for block 101, got %d", len(transfers))
	}
	itx := transfers[0]
	if got, want := tronString(itx["from"]), tronOwnerAddr; got != want {
		t.Errorf("internal transfer from mismatch: got %s want %s", got, want)
	}
	if got, want := tronString(itx["to"]), tronContractAddr; got != want {
		t.Errorf("internal transfer to mismatch: got %s want %s", got, want)
	}
	if got, want := tronFieldInt(itx, "amount"), tronContractValue; got != want {
		t.Errorf("internal transfer amount mismatch: got %d want %d", got, want)
	}
	if got, want := tronString(itx["assetName"]), "105"; got != want {
		t.Errorf("internal transfer assetName normalization failed: got %q want %q", got, want)
	}
	if rejected, ok := itx["rejected"].(bool); ok && rejected {
		t.Error("internal transfer rejected must be false")
	}

	// BlockSignature doc per branch-A block: content coverage and an actual
	// signature value, with the keyring named as the suspect when missing.
	for num := tronStartHeight; num <= tronHeadA; num++ {
		blockA := tronChain.blockByNum(tronBranchA, num)
		sigs := tronQueryRows(t, tron.DefaultCollectionBlockSignature,
			fmt.Sprintf("(filter: {blockNumber: {_eq: %d}})", num),
			"blockNumber blockHash merkleRoot cidCount signatureValue")
		if len(sigs) != 1 {
			t.Errorf("expected exactly 1 BlockSignature doc for block %d, got %d", num, len(sigs))
			continue
		}
		sig := sigs[0]
		if got, want := tronString(sig["blockHash"]), blockA.BlockID; got != want {
			t.Errorf("block %d signature blockHash mismatch: got %s want %s", num, got, want)
		}
		if tronString(sig["merkleRoot"]) == "" {
			t.Errorf("block %d signature has an empty merkleRoot", num)
		}
		if tronFieldInt(sig, "cidCount") < 1 {
			t.Errorf("block %d signature has cidCount %d, must cover at least the block doc",
				num, tronFieldInt(sig, "cidCount"))
		}
		if tronString(sig["signatureValue"]) == "" {
			t.Errorf("block %d signature value is empty; the keyring identity (KeyringSecret) is the first suspect", num)
		}
	}
}

// TestTronMockSuite is the single ordered lifecycle test. The subtests run
// strictly in order and share package state.
func TestTronMockSuite(t *testing.T) {
	t.Run("startup", suitePhaseStartup)
	t.Run("field assertions", suitePhaseFieldAssertions)
	t.Run("reorg", suitePhaseReorg)
	t.Run("restart", suitePhaseRestart)
}

// sameAnySlice reports whether two decoded JSON arrays carry equal values.
func sameAnySlice(a, b any) bool {
	first, ok := a.([]any)
	if !ok {
		return false
	}
	second, ok := b.([]any)
	if !ok {
		return false
	}
	if len(first) != len(second) {
		return false
	}
	for i := range first {
		firstRaw, fErr := json.Marshal(first[i])
		secondRaw, sErr := json.Marshal(second[i])
		if fErr != nil || sErr != nil || string(firstRaw) != string(secondRaw) {
			return false
		}
	}
	return true
}

// ioAll reads a whole http response body.
func ioAll(r io.ReadCloser) ([]byte, error) {
	defer func() { _ = r.Close() }()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
