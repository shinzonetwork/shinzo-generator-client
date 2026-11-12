//go:build integration
// +build integration

package bitcoin

// The ordered Bitcoin lifecycle suite: a full indexer StartIndexing run
// against a scripted Bitcoin Core fake, exercising startup field integrity,
// the one-block fork flip, and a stop/extend/restart resume, all against
// one embedded DefraDB store.
//
// The three subtests deliberately share one production wiring (fetcher,
// converter, block handler, processor, pruner/snapshotter seam) so ordering
// failures surface: the fork flip runs while the same indexer is parked at
// the tip, and the restart leg reopens the very store the first run wrote.

import (
	"fmt"
	"testing"
	"time"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	btc "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/bitcoin"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBitcoinIndexerLifecycle(t *testing.T) {
	chain := newScriptedChain(104)
	server := newCoreRPCServer(chain.respond)
	t.Cleanup(server.Close)

	storeDir := t.TempDir()
	cfg := btcIndexerConfig(server.URL, storeDir, config.IndexerConfig{
		StartHeight:      100,
		ConcurrentBlocks: 1,
		MaxDocsPerTxn:    100,
		HealthServerPort: 0,
		StartBuffer:      100,
	})
	ix, err := indexer.CreateIndexer(cfg)
	require.NoError(t, err)
	errCh := startIndexingBackground(t, ix)
	t.Cleanup(ix.StopIndexing)

	t.Run("startup and field assertions", func(t *testing.T) {
		waitForBlocks(t, ix, errCh, 104, 90*time.Second)
		port := ix.GetDefraDBPort()
		require.Greater(t, port, 0)

		assertStoredRange(t, port, 100, 104)
		assertHotBlockFields(t, port, 104)
		assertBlockSignaturesStored(t, port, 100, 104)
	})

	t.Run("fork flip", func(t *testing.T) {
		// The arm overshoots the reorg height by exactly one: the episode's
		// confirmation waits for a fetched block whose parent equals the
		// parked mismatch height's hash, so the canonical successor above
		// the reorged height must exist on the scripted tip, or the parked
		// waiter would never see one.
		chain.armFork(105, 107)
		waitForBlocks(t, ix, errCh, 107, 90*time.Second)
		port := ix.GetDefraDBPort()
		require.Greater(t, port, 0)

		assertCanonicalHeightStored(t, port, 105)
		assertOrphanPurged(t, port, 105)
		assertStoredRange(t, port, 100, 107)
		assertBlockSignaturesStored(t, port, 105, 107)
	})

	t.Run("restart and resume", func(t *testing.T) {
		port := ix.GetDefraDBPort()
		require.Greater(t, port, 0)
		before := storedDocsByHeight(t, port, 100, 107)

		ix.StopIndexing()

		chain.setTip(109)
		marker := server.callMarker()
		resumeCfg := btcIndexerConfig(server.URL, storeDir, config.IndexerConfig{
			StartHeight:      0,
			ConcurrentBlocks: 1,
			MaxDocsPerTxn:    100,
			HealthServerPort: 0,
			StartBuffer:      100,
		})
		ix2, err := indexer.CreateIndexer(resumeCfg)
		require.NoError(t, err)
		errCh2 := startIndexingBackground(t, ix2)
		t.Cleanup(ix2.StopIndexing)

		waitForBlocks(t, ix2, errCh2, 109, 90*time.Second)
		resumePort := ix2.GetDefraDBPort()
		require.Greater(t, resumePort, 0)

		// The store tip before the stop sat at 107, so resume lands at 108
		// once the scripted chain advanced by two heights, a gap smaller
		// than StartBuffer.
		require.Equal(t, before, storedDocsByHeight(t, resumePort, 100, 107),
			"pre-stop heights must keep their hashes and content addresses")
		assertCanonicalHeightStored(t, resumePort, 108)
		assertCanonicalHeightStored(t, resumePort, 109)
		assertBlockSignaturesStored(t, resumePort, 108, 109)
		assertNoBlockHashRequestsBelow(t, server, marker, 108)
	})
}

// assertStoredRange pins the whole canonical run: every height in the range
// holds exactly one block row whose hash and parent bridge match the seeds.
func assertStoredRange(t *testing.T, port int, lo, hi int64) {
	t.Helper()
	rows := collectionRows(t, port, btc.DefaultCollectionBlock, rangeFilter("number", lo, hi),
		"number hash parentHash")
	require.Len(t, rows, int(hi-lo+1))
	for i, row := range rows {
		height := lo + int64(i)
		assert.Equal(t, expectedBlockHash(seedSectionCanonical, height), row["hash"],
			"block %d hash", height)
		assert.Equal(t, expectedBlockHash(seedSectionCanonical, height-1), row["parentHash"],
			"block %d parent bridge", height)
	}
}

// assertCanonicalHeightStored pins one post-reorg height: exactly one row,
// with the canonical branch's hash.
func assertCanonicalHeightStored(t *testing.T, port int, height int64) {
	t.Helper()
	rows := collectionRows(t, port, btc.DefaultCollectionBlock, heightFilter("number", height),
		"number hash parentHash")
	require.Len(t, rows, 1)
	assert.Equal(t, expectedBlockHash(seedSectionCanonical, height), rows[0]["hash"],
		"height %d must hold the canonical branch", height)
}

// assertHotBlockFields pins the hot block's whole document tree against the
// seeds the scripted fake derives its wire identity from: header payloads,
// both transaction shapes, coinbase and regular inputs, outputs, and the
// key-omission contract of every nullable field.
func assertHotBlockFields(t *testing.T, port int, height int64) {
	t.Helper()
	blockHash := expectedBlockHash(seedSectionCanonical, height)

	blockRows := collectionRows(t, port, btc.DefaultCollectionBlock, heightFilter("number", height),
		"number hash merkleRoot time version bits nonce chainwork")
	require.Len(t, blockRows, 1)
	head := blockRows[0]
	assert.Equal(t, blockHash, head["hash"])
	assert.Equal(t, expectedMerkleRoot(seedSectionCanonical, height), head["merkleRoot"])
	assert.Equal(t, float64(expectedBlockTime(height)), head["time"])
	assert.Equal(t, float64(2), head["version"])
	assert.Equal(t, blockBits, head["bits"])
	assert.Equal(t, expectedNonceDoc(height), head["nonce"])
	assert.Equal(t, expectedChainwork(height), head["chainwork"])

	txRows := collectionRows(t, port, btc.DefaultCollectionTransaction, heightFilter("blockNumber", height),
		"txid hash blockHash blockNumber txIndex version locktime isCoinbase feeSat")
	require.Len(t, txRows, 2)
	coinbaseRow := findRowBy(t, txRows, "txIndex", float64(0))
	assert.Equal(t, expectedCoinbaseTxID(seedSectionCanonical, height), coinbaseRow["txid"])
	assert.Equal(t, expectedCoinbaseWtxID(seedSectionCanonical, height), coinbaseRow["hash"])
	assert.Equal(t, blockHash, coinbaseRow["blockHash"])
	assert.Equal(t, true, coinbaseRow["isCoinbase"])
	assert.Equal(t, float64(1), coinbaseRow["version"])
	assert.Equal(t, float64(0), coinbaseRow["locktime"])
	assert.Nil(t, coinbaseRow["feeSat"], "the coinbase tx must carry no fee, not a zero one")

	witnessRow := findRowBy(t, txRows, "txIndex", float64(1))
	assert.Equal(t, expectedWitnessTxID(seedSectionCanonical, height), witnessRow["txid"])
	assert.Equal(t, expectedWitnessWtxID(seedSectionCanonical, height), witnessRow["hash"],
		"the witness tx's wtxid must differ from its txid")
	assert.Equal(t, false, witnessRow["isCoinbase"])
	assert.Equal(t, float64(witnessFeeSat), witnessRow["feeSat"])
	assert.Equal(t, float64(2), witnessRow["version"])

	inputRows := collectionRows(t, port, btc.DefaultCollectionTxInput, heightFilter("blockNumber", height),
		"txid blockNumber inputIndex sequence coinbase prevTxid prevVout scriptSigHex witness prevoutValueSat prevoutAddress prevoutType")
	require.Len(t, inputRows, 2)
	// Each transaction in the block carries exactly one input, so both rows
	// share inputIndex 0 and the branches differ by coinbase presence.
	coinbaseInput := findRowBy(t, inputRows, "coinbase", expectedCoinbaseScript(height))
	assert.Equal(t, float64(0), coinbaseInput["inputIndex"])
	assert.Nil(t, coinbaseInput["prevTxid"], "the coinbase input carries no outpoint")
	assert.Nil(t, coinbaseInput["prevoutValueSat"], "the coinbase input carries no prevout")

	witnessInput := findRowBy(t, inputRows, "coinbase", nil)
	assert.Equal(t, float64(0), witnessInput["inputIndex"])
	assert.Equal(t, expectedWitnessTxID(seedSectionCanonical, height), witnessInput["txid"])
	assert.Equal(t, expectedWitnessPrevTxID(seedSectionCanonical, height), witnessInput["prevTxid"])
	assert.Equal(t, float64(0), witnessInput["prevVout"])
	assert.Equal(t, scriptSigStdHex, witnessInput["scriptSigHex"])
	assert.Equal(t, float64(sequenceMax), witnessInput["sequence"])
	witness, ok := witnessInput["witness"].([]any)
	require.True(t, ok, "witness must be an array: %#v", witnessInput["witness"])
	require.Len(t, witness, 2)
	assert.Equal(t, expectedWitnessEntry(seedSectionCanonical, height, 0), witness[0])
	assert.Equal(t, expectedWitnessEntry(seedSectionCanonical, height, 1), witness[1])
	assert.Equal(t, float64(prevoutValueSat), witnessInput["prevoutValueSat"])
	assert.Equal(t, witnessAddr, witnessInput["prevoutAddress"])
	assert.Equal(t, witnessScriptType, witnessInput["prevoutType"])

	outputRows := collectionRows(t, port, btc.DefaultCollectionTxOutput, heightFilter("blockNumber", height),
		"txid blockNumber n valueSat scriptPubKeyHex scriptPubKeyAddress scriptPubKeyType")
	require.Len(t, outputRows, 2)
	coinbaseOutput := findRowBy(t, outputRows, "txid", expectedCoinbaseTxID(seedSectionCanonical, height))
	assert.Equal(t, float64(0), coinbaseOutput["n"])
	assert.Equal(t, float64(coinbaseRewardSat), coinbaseOutput["valueSat"])
	assert.Equal(t, scriptPubKeyStdHex, coinbaseOutput["scriptPubKeyHex"])
	assert.Equal(t, pubKeyHashType, coinbaseOutput["scriptPubKeyType"])
	assert.Nil(t, coinbaseOutput["scriptPubKeyAddress"],
		"the coinbase output carries no address instead of an empty one")

	witnessOutput := findRowBy(t, outputRows, "txid", expectedWitnessTxID(seedSectionCanonical, height))
	assert.Equal(t, float64(0), witnessOutput["n"])
	assert.Equal(t, float64(witnessOutputSat), witnessOutput["valueSat"])
	assert.Equal(t, witnessAddr, witnessOutput["scriptPubKeyAddress"])
}

// assertBlockSignaturesStored pins the signature contract: one signed
// BlockSignature document per stored height, over the seven docs the block
// produced (block, both transactions, both inputs, both outputs).
func assertBlockSignaturesStored(t *testing.T, port int, lo, hi int64) {
	t.Helper()
	rows := collectionRows(t, port, btc.DefaultCollectionBlockSignature, rangeFilter("blockNumber", lo, hi),
		"blockNumber blockHash merkleRoot cidCount cids signatureType signatureIdentity signatureValue createdAt")
	require.Len(t, rows, int(hi-lo+1))
	for _, row := range rows {
		height := rowNumber(t, row, "blockNumber")
		assert.Equal(t, expectedBlockHash(seedSectionCanonical, height), row["blockHash"],
			"signature %d block hash", height)
		assert.Equal(t, float64(7), row["cidCount"], "signature %d cid count", height)
		cids, ok := row["cids"].([]any)
		require.True(t, ok, "cids must be an array: %#v", row["cids"])
		assert.Len(t, cids, 7)
		assert.NotEmpty(t, row["merkleRoot"], "signature %d merkle root", height)
		assert.NotEmpty(t, row["signatureIdentity"], "signature %d identity", height)
		assert.NotEmpty(t, row["signatureValue"], "signature %d value", height)
		assert.Equal(t, "ES256K", row["signatureType"], "signature %d type", height)
		assert.NotEmpty(t, row["createdAt"], "signature %d createdAt", height)
	}
}

// assertOrphanPurged pins the rollback outcome: the orphan branch's rows are
// gone (deleted dependents-first, with neither signatures nor a resurrection
// of the orphan txs) while the canonical re-index left exactly one set.
func assertOrphanPurged(t *testing.T, port int, height int64) {
	t.Helper()
	orphanTxIDs := []string{
		expectedCoinbaseTxID(seedSectionFork, height),
		expectedWitnessTxID(seedSectionFork, height),
	}

	txRows := collectionRows(t, port, btc.DefaultCollectionTransaction, heightFilter("blockNumber", height),
		"txid txIndex isCoinbase")
	require.Len(t, txRows, 2, "the canonical txs must be re-included exactly once")
	for _, row := range txRows {
		assert.NotContains(t, orphanTxIDs, row["txid"], "the orphan branch's txs must be deleted")
	}

	assertCanonicalHeightStored(t, port, height)
	assertBlockSignaturesStoredRow(t, port, height)
}

// assertBlockSignaturesStoredRow is the single-height form of the signature
// contract check, used where a range would over-constrain.
func assertBlockSignaturesStoredRow(t *testing.T, port int, height int64) {
	t.Helper()
	rows := collectionRows(t, port, btc.DefaultCollectionBlockSignature, heightFilter("blockNumber", height),
		"blockNumber blockHash merkleRoot cids signatureType signatureValue")
	require.Len(t, rows, 1, "the re-stored height must carry exactly one BlockSignature doc")
	assert.Equal(t, expectedBlockHash(seedSectionCanonical, height), rows[0]["blockHash"])
}

// recordedDoc is the identity of one stored document used by the
// restart/resume comparison: its content address for every collection, plus
// the hash field on the block doc itself.
type recordedDoc struct {
	number int64
	hash   string
}

// storedDocsByHeight reads every content-bearing collection for the range
// and returns them keyed collection then docID, the shape the restart leg
// compares before and after the stop.
func storedDocsByHeight(t *testing.T, port int, lo, hi int64) map[string]map[string]recordedDoc {
	t.Helper()
	type collectionSpec struct {
		name   string
		field  string
		hashes bool
	}
	specs := []collectionSpec{
		{btc.DefaultCollectionBlock, "number", true},
		{btc.DefaultCollectionTransaction, "blockNumber", false},
		{btc.DefaultCollectionTxInput, "blockNumber", false},
		{btc.DefaultCollectionTxOutput, "blockNumber", false},
		{btc.DefaultCollectionBlockSignature, "blockNumber", false},
	}

	result := make(map[string]map[string]recordedDoc)
	for _, spec := range specs {
		fields := "_docID " + spec.field
		if spec.hashes {
			fields += " hash"
		}
		rows := collectionRows(t, port, spec.name, rangeFilter(spec.field, lo, hi), fields)
		docs := make(map[string]recordedDoc, len(rows))
		for _, row := range rows {
			docID, ok := row["_docID"].(string)
			require.True(t, ok, "row missing _docID: %#v", row)
			doc := recordedDoc{number: rowNumber(t, row, spec.field)}
			if spec.hashes {
				doc.hash, ok = row["hash"].(string)
				require.True(t, ok)
			}
			docs[docID] = doc
		}
		result[spec.name] = docs
	}
	return result
}

// assertNoBlockHashRequestsBelow pins the resume behavior on the recorded
// wire: after the marker, no getblockhash ask ever dips below the resume
// height, so the restart never re-requested a height it already owned.
func assertNoBlockHashRequestsBelow(t *testing.T, server *coreRPCServer, marker int, minHeight int64) {
	t.Helper()
	for _, call := range server.callsSince(marker) {
		if call.Method != methodBlockHash {
			continue
		}
		params, err := decodeParams(call)
		require.NoError(t, err, "params %s", call.Params)
		height, ok := paramInt(params)
		require.True(t, ok, "getblockhash params %s", call.Params)
		assert.GreaterOrEqual(t, height, minHeight,
			"getblockhash requested height %d after the restart marker", height)
	}
}

// findRowBy locates the one row whose field holds want and asserts
// uniqueness.
func findRowBy(t *testing.T, rows []map[string]any, field string, want any) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, row := range rows {
		if row[field] == want {
			found = append(found, row)
		}
	}
	require.Len(t, found, 1, "expected exactly one row with %s == %v", field, want)
	return found[0]
}

// rangeFilter builds the GraphQL filter clause for an inclusive height
// window, ordered by the window field.
func rangeFilter(field string, lo, hi int64) string {
	return fmt.Sprintf(`(filter: {%s: {_geq: %d, _leq: %d}}, order: {%s: ASC})`, field, lo, hi, field)
}

// heightFilter builds the GraphQL filter clause for one height.
func heightFilter(field string, height int64) string {
	return fmt.Sprintf(`(filter: {%s: {_eq: %d}})`, field, height)
}
