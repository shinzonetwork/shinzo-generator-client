package snapshot

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defra"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/defracontext"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/testutils"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/client"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Test collection names derived from the default prefix
	// testBlockCollection is a constant for block collections.
	testBlockCollection = evm.DefaultCollectionPrefix + "__Block"
	// testTransactionCollection is a constant for transaction collections.
	testTransactionCollection = evm.DefaultCollectionPrefix + "__Transaction"
	// testBlockSignatureCollection is a constant for blockSignature collections.
	testBlockSignatureCollection = evm.DefaultCollectionPrefix + "__BlockSignature"
	// testSnapshotSignatureCollection is a constant for snapshotSignature collections.
	testSnapshotSignatureCollection = evm.DefaultCollectionPrefix + "__SnapshotSignature"
)

func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// newTestChainFromNode returns a chains.Converter for the given test node.
func newTestChainFromNode(t *testing.T, _ *testutils.TestDefraDB) chains.Converter {
	t.Helper()
	return evm.NewConverter(nil)
}

func newTestSnapshotter(t *testing.T) (*Snapshotter, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.SnapshotConfig{Dir: dir, BlocksPerFile: 1000, IntervalSeconds: 60}
	s := New(cfg, nil, nil)
	return s, dir
}

// newTestSnapshotterWithDB sets up a DefraDB test node, optionally inserts the
// inclusive block range [start, end] (skipped when start > end), and returns a
// Snapshotter over a fresh temp snapshot directory (accessible via s.cfg.Dir).
func newTestSnapshotterWithDB(t *testing.T, blocksPerFile, start, end int64) (*Snapshotter, *testutils.TestDefraDB, context.Context) {
	t.Helper()
	td := testutils.SetupTestDefraDB(t)
	if start <= end {
		insertTestBlocks(t, td, start, end)
	}

	cfg := &config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: blocksPerFile}
	s := New(cfg, td.Node, newTestChainFromNode(t, td))
	ctx := context.Background()
	s.ctx = ctx
	return s, td, ctx
}

func writeJSONLFile(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	err := os.WriteFile(filepath.Clean(p), []byte(content), 0o600)
	require.NoError(t, err)
	return p
}

func writeGzipJSONLFile(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(filepath.Clean(p))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	gw := gzip.NewWriter(f)
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	_, err = gw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return p
}

func hexRoot(data string) string {
	return hex.EncodeToString([]byte(data))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}

// readSnapshotHeader opens a .kvsnap.gz file and decodes its length-prefixed JSON header.
func readSnapshotHeader(t *testing.T, path string) kvSnapshotHeader {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	gr, err := gzip.NewReader(f)
	require.NoError(t, err)
	defer func() { _ = gr.Close() }()

	// Read header length (4 bytes, big-endian)
	var lenBuf [4]byte
	_, err = io.ReadFull(gr, lenBuf[:])
	require.NoError(t, err)
	headerLen := binary.BigEndian.Uint32(lenBuf[:])

	headerBytes := make([]byte, headerLen)
	_, err = io.ReadFull(gr, headerBytes)
	require.NoError(t, err)

	var header kvSnapshotHeader
	require.NoError(t, json.Unmarshal(headerBytes, &header))
	return header
}

// writeKVSnapHeader writes the length-prefixed JSON header of a .kvsnap.gz file.
func writeKVSnapHeader(gw *gzip.Writer, header kvSnapshotHeader) error {
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return err
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(headerBytes))) //nolint:gosec
	if _, err := gw.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = gw.Write(headerBytes)
	return err
}

// writeKVSnapEOF writes the EOF marker (key_len = 0) of a .kvsnap.gz file.
func writeKVSnapEOF(gw *gzip.Writer) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], 0)
	_, err := gw.Write(lenBuf[:])
	return err
}

// writeKVSnapGz is a helper that creates a .kvsnap.gz file with raw gzipped bytes.
func writeKVSnapGz(t *testing.T, dir, name string, writeContent func(gw *gzip.Writer)) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(filepath.Clean(p))
	require.NoError(t, err)

	gw := gzip.NewWriter(f)
	writeContent(gw)
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())
	return p
}

// deterministicHash generates a valid 66-char hex hash from a seed string.
func deterministicHash(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return "0x" + hex.EncodeToString(h[:])
}

// testBlock creates a *evm.Block with a hex-encoded block number.
func testBlock(hexNumber string) *evm.Block {
	return &evm.Block{
		Hash:             deterministicHash("block-" + hexNumber),
		Number:           hexNumber,
		Timestamp:        "1640995200",
		ParentHash:       "0x0000000000000000000000000000000000000000000000000000000000000000",
		Difficulty:       "1000000",
		TotalDifficulty:  "1000000",
		GasUsed:          "21000",
		GasLimit:         "8000000",
		BaseFeePerGas:    "",
		Nonce:            "0x0",
		Miner:            "0x0000000000000000000000000000000000000001",
		Size:             "1024",
		StateRoot:        "0x0000000000000000000000000000000000000000000000000000000000000001",
		Sha3Uncles:       "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
		TransactionsRoot: "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		ReceiptsRoot:     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		LogsBloom:        "0x00",
		ExtraData:        "0x",
		MixHash:          "0x0000000000000000000000000000000000000000000000000000000000000000",
	}
}

// testTransaction creates a *evm.Transaction with a deterministic hash.
func testTransaction(seed, blockNumber string) *evm.Transaction {
	return &evm.Transaction{
		Hash:              deterministicHash("tx-" + seed),
		BlockHash:         "0x0000000000000000000000000000000000000000000000000000000000000001",
		BlockNumber:       blockNumber,
		From:              "0x0000000000000000000000000000000000000001",
		To:                "0x0000000000000000000000000000000000000002",
		Value:             "1000000000000000000",
		Gas:               "21000",
		GasPrice:          "20000000000",
		Input:             "0x",
		Nonce:             "1",
		TransactionIndex:  0,
		Type:              "0",
		ChainID:           "1",
		V:                 "27",
		R:                 "0x0000000000000000000000000000000000000000000000000000000000000001",
		S:                 "0x0000000000000000000000000000000000000000000000000000000000000001",
		Status:            true,
		CumulativeGasUsed: "21000",
		EffectiveGasPrice: "20000000000",
	}
}

// testReceipt creates a *evm.TransactionReceipt with one log.
func testReceipt(txSeed, blockNumberHex string) *evm.TransactionReceipt {
	txHash := deterministicHash("tx-" + txSeed)
	return &evm.TransactionReceipt{
		TransactionHash:   txHash,
		TransactionIndex:  "0",
		BlockHash:         "0x0000000000000000000000000000000000000000000000000000000000000001",
		BlockNumber:       blockNumberHex,
		From:              "0x0000000000000000000000000000000000000001",
		To:                "0x0000000000000000000000000000000000000002",
		CumulativeGasUsed: "21000",
		GasUsed:           "21000",
		Status:            "0x1",
		Logs: []evm.Log{
			{
				Address:          "0x0000000000000000000000000000000000000003",
				Topics:           []string{"0x0000000000000000000000000000000000000000000000000000000000000001"},
				Data:             "0x00",
				BlockNumber:      blockNumberHex,
				TransactionHash:  txHash,
				TransactionIndex: 0,
				BlockHash:        "0x0000000000000000000000000000000000000000000000000000000000000001",
				LogIndex:         0,
				Removed:          false,
			},
		},
	}
}

// insertTestBlocks inserts a range of blocks into DefraDB using the block handler.
// Each block gets one transaction and one receipt (with one log).
// Returns the block handler for further use.
func insertTestBlocks(t *testing.T, td *testutils.TestDefraDB, startBlock, endBlock int64) *defra.BlockHandler {
	t.Helper()
	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	ctx := context.Background()
	for i := startBlock; i <= endBlock; i++ {
		hexNum := fmt.Sprintf("0x%x", i)
		decNum := fmt.Sprintf("%d", i)
		block := testBlock(hexNum)
		tx := testTransaction(fmt.Sprintf("block%d_tx0", i), decNum)
		receipt := testReceipt(fmt.Sprintf("block%d_tx0", i), hexNum)
		result, _ := evm.NewConverter(nil).Convert(context.Background(), &evm.BlockBundle{
			Block:        block,
			Transactions: []*evm.Transaction{tx},
			Receipts:     []*evm.TransactionReceipt{receipt},
		})
		_, err = handler.Store(ctx, result)
		require.NoError(t, err, "failed to insert block %d", i)
	}
	return handler
}

// insertTestBlocksWithIdentity inserts blocks with a signing identity context.
// This creates actual BlockSignature documents through the normal code path.
func insertTestBlocksWithIdentity(t *testing.T, td *testutils.TestDefraDB, startBlock, endBlock int64) (context.Context, *defra.BlockHandler) {
	t.Helper()
	handler, err := defra.NewBlockHandler(td.Node, 1000)
	require.NoError(t, err)

	fullIdent, err := identity.Generate(crypto.KeyTypeSecp256k1)
	require.NoError(t, err)
	ctx := defracontext.WithIdentity(context.Background(), fullIdent)

	for i := startBlock; i <= endBlock; i++ {
		hexNum := fmt.Sprintf("0x%x", i)
		decNum := fmt.Sprintf("%d", i)
		block := testBlock(hexNum)
		tx := testTransaction(fmt.Sprintf("block%d_tx0", i), decNum)
		receipt := testReceipt(fmt.Sprintf("block%d_tx0", i), hexNum)
		result, _ := evm.NewConverter(nil).Convert(context.Background(), &evm.BlockBundle{
			Block:        block,
			Transactions: []*evm.Transaction{tx},
			Receipts:     []*evm.TransactionReceipt{receipt},
		})
		_, err = handler.Store(ctx, result)
		require.NoError(t, err, "failed to insert block %d", i)
	}
	return ctx, handler
}

// insertBlockSignature inserts a BlockSignature doc directly via an explicit txn.
func insertBlockSignature(t *testing.T, td *testutils.TestDefraDB, blockNumber int64, merkleRoot string) {
	t.Helper()
	ctx := context.Background()

	txn, err := td.Node.DB.NewTxn(false)
	require.NoError(t, err)

	col, err := txn.GetCollectionByName(ctx, testBlockSignatureCollection)
	require.NoError(t, err)

	data := map[string]any{
		constants.BlockNumberKeyValue: blockNumber,
		constants.BlockHashKeyValue:   deterministicHash(fmt.Sprintf("block-%d", blockNumber)),
		constants.MerkleRootKeyValue:  merkleRoot,
		"cidCount":                    5,
		"cids":                        []string{"cidA", "cidB"},
	}

	doc, err := client.NewDocFromMap(ctx, data, col.Version())
	require.NoError(t, err)

	err = col.AddDocument(ctx, doc)
	require.NoError(t, err)

	err = txn.Commit()
	require.NoError(t, err)
}

// newIdentityCtx generates a full identity of the given key type and returns a
// context that carries it.
func newIdentityCtx(t *testing.T, keyType crypto.KeyType) (context.Context, identity.FullIdentity) {
	t.Helper()
	fullIdent, err := identity.Generate(keyType)
	require.NoError(t, err)
	return defracontext.WithIdentity(context.Background(), fullIdent), fullIdent
}

func signMerkleRootErr(ctx context.Context, merkleRoot []byte) error {
	_, _, _, err := signMerkleRoot(ctx, merkleRoot) // nolint: dogsled
	return err
}

// newVerifyFixture writes a one-block_signature JSONL snapshot plus a signature
// over its computed merkle root. The returned sig is fully valid for
// VerifySnapshotWithSig; tests mutate individual fields per scenario.
func newVerifyFixture(t *testing.T, keyType crypto.KeyType) (string, *SnapshotSignatureData) {
	t.Helper()
	fullIdent, err := identity.Generate(keyType)
	require.NoError(t, err)

	rootData := bytes.Repeat([]byte{0xBB}, 32)
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, t.TempDir(), "test.jsonl", lines)

	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootData})

	sigValue, err := fullIdent.PrivateKey().Sign(computedRoot)
	require.NoError(t, err)

	sigType := constants.Ed25519ValueString
	if keyType == crypto.KeyTypeSecp256k1 {
		sigType = constants.Secp256k1ValueString
	}

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        hex.EncodeToString(computedRoot),
		BlockCount:        1,
		SignatureType:     sigType,
		SignatureIdentity: fullIdent.PublicKey().String(),
		SignatureValue:    hex.EncodeToString(sigValue),
	}
	return p, sig
}

// chmodReadOnly makes dir read-only and restores the original mode on cleanup.
func chmodReadOnly(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, 0o555)) //nolint:gosec
	t.Cleanup(func() {
		os.Chmod(dir, 0o755) //nolint:gosec,errcheck
	})
}

// assertBlockRange builds a lightweight Snapshotter over td and asserts that the
// converter reports exactly the given stored block range.
func assertBlockRange(t *testing.T, td *testutils.TestDefraDB, lowest, highest int64) {
	t.Helper()
	s2 := New(&config.SnapshotConfig{Dir: t.TempDir(), BlocksPerFile: 1000}, td.Node, newTestChainFromNode(t, td))
	ctx := context.Background()

	gotLowest, err := s2.converter.GetLowestStoredBlockNumber(ctx, s2.defraNode)
	require.NoError(t, err)
	assert.Equal(t, lowest, gotLowest)

	gotHighest, err := s2.converter.GetHighestStoredBlockNumber(ctx, s2.defraNode)
	require.NoError(t, err)
	assert.Equal(t, highest, gotHighest)
}

// seedSnapshotFiles writes count well-formed (empty) snapshot files covering
// sequential 100-block aligned ranges starting at start. Returns the names.
func seedSnapshotFiles(t *testing.T, dir string, start, count int64) []string {
	t.Helper()
	names := make([]string, 0, count)
	for i := range count {
		s := start + i*100
		name := fmt.Sprintf("snapshot_%d_%d.kvsnap.gz", s, s+99)
		writeKVSnapGz(t, dir, name, func(_ *gzip.Writer) {})
		names = append(names, name)
	}
	return names
}

// countSnapshotFiles counts on-disk snapshot files (well-formed or not).
func countSnapshotFiles(t *testing.T, dir string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "snapshot_*.kvsnap.gz"))
	require.NoError(t, err)
	return len(files)
}
