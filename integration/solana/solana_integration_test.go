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
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/utils"
)

// Mock Solana integration suite: a scripted JSON-RPC chain fake plus real
// indexers, each owning its own embedded DefraDB node. The suite does not
// stand up a node itself: indexers are created and started the way the live
// suite does it, and assertions poll GraphQL over HTTP on the embedded
// node's port.
//
// The lifecycle runs as one ordered parent test with sequential subtests:
// startup, field assertions, skipped-slot advancement, duplicate-content
// instructions, restart (restart_test.go). Phases communicate through
// package state because each subtest depends on the previous one having
// run. Solana's general-fork case is deliberately absent: the generic
// walk-back fetches chain-side ancestor headers BY HASH, and the Solana
// fetcher can only serve headers it fetched itself (no header-by-hash RPC
// exists), so a flip below the walking cursor stalls the walk-back
// indefinitely; see the Phase 6 note in specs/features/SOLANA-INTEGRATION.md.

var (
	solanaChain        *solanaMockChain
	solanaMockURL      string
	solanaStoreDir     string
	solanaDefraIP      string
	solanaDefraPort    int
	solanaGraphqlURLs  []string
	solanaFirstIndexer *solanaIndexerRun
	solanaNextIndexer  *solanaIndexerRun
)

// solanaIndexerRun couples an indexer with the exit channel of its
// StartIndexing goroutine, so phases can notice an init failure without
// waiting out the full polling budget.
type solanaIndexerRun struct {
	indexer    *indexer.ChainIndexer
	startErrCh chan error
}

// startError reports whether StartIndexing has returned, and with what.
func (run *solanaIndexerRun) startError() (error, bool) {
	select {
	case err := <-run.startErrCh:
		return err, true
	default:
		return nil, false
	}
}

// TestMain owns the suite-scoped resources: logger, temp store dir, a
// reserved port for the embedded DefraDB HTTP API, and the mock chain
// server. It never starts a DefraDB node: the indexers own their nodes.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	logger.Test("TestMain - starting solana mock integration suite")

	lanIP, err := utils.GetLANIP()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot resolve a local IP for DefraDB: %v\n", err)
		os.Exit(1)
	}
	solanaDefraIP = lanIP

	solanaStoreDir, err = os.MkdirTemp("", "solana-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot create suite store dir: %v\n", err)
		os.Exit(1)
	}

	// Reserve one free port for the embedded node's HTTP API before any
	// indexer starts; reuse it across the restart so GraphQL polling has
	// a stable candidate address.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: cannot reserve a port for DefraDB: %v\n", err)
		_ = os.RemoveAll(solanaStoreDir)
		os.Exit(1)
	}
	solanaDefraPort = listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	solanaGraphqlURLs = []string{
		fmt.Sprintf("http://%s:%d/api/v0/graphql", solanaDefraIP, solanaDefraPort),
		fmt.Sprintf("http://127.0.0.1:%d/api/v0/graphql", solanaDefraPort),
	}

	solanaChain = newSolanaMockChain()
	server := solanaChain.startServer()
	solanaMockURL = server.URL

	code := m.Run()

	// Teardown: stop whatever indexer is still running, close the mock
	// server, drop the suite store dir.
	if solanaFirstIndexer != nil {
		solanaFirstIndexer.indexer.StopIndexing()
	}
	if solanaNextIndexer != nil {
		solanaNextIndexer.indexer.StopIndexing()
	}
	server.Close()
	if err := os.RemoveAll(solanaStoreDir); err != nil {
		logger.Test(fmt.Sprintf("TestMain - failed to remove suite store dir: %v", err))
	}

	os.Exit(code)
}

// testSolanaConfig builds the suite config directly, no YAML round-trip.
// The DefraDB URL is host:port of the reserved port: the embedded node
// binds that address and the suite knows exactly where GraphQL lives. The
// keyring sits in a keys dir beside the store path, and the same secret on
// restart keeps the signing identity stable. No WS URL: the suite runs
// plain polling, exactly the default deployment shape.
func testSolanaConfig() *config.Config {
	return &config.Config{
		Chain: config.ChainConfig{
			Name:    "Solana",
			Network: "Mainnet",
			Adapter: "solana",
		},
		DefraDB: config.DefraDBConfig{
			URL:           fmt.Sprintf("%s:%d", solanaDefraIP, solanaDefraPort),
			Embedded:      true,
			KeyringSecret: solanaTestKeyring,
			P2P: config.DefraDBP2PConfig{
				Enabled:    false,
				ListenAddr: fmt.Sprintf("/ip4/%s/tcp/0", solanaDefraIP),
			},
			Store: config.DefraDBStoreConfig{
				Path: filepath.Join(solanaStoreDir, "store"),
			},
		},
		Solana: config.SolanaConfig{
			NodeURL:                        solanaMockURL,
			APIKey:                         solanaTestAPIKey,
			Commitment:                     config.DefaultSolanaCommitment,
			MaxSupportedTransactionVersion: config.DefaultSolanaMaxSupportedTxVersion,
		},
		Indexer: config.IndexerConfig{
			StartHeight:      int(solanaStartSlot),
			ConcurrentBlocks: 1,
			BlocksPerMinute:  0,
			MaxDocsPerTxn:    100,
			HealthServerPort: -1,
			StartBuffer:      10,
		},
		Logger: config.LoggerConfig{Development: true},
	}
}

// startSolanaIndexer creates an indexer and starts it in the background the
// way the live suite does: StartIndexing(false) lets the indexer own its
// embedded DefraDB node. Init failures are captured for the polling loops.
func startSolanaIndexer(t *testing.T) *solanaIndexerRun {
	idx, err := indexer.CreateIndexer(testSolanaConfig())
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	run := &solanaIndexerRun{
		indexer:    idx,
		startErrCh: make(chan error, 1),
	}
	go func() {
		run.startErrCh <- idx.StartIndexing(false)
	}()
	return run
}

// solanaGraphQLRun posts one GraphQL request to the first responsive
// candidate URL and returns the decoded response envelope.
func solanaGraphQLRun(query string) (map[string]any, error) {
	var lastErr error
	for _, url := range solanaGraphqlURLs {
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

// solanaGraphQLQuery posts once and fails the test on any error or GraphQL
// errors field. Use once the node provably answers (direct assertion
// paths); the polling loops use the try variant.
func solanaGraphQLQuery(t *testing.T, query string) map[string]any {
	t.Helper()
	envelope, err := solanaGraphQLRun(query)
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

// solanaQueryRows runs a collection query and returns the rows as maps. The
// response key is the collection name itself.
func solanaQueryRows(t *testing.T, collection, filter, fields string) []map[string]any {
	t.Helper()
	query := fmt.Sprintf("query { %s%s { %s } }", collection, filter, fields)
	envelope := solanaGraphQLQuery(t, query)
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

// trySolanaQueryRows is solanaQueryRows without the test dependency, for
// the polling loops that probe while the node is still starting up.
func trySolanaQueryRows(collection, filter, fields string) ([]map[string]any, error) {
	query := fmt.Sprintf("query { %s%s { %s } }", collection, filter, fields)
	envelope, err := solanaGraphQLRun(query)
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

// pollSolanaBlockCount waits until the block collection holds at least
// target rows, checking the indexer's start error so a failed init fails
// the waiting phase immediately instead of after the budget.
func pollSolanaBlockCount(t *testing.T, run *solanaIndexerRun, target int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		if run != nil {
			if err, settled := run.startError(); settled && err != nil {
				t.Fatalf("indexer exited with a start error before %d blocks were stored: %v", target, err)
			}
		}
		rows, err := trySolanaQueryRows(solana.DefaultCollectionBlock, "", "_docID")
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

// solanaBlockRow is one Block document reduced to what the skip and restart
// phases compare.
type solanaBlockRow struct {
	number int64
	hash   string
	docID  string
}

// solanaRangeFilter builds a GraphQL filter for an inclusive numeric range
// on an Int field, shared by every per-slot assertion.
func solanaRangeFilter(field string, from, to int64) string {
	return fmt.Sprintf("(filter: {%s: {_geq: %d, _leq: %d}})", field, from, to)
}

// solanaBlockRows loads the hashed rows for a slot range, keyed by slot.
// A slot missing inside the range is a hard failure: the caller's asserts
// index the map directly.
func solanaBlockRows(t *testing.T, from, to int64) map[int64]solanaBlockRow {
	t.Helper()
	rows := solanaQueryRows(t, solana.DefaultCollectionBlock,
		solanaRangeFilter("number", from, to),
		"number hash _docID")
	result := make(map[int64]solanaBlockRow, len(rows))
	for _, row := range rows {
		got := solanaBlockRow{
			number: solanaFieldInt(row, "number"),
			hash:   solanaString(row["hash"]),
			docID:  solanaString(row["_docID"]),
		}
		result[got.number] = got
	}
	return result
}

// suitePhaseStartup creates the first indexer, drives it across the
// skipped slot to the branch head, and makes sure every RPC request
// carried the configured API key header. Nine slots are stored: the branch
// spans 100..109 minus the skipped 107.
func suitePhaseStartup(t *testing.T) {
	t.Helper()
	if solanaFirstIndexer != nil {
		t.Fatal("startup phase already ran")
	}
	run := startSolanaIndexer(t)
	solanaFirstIndexer = run
	wantStored := 9 // 100..109 minus the skipped 107
	pollSolanaBlockCount(t, run, wantStored, 90*time.Second)

	if missing := solanaChain.missingAuthCount(); missing != 0 {
		t.Errorf("%d RPC requests reached the mock without the configured %s header",
			missing, solanaDefaultAuthHdr)
	}
}

// suitePhaseFieldAssertions verifies the stored documents field by field
// against the scripted content. The hot transaction's identity credentials
// come from the same deterministic seeds the mock derives its wire shape
// from, so assertions pin exact base58 values.
func suitePhaseFieldAssertions(t *testing.T) {
	t.Helper()

	// Hot slot 100: identity, lineage, header fields, counts.
	wantHash := solanaChain.blockHashAt(solanaStartSlot)
	wantParent := solanaGenesisHash()
	wantTime := solanaChain.blockTimeAt(solanaStartSlot)
	blocks := solanaQueryRows(t, solana.DefaultCollectionBlock,
		"(filter: {number: {_eq: 100}})",
		"hash number parentHash blockTime transactionCount rewardCount")
	if len(blocks) != 1 {
		t.Fatalf("expected exactly 1 Block doc for number 100, got %d", len(blocks))
	}
	b100 := blocks[0]
	if got, want := solanaString(b100["hash"]), wantHash; got != want {
		t.Errorf("slot 100 hash mismatch: got %s want %s", got, want)
	}
	if got, want := solanaString(b100["parentHash"]), wantParent; got != want {
		t.Errorf("slot 100 parentHash mismatch: got %s want %s", got, want)
	}
	if got, want := solanaFieldInt(b100, "blockTime"), wantTime; got != want {
		t.Errorf("slot 100 blockTime mismatch: got %d want %d", got, want)
	}
	if got, want := solanaFieldInt(b100, "transactionCount"), int64(1); got != want {
		t.Errorf("slot 100 transactionCount mismatch: got %d want %d", got, want)
	}
	if got, want := solanaFieldInt(b100, "rewardCount"), int64(2); got != want {
		t.Errorf("slot 100 rewardCount mismatch: got %d want %d", got, want)
	}

	// The transaction of the hot slot: identity fields and metadata that
	// rides the versioned-transaction meta.
	txs := solanaQueryRows(t, solana.DefaultCollectionTransaction,
		fmt.Sprintf("(filter: {signature: {_eq: %q}})", solanaSignatureWire()),
		"signature version err fee computeUnitsConsumed blockNumber blockHash recentBlockhash")
	if len(txs) != 1 {
		t.Fatalf("expected exactly 1 Transaction doc for the hot slot, got %d", len(txs))
	}
	tx := txs[0]
	if got, want := solanaString(tx["version"]), "legacy"; got != want {
		t.Errorf("tx version mismatch: got %s want %s", got, want)
	}
	if got, want := solanaFieldInt(tx, "fee"), int64(solanaHotTxFee); got != want {
		t.Errorf("tx fee mismatch: got %d want %d", got, want)
	}
	if got, want := solanaFieldInt(tx, "computeUnitsConsumed"), int64(solanaHotTxCU); got != want {
		t.Errorf("tx computeUnitsConsumed mismatch: got %d want %d", got, want)
	}
	if got, want := solanaFieldInt(tx, "blockNumber"), solanaStartSlot; got != want {
		t.Errorf("tx blockNumber mismatch: got %d want %d", got, want)
	}
	if got, want := solanaString(tx["blockHash"]), wantHash; got != want {
		t.Errorf("tx blockHash mismatch: got %s want %s", got, want)
	}
	// Explicit selection makes GraphQL echo err as null when the stored
	// doc omits the key, so only a non-null value means failure leaked in.
	if got, exists := tx["err"]; exists && got != nil {
		t.Errorf("successful tx must not carry the err field, got %#v", got)
	}

	// Token balance change of the hot slot: the pre/post diff materialized.
	tbcs := solanaQueryRows(t, solana.DefaultCollectionTokenBalanceChange,
		fmt.Sprintf("(filter: {transactionSignature: {_eq: %q}})", solanaSignatureWire()),
		"mint owner tokenAccount preAmount postAmount blockNumber")
	if len(tbcs) != 1 {
		t.Fatalf("expected exactly 1 TokenBalanceChange doc, got %d", len(tbcs))
	}
	tbc := tbcs[0]
	if got, want := solanaString(tbc["mint"]), solanaBase58Key("mock-tx-100-mint"); got != want {
		t.Errorf("token balance mint mismatch: got %s want %s", got, want)
	}
	if got, want := solanaString(tbc["tokenAccount"]), solanaBase58Key("mock-tx-100-inner-account"); got != want {
		t.Errorf("token balance tokenAccount mismatch: got %s want %s", got, want)
	}
	if got, want := solanaString(tbc["preAmount"]), solanaTBCPreAmt; got != want {
		t.Errorf("token balance preAmount mismatch: got %s want %s", got, want)
	}
	if got, want := solanaString(tbc["postAmount"]), solanaTBCPostAmt; got != want {
		t.Errorf("token balance postAmount mismatch: got %s want %s", got, want)
	}

	// Rewards of the hot slot: fee debit and voting credit with the
	// commission string field.
	rewards := solanaQueryRows(t, solana.DefaultCollectionReward,
		"(filter: {blockNumber: {_eq: 100}})",
		"pubkey lamports postBalance rewardType commission")
	if len(rewards) != 2 {
		t.Fatalf("expected exactly 2 Reward docs for slot 100, got %d", len(rewards))
	}
	byType := map[string]map[string]any{}
	for _, reward := range rewards {
		byType[solanaString(reward["rewardType"])] = reward
	}
	feeReward, ok := byType[solanaRewardFeeType]
	if !ok {
		t.Fatalf("fee reward doc missing, got types %v", keysOf(byType))
	}
	if got, want := solanaFieldInt(feeReward, "lamports"), solanaRewardFeeLamp; got != want {
		t.Errorf("fee reward lamports mismatch: got %d want %d", got, want)
	}
	voteReward, ok := byType[solanaRewardVoteType]
	if !ok {
		t.Fatalf("voting reward doc missing, got types %v", keysOf(byType))
	}
	if got, want := solanaString(voteReward["commission"]), fmt.Sprintf("%d", solanaCommission); got != want {
		t.Errorf("voting reward commission mismatch: got %s want %s", got, want)
	}

	// Quiet slot 101: stored with rewards but no transactions, a sanity
	// check that an empty-slot pipeline writes the block + rewards pair
	// without tripping the signature stage.
	quiet := solanaQueryRows(t, solana.DefaultCollectionBlock,
		"(filter: {number: {_eq: 101}})",
		"number transactionCount rewardCount")
	if len(quiet) != 1 {
		t.Fatalf("expected exactly 1 Block doc for quiet slot 101, got %d", len(quiet))
	}
	if got, want := solanaFieldInt(quiet[0], "transactionCount"), int64(0); got != want {
		t.Errorf("quiet slot 101 transactionCount mismatch: got %d want %d", got, want)
	}
	txs101 := solanaQueryRows(t, solana.DefaultCollectionTransaction,
		"(filter: {blockNumber: {_eq: 101}})", "_docID")
	if len(txs101) != 0 {
		t.Errorf("quiet slot 101 must have no Transaction docs, got %d", len(txs101))
	}
	rewards101 := solanaQueryRows(t, solana.DefaultCollectionReward,
		"(filter: {blockNumber: {_eq: 101}})", "_docID")
	if len(rewards101) != 2 {
		t.Errorf("quiet slot 101 must store its 2 rewards (rewards stay live on empty slots), got %d", len(rewards101))
	}

	// One signed BlockSignature doc per STORED slot: content coverage and
	// an actual signature value, with the keyring named as the suspect
	// when missing. The skip slot never signs (it never stores).
	for _, slot := range []int64{100, 101, 102, 103, 104, 105, 106, 108, 109} {
		wantHash := solanaChain.blockHashAt(slot)
		sigs := solanaQueryRows(t, solana.DefaultCollectionBlockSignature,
			fmt.Sprintf("(filter: {blockNumber: {_eq: %d}})", slot),
			"blockNumber blockHash merkleRoot cidCount signatureValue")
		if len(sigs) != 1 {
			t.Errorf("expected exactly 1 BlockSignature doc for slot %d, got %d", slot, len(sigs))
			continue
		}
		sig := sigs[0]
		if got, want := solanaString(sig["blockHash"]), wantHash; got != want {
			t.Errorf("slot %d signature blockHash mismatch: got %s want %s", slot, got, want)
		}
		if solanaString(sig["merkleRoot"]) == "" {
			t.Errorf("slot %d signature has an empty merkleRoot", slot)
		}
		if solanaFieldInt(sig, "cidCount") < 1 {
			t.Errorf("slot %d signature has cidCount %d, must cover at least the block doc",
				slot, solanaFieldInt(sig, "cidCount"))
		}
		if solanaString(sig["signatureValue"]) == "" {
			t.Errorf("slot %d signature value is empty; the keyring identity (KeyringSecret) is the first suspect", slot)
		}
	}
}

// suitePhaseSkipAdvancement pins the skipped-slot contract end to end: the
// null getBlock at 107 below the confirmed tip advanced the indexer across
// the window (startup already crossed it), the skipped slot left NO block
// row behind, the produced blocks keep their scripted parent chain (the
// parentHash bridges the skip onto slot 106), and nothing rolled back.
func suitePhaseSkipAdvancement(t *testing.T) {
	t.Helper()
	if solanaFirstIndexer == nil {
		t.Fatal("skip phase must run after the startup phase")
	}

	// Exactly the nine produced slots of the startup window are stored and
	// every hash is the scripted one; nothing else exists.
	rows := solanaBlockRows(t, solanaStartSlot, solanaHeadInitial)
	if len(rows) != 9 {
		t.Fatalf("expected exactly 9 stored slots across 100..109 minus the skip, got %d rows", len(rows))
	}
	for slot := solanaStartSlot; slot <= solanaHeadInitial; slot++ {
		if slot == solanaSkipSlot {
			if _, exists := rows[slot]; exists {
				t.Fatalf("skipped slot %d stored a block row; a skip must advance, never store", slot)
			}
			continue
		}
		want := solanaChain.blockHashAt(slot)
		if got := rows[slot].hash; got != want {
			t.Errorf("slot %d hash mismatch after the skip window: got %s want %s", slot, got, want)
		}
	}

	// The parent chain bridges the skip: slot 108's parentHash must be the
	// scripted block 106 hash (the last produced ancestor), which is what
	// keeps the continuity check from raising a false reorg at the gap.
	b108 := solanaQueryRows(t, solana.DefaultCollectionBlock,
		"(filter: {number: {_eq: 108}})", "parentHash")
	if len(b108) != 1 {
		t.Fatalf("expected exactly 1 Block doc for slot 108, got %d", len(b108))
	}
	if got, want := solanaString(b108[0]["parentHash"]), solanaChain.blockHashAt(106); got != want {
		t.Errorf("slot 108 parentHash must bridge the skipped slot: got %s want slot-106 %s", got, want)
	}

	// Nothing rolled back: the signature docs still cover every stored
	// slot and no extra signature doc exists for the skip.
	sigs := solanaQueryRows(t, solana.DefaultCollectionBlockSignature,
		solanaRangeFilter("blockNumber", solanaStartSlot, solanaHeadInitial),
		"blockNumber")
	if len(sigs) != 9 {
		t.Errorf("expected exactly 9 BlockSignature docs across the skipped window, got %d", len(sigs))
	}
}

// suitePhaseDuplicateDocs pins the 2026-10-31 duplicate-content fix end to
// end: the two byte-identical OUTER instructions and the two byte-identical
// INNER instructions of the hot slot must land as four DISTINCT documents
// separated by the positional ordinals, and the block still signs.
func suitePhaseDuplicateDocs(t *testing.T) {
	t.Helper()

	instructions := solanaQueryRows(t, solana.DefaultCollectionInstruction,
		fmt.Sprintf("(filter: {transactionSignature: {_eq: %q}})", solanaSignatureWire()),
		"_docID instructionIndex innerIndex stackHeight programId data blockNumber")
	if len(instructions) != 4 {
		t.Fatalf("expected 4 instruction docs (2 outer + 2 identical inner), got %d", len(instructions))
	}

	// Collect by the stack-height invariant that discriminates inner from
	// outer instruction docs, then assert per class.
	outerDocs := make([]solanaBlockRow, 0, 2)
	innerDocs := make([]solanaBlockRow, 0, 2)
	for _, doc := range instructions {
		if hasStackHeight(doc) {
			// Inner (CPI) instruction.
			if got, want := solanaString(doc["programId"]), solanaBase58Key("mock-tx-100-inner-program"); got != want {
				t.Errorf("inner instruction programId mismatch: got %s want %s", got, want)
			}
			innerIndex, hasInner := doc["innerIndex"]
			if !hasInner || innerIndex == nil {
				t.Error("inner instruction doc must carry the innerIndex ordinal")
				continue
			}
			innerDocs = append(innerDocs, solanaBlockRow{
				number: solanaFieldInt(doc, "innerIndex"),
				hash:   solanaString(doc["_docID"]),
			})
			continue
		}
		// Outer instruction: innerIndex must be absent.
		if _, hasInner := doc["innerIndex"]; hasInner && doc["innerIndex"] != nil {
			t.Errorf("outer instruction doc must omit innerIndex, got %#v", doc["innerIndex"])
		}
		if got, want := solanaString(doc["programId"]), solanaBase58Key("mock-tx-100-outer-program"); got != want {
			t.Errorf("outer instruction programId mismatch: got %s want %s", got, want)
		}
		outerDocs = append(outerDocs, solanaBlockRow{
			number: solanaFieldInt(doc, "instructionIndex"),
			hash:   solanaString(doc["_docID"]),
		})
	}

	if len(outerDocs) != 2 || len(innerDocs) != 2 {
		t.Fatalf("expected 2 outer and 2 inner docs, got %d outer / %d inner", len(outerDocs), len(innerDocs))
	}

	// Outer ordinals: the two identical instructions must be distinct docs
	// at instructionIndex 0 and 1 inside the same transaction.
	if outerDocs[0].number == outerDocs[1].number {
		t.Errorf("byte-identical outer instructions collapsed onto one ordinal %d", outerDocs[0].number)
	}
	if outerDocs[0].hash == outerDocs[1].hash {
		t.Error("byte-identical outer instructions wrote a single doc; the instructionIndex ordinals must stay distinct")
	}
	var outerIndexes []int64
	for _, doc := range outerDocs {
		outerIndexes = append(outerIndexes, doc.number)
	}
	if outerIndexes[0]+outerIndexes[1] != 1 || outerIndexes[0] == outerIndexes[1] {
		t.Errorf("outer ordinals must be {0, 1}, got %v", outerIndexes)
	}

	// Inner ordinals: identical CPI instructions must be distinct docs at
	// innerIndex 0 and 1 under the parent outer position 0.
	if innerDocs[0].number == innerDocs[1].number {
		t.Errorf("byte-identical inner instructions collapsed onto one ordinal %d", innerDocs[0].number)
	}
	if innerDocs[0].hash == innerDocs[1].hash {
		t.Error("byte-identical inner instructions wrote a single doc; the innerIndex ordinals must stay distinct")
	}
	var innerIndexes []int64
	for _, doc := range innerDocs {
		innerIndexes = append(innerIndexes, doc.number)
	}
	if innerIndexes[0]+innerIndexes[1] != 1 || innerIndexes[0] == innerIndexes[1] {
		t.Errorf("inner ordinals must be {0, 1}, got %v", innerIndexes)
	}

	// Every instruction doc carries the block contract fields.
	for _, doc := range instructions {
		if got, want := solanaFieldInt(doc, "blockNumber"), solanaStartSlot; got != want {
			t.Errorf("instruction doc blockNumber mismatch: got %d want %d", got, want)
		}
		wantData := solanaOuterData
		if hasStackHeight(doc) {
			wantData = solanaInnerData
		}
		if got, want := solanaString(doc["data"]), wantData; got != want {
			t.Errorf("instruction data mismatch: got %s want %s", got, want)
		}
	}

	// The hot slot still signed: the duplicate docs did not break the
	// completeness guard of the signing pipeline.
	sigs := solanaQueryRows(t, solana.DefaultCollectionBlockSignature,
		fmt.Sprintf("(filter: {blockNumber: {_eq: %d}})", solanaStartSlot),
		"signatureValue")
	if len(sigs) != 1 || solanaString(sigs[0]["signatureValue"]) == "" {
		t.Errorf("hot slot must keep exactly one signed BlockSignature doc, got %#v", sigs)
	}
}

// solanaInt normalizes a decoded GraphQL numeric value to int64. Numbers
// surface as float64 or int64 depending on the query shape, and rarely as
// json.Number from a strict decoder.
func solanaInt(value any) int64 {
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

// solanaString normalizes a decoded GraphQL string value.
func solanaString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

// solanaFieldInt reads a numeric field off a decoded row.
func solanaFieldInt(row map[string]any, field string) int64 {
	return solanaInt(row[field])
}

// hasStackHeight discriminates inner from outer instruction docs by the
// stackHeight key-presence invariant the stamper itself relies on.
func hasStackHeight(row map[string]any) bool {
	value, exists := row["stackHeight"]
	return exists && value != nil
}

// keysOf renders the keys of a map for error output.
func keysOf(m map[string]map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// TestSolanaMockSuite is the single ordered lifecycle test. The subtests
// run strictly in order and share package state.
func TestSolanaMockSuite(t *testing.T) {
	t.Run("startup", suitePhaseStartup)
	t.Run("field assertions", suitePhaseFieldAssertions)
	t.Run("skipped-slot advancement", suitePhaseSkipAdvancement)
	t.Run("duplicate-content instructions", suitePhaseDuplicateDocs)
	t.Run("restart", suitePhaseRestart)
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
