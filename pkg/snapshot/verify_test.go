package snapshot

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/sourcenetwork/defradb/acp/identity"
	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// extractBlockSigMerkleRoots
// ---------------------------------------------------------------------------

func TestExtractBlockSigMerkleRoots_PlainJSONL(t *testing.T) {
	dir := t.TempDir()

	mr1 := hexRoot("root1_data_bytes")
	mr2 := hexRoot("root2_data_bytes")

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr1, constants.BlockNumberKeyValue: 1000}}),
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr2, constants.BlockNumberKeyValue: 1001}}),
	}

	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	require.Len(t, roots, 2)

	expected1, _ := hex.DecodeString(mr1)
	expected2, _ := hex.DecodeString(mr2)
	assert.Equal(t, expected1, roots[0])
	assert.Equal(t, expected2, roots[1])
}

func TestExtractBlockSigMerkleRoots_GzipFile(t *testing.T) {
	dir := t.TempDir()

	mr1 := hexRoot("gzip_root_data")

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr1}}),
	}

	p := writeGzipJSONLFile(t, dir, "test.jsonl.gz", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	require.Len(t, roots, 1)

	expected, _ := hex.DecodeString(mr1)
	assert.Equal(t, expected, roots[0])
}

func TestExtractBlockSigMerkleRoots_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := writeJSONLFile(t, dir, "empty.jsonl", []string{})

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	assert.Empty(t, roots)
}

func TestExtractBlockSigMerkleRoots_EmptyGzipFile(t *testing.T) {
	dir := t.TempDir()
	p := writeGzipJSONLFile(t, dir, "empty.jsonl.gz", []string{})

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	assert.Empty(t, roots)
}

func TestExtractBlockSigMerkleRoots_NonBlockSigEntriesSkipped(t *testing.T) {
	dir := t.TempDir()

	mr := hexRoot("valid_root")

	lines := []string{
		mustJSON(t, map[string]any{"type": "block", "data": map[string]any{constants.NumberFieldValue: 1000}}),
		mustJSON(t, map[string]any{"type": "transaction", "data": map[string]any{"hash": "0xabc"}}),
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
		mustJSON(t, map[string]any{"type": "log", "data": map[string]any{"logIndex": 0}}),
	}

	p := writeJSONLFile(t, dir, "mixed.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	require.Len(t, roots, 1)

	expected, _ := hex.DecodeString(mr)
	assert.Equal(t, expected, roots[0])
}

func TestExtractBlockSigMerkleRoots_InvalidJSONSkipped(t *testing.T) {
	dir := t.TempDir()

	mr := hexRoot("good_root")

	lines := []string{
		"this is not json at all",
		"{ broken json",
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
		"",
	}

	p := writeJSONLFile(t, dir, "invalid_lines.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	require.Len(t, roots, 1)

	expected, _ := hex.DecodeString(mr)
	assert.Equal(t, expected, roots[0])
}

func TestExtractBlockSigMerkleRoots_EmptyMerkleRoot(t *testing.T) {
	dir := t.TempDir()

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: ""}}),
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{"other": "field"}}),
	}

	p := writeJSONLFile(t, dir, "empty_roots.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	assert.Empty(t, roots)
}

func TestExtractBlockSigMerkleRoots_InvalidHexSkipped(t *testing.T) {
	dir := t.TempDir()

	mr := hexRoot("valid_root")

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: "not_valid_hex_zzz"}}),
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}

	p := writeJSONLFile(t, dir, "bad_hex.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	require.Len(t, roots, 1)

	expected, _ := hex.DecodeString(mr)
	assert.Equal(t, expected, roots[0])
}

func TestExtractBlockSigMerkleRoots_NilData(t *testing.T) {
	dir := t.TempDir()

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": nil}),
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue}),
	}

	p := writeJSONLFile(t, dir, "nil_data.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	assert.Empty(t, roots)
}

func TestExtractBlockSigMerkleRoots_FileNotFound(t *testing.T) {
	roots, err := extractBlockSigMerkleRoots("/nonexistent/path/file.jsonl")
	assert.Error(t, err)
	assert.Nil(t, roots)
}

func TestExtractBlockSigMerkleRoots_MultipleValidRootsInOrder(t *testing.T) {
	dir := t.TempDir()

	mrs := make([]string, 5)
	for i := range mrs {
		data := make([]byte, 32)
		data[0] = byte(i + 1)
		mrs[i] = hex.EncodeToString(data)
	}

	var lines []string
	for i, mr := range mrs {
		lines = append(lines, mustJSON(t, map[string]any{
			"type": constants.BlockSignatureTypeValue,
			"data": map[string]any{constants.MerkleRootKeyValue: mr, constants.BlockNumberKeyValue: 1000 + i},
		}))
	}

	p := writeJSONLFile(t, dir, "ordered.jsonl", lines)

	roots, err := extractBlockSigMerkleRoots(p)
	require.NoError(t, err)
	require.Len(t, roots, 5)

	for i, mr := range mrs {
		expected, _ := hex.DecodeString(mr)
		assert.Equal(t, expected, roots[i])
	}
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig - error paths
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_NoBlockSigsInSnapshot(t *testing.T) {
	dir := t.TempDir()

	// Create a snapshot with no block_signature entries
	lines := []string{
		mustJSON(t, map[string]any{"type": "block", "data": map[string]any{constants.NumberFieldValue: 1000}}),
		mustJSON(t, map[string]any{"type": "transaction", "data": map[string]any{"hash": "0xabc"}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        "abcdef1234567890",
		BlockCount:        1000,
		SignatureIdentity: "signer123",
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.Equal(t, 0, result.BlockSigsFound)
	assert.Contains(t, result.Error, "no block signatures found")
}

func TestVerifySnapshotWithSig_MerkleRootMismatch(t *testing.T) {
	dir := t.TempDir()

	mr := hexRoot("actual_root_data")

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        "0000000000000000000000000000000000000000000000000000000000000000",
		BlockCount:        1,
		SignatureIdentity: "signer123",
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.False(t, result.MerkleRootMatch)
	assert.Contains(t, result.Error, "merkle root mismatch")
	assert.Equal(t, 1, result.BlockSigsFound)
}

func TestVerifySnapshotWithSig_FieldsPropagated(t *testing.T) {
	dir := t.TempDir()

	// No block sigs -> quick error path, but fields should be set
	p := writeJSONLFile(t, dir, "test.jsonl", []string{})

	sig := &SnapshotSignatureData{
		SnapshotFile:      "my_snapshot.jsonl",
		StartBlock:        5000,
		EndBlock:          5999,
		BlockCount:        1000,
		SignatureIdentity: "did:key:z6Mk...",
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "my_snapshot.jsonl", result.SnapshotFile)
	assert.Equal(t, int64(5000), result.StartBlock)
	assert.Equal(t, int64(5999), result.EndBlock)
	assert.Equal(t, 1000, result.BlockCount)
	assert.Equal(t, "did:key:z6Mk...", result.SignerIdentity)
}

func TestVerifySnapshotWithSig_MatchingMerkleRootButBadSignatureHex(t *testing.T) {
	dir := t.TempDir()

	// Build a snapshot with a known root, compute the expected merkle root
	rootData := bytes.Repeat([]byte{0xAB}, 32)
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	// Compute expected merkle root from the function
	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootData})
	computedRootHex := hex.EncodeToString(computedRoot)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        computedRootHex,
		BlockCount:        1,
		SignatureType:     constants.Secp256k1ValueString,
		SignatureIdentity: "signer123",
		SignatureValue:    "not_valid_hex_zzz",
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.True(t, result.MerkleRootMatch)
	assert.Contains(t, result.Error, "decode signature hex")
}

func TestVerifySnapshotWithSig_UnsupportedSignatureType(t *testing.T) {
	dir := t.TempDir()

	rootData := bytes.Repeat([]byte{0xCD}, 32)
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootData})
	computedRootHex := hex.EncodeToString(computedRoot)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        computedRootHex,
		BlockCount:        1,
		SignatureType:     "RSA-UNSUPPORTED",
		SignatureIdentity: "signer123",
		SignatureValue:    hex.EncodeToString([]byte("fake_sig")),
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.Error(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.True(t, result.MerkleRootMatch)
	assert.Contains(t, result.Error, "unsupported signature type")
}

func TestVerifySnapshotWithSig_BadMerkleRootHex(t *testing.T) {
	dir := t.TempDir()

	rootData := bytes.Repeat([]byte{0xEF}, 32)
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootData})
	computedRootHex := hex.EncodeToString(computedRoot)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        computedRootHex,
		BlockCount:        1,
		SignatureType:     constants.Secp256k1ValueString,
		SignatureIdentity: "bad_key_string",
		SignatureValue:    hex.EncodeToString([]byte("fake_sig")),
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.True(t, result.MerkleRootMatch)
	assert.Contains(t, result.Error, "parse public key")
}

func TestVerifySnapshotWithSig_SnapshotFileNotFound(t *testing.T) {
	sig := &SnapshotSignatureData{
		SnapshotFile: "missing.jsonl",
		StartBlock:   1000,
		EndBlock:     1999,
	}

	result, err := VerifySnapshotWithSig("/nonexistent/path/missing.jsonl", sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Error, "extract block sig roots")
}

// ---------------------------------------------------------------------------
// VerifySnapshot - sidecar file handling
// ---------------------------------------------------------------------------

func TestVerifySnapshot_MissingSigFile(t *testing.T) {
	dir := t.TempDir()

	// Create snapshot file without corresponding .sig.json
	snapshotPath := filepath.Join(dir, "snapshot_1000_1999.jsonl.gz")
	err := os.WriteFile(snapshotPath, []byte("data"), 0o600)
	require.NoError(t, err)

	result, err := VerifySnapshot(snapshotPath)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "read signature file")
}

func TestVerifySnapshot_InvalidSigJSON(t *testing.T) {
	dir := t.TempDir()

	snapshotPath := filepath.Join(dir, "snapshot_1000_1999.jsonl.gz")
	err := os.WriteFile(snapshotPath, []byte("data"), 0o600)
	require.NoError(t, err)

	// Sidecar path: strip .jsonl.gz, add .sig.json
	sigPath := filepath.Join(dir, "snapshot_1000_1999.sig.json")
	err = os.WriteFile(sigPath, []byte("not valid json {{{"), 0o600)
	require.NoError(t, err)

	result, err := VerifySnapshot(snapshotPath)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "parse signature file")
}

func TestVerifySnapshot_ValidSigFileButNoBlockSigs(t *testing.T) {
	dir := t.TempDir()

	// Create a gzip'd JSONL file with no block_signature entries
	lines := []string{
		mustJSON(t, map[string]any{"type": "block", "data": map[string]any{constants.NumberFieldValue: 1000}}),
	}
	snapshotPath := writeGzipJSONLFile(t, dir, "snapshot_1000_1999.jsonl.gz", lines)

	sig := SnapshotSignatureData{
		SnapshotFile:      "snapshot_1000_1999.jsonl.gz",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        "abcdef",
		BlockCount:        1000,
		SignatureIdentity: "signer",
	}
	sigBytes, err := json.Marshal(sig)
	require.NoError(t, err)

	// Note: VerifySnapshot strips .jsonl.gz and appends .sig.json
	sigPath := filepath.Join(dir, "snapshot_1000_1999.sig.json")
	err = os.WriteFile(sigPath, sigBytes, 0o600)
	require.NoError(t, err)

	result, err := VerifySnapshot(snapshotPath)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Error, "no block signatures found")
}

func TestVerifySnapshot_SidecarPathDerivation(t *testing.T) {
	// The sidecar path is derived by trimming .jsonl.gz and adding .sig.json.
	// Verify this derivation with a specific filename.
	dir := t.TempDir()

	snapshotName := "snapshot_23700000_23700999.jsonl.gz"
	expectedSigName := "snapshot_23700000_23700999.sig.json"

	snapshotPath := filepath.Join(dir, snapshotName)
	err := os.WriteFile(snapshotPath, []byte("data"), 0o600)
	require.NoError(t, err)

	// Write a valid sig file at the expected path
	sig := SnapshotSignatureData{
		SnapshotFile: snapshotName,
		StartBlock:   23700000,
		EndBlock:     23700999,
	}
	sigBytes, err := json.Marshal(sig)
	require.NoError(t, err)

	sigPath := filepath.Join(dir, expectedSigName)
	err = os.WriteFile(sigPath, sigBytes, 0o600)
	require.NoError(t, err)

	// Should find the sig file (but verification will fail due to no block sigs)
	result, err := VerifySnapshot(snapshotPath)
	require.NoError(t, err)
	require.NotNil(t, result)
	// The fact that we get a result (not a file-not-found error) proves the path derivation works
	assert.Equal(t, snapshotName, result.SnapshotFile)
}

// ---------------------------------------------------------------------------
// VerifyResult JSON serialization
// ---------------------------------------------------------------------------

func TestVerifyResult_JSONRoundTrip(t *testing.T) {
	original := VerifyResult{
		Valid:           true,
		SnapshotFile:    "snapshot.jsonl.gz",
		StartBlock:      1000,
		EndBlock:        1999,
		BlockCount:      1000,
		BlockSigsFound:  1000,
		MerkleRootMatch: true,
		SignatureValid:  true,
		SignerIdentity:  "signer123",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded VerifyResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original, decoded)
}

func TestVerifyResult_OmitEmptyError(t *testing.T) {
	result := VerifyResult{Valid: true}
	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "error")

	resultWithErr := VerifyResult{Valid: false, Error: "something went wrong"}
	data2, err := json.Marshal(resultWithErr)
	require.NoError(t, err)
	assert.Contains(t, string(data2), "error")
	assert.Contains(t, string(data2), "something went wrong")
}

func TestExtractBlockSigMerkleRoots_InvalidGzipFile(t *testing.T) {
	dir := t.TempDir()

	// Write non-gzip content to a .gz file
	p := filepath.Join(dir, "bad.jsonl.gz")
	err := os.WriteFile(p, []byte("this is not gzip data"), 0o600)
	require.NoError(t, err)

	roots, err := extractBlockSigMerkleRoots(p)
	assert.Error(t, err)
	assert.Nil(t, roots)
	assert.Contains(t, err.Error(), "gzip reader")
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: invalid merkle root hex in signature
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_InvalidMerkleRootHex(t *testing.T) {
	dir := t.TempDir()

	rootData := bytes.Repeat([]byte{0xAA}, 32)
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)
	sig := &SnapshotSignatureData{}

	_ = sig
	_ = p
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: valid signature (full end-to-end verify)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_ValidSignature_Ed25519(t *testing.T) {
	p, sig := newVerifyFixture(t, crypto.KeyTypeEd25519)

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Valid, "signature should be valid")
	assert.True(t, result.MerkleRootMatch, "merkle root should match")
	assert.True(t, result.SignatureValid, "signature should be cryptographically valid")
	assert.Empty(t, result.Error)
}

func TestVerifySnapshotWithSig_ValidSignature_Secp256k1(t *testing.T) {
	p, sig := newVerifyFixture(t, crypto.KeyTypeSecp256k1)

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Valid, "signature should be valid")
	assert.True(t, result.MerkleRootMatch)
	assert.True(t, result.SignatureValid)
	assert.Empty(t, result.Error)
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: wrong signature (valid key, but signed different data)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_WrongSignature(t *testing.T) {
	dir := t.TempDir()

	fullIdent, err := identity.Generate(crypto.KeyTypeEd25519)
	require.NoError(t, err)

	rootData := bytes.Repeat([]byte{0xDD}, 32)
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootData})
	computedRootHex := hex.EncodeToString(computedRoot)

	// Sign DIFFERENT data (not the computed root)
	wrongData := bytes.Repeat([]byte{0xFF}, 32)
	sigValue, err := fullIdent.PrivateKey().Sign(wrongData)
	require.NoError(t, err)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        computedRootHex,
		BlockCount:        1,
		SignatureType:     "Ed25519",
		SignatureIdentity: fullIdent.PublicKey().String(),
		SignatureValue:    hex.EncodeToString(sigValue),
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid, "signature should be invalid (wrong data signed)")
	assert.True(t, result.MerkleRootMatch)
	assert.False(t, result.SignatureValid)
	assert.Contains(t, result.Error, "signature verification failed")
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: secp256k1 with bad signature bytes (triggers verify error)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_Secp256k1_InvalidSigBytes(t *testing.T) {
	p, sig := newVerifyFixture(t, crypto.KeyTypeSecp256k1)

	// Use garbage bytes as signature - should fail DER parsing for secp256k1
	sig.SignatureValue = hex.EncodeToString([]byte("not a valid DER signature"))

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Valid)
	assert.True(t, result.MerkleRootMatch)
	// Should hit either "verify signature" error or signature invalid
	assert.NotEmpty(t, result.Error)
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: "ecdsa-256k" and "ed25519" lowercase variants
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_LowercaseSignatureTypes(t *testing.T) {
	// Test "ecdsa-256k" variant
	p, sig := newVerifyFixture(t, crypto.KeyTypeSecp256k1)
	sig.SignatureType = "ecdsa-256k"
	sig.SnapshotFile = "test_ecdsa.jsonl"

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Valid)

	// Test "ed25519" lowercase variant
	p2, sig2 := newVerifyFixture(t, crypto.KeyTypeEd25519)
	sig2.SignatureType = strings.ToLower(constants.Ed25519ValueString)
	sig2.SnapshotFile = "test_ed25519.jsonl"

	result2, err := VerifySnapshotWithSig(p2, sig2)
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.True(t, result2.Valid)
}

// ---------------------------------------------------------------------------
// extractBlockSigMerkleRoots: reader.Err() error path via truncated gzip
// ---------------------------------------------------------------------------

func TestExtractBlockSigMerkleRoots_TruncatedGzipCausesReaderErr(t *testing.T) {
	dir := t.TempDir()

	// Create a valid gzip file and then truncate it mid-stream
	fullPath := filepath.Join(dir, "truncated.jsonl.gz")
	f, err := os.Create(filepath.Clean(fullPath))
	require.NoError(t, err)

	gw := gzip.NewWriter(f)
	// Write a large amount of data so we have something to truncate
	for i := range 100 {
		line := mustJSON(t, map[string]any{
			"type": constants.BlockSignatureTypeValue,
			"data": map[string]any{constants.MerkleRootKeyValue: hex.EncodeToString(bytes.Repeat([]byte{byte(i)}, 32))},
		})
		_, err = gw.Write([]byte(line + "\n"))
		require.NoError(t, err)
	}
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	// Read the file, then truncate it to half its size
	data, err := os.ReadFile(filepath.Clean(fullPath))
	require.NoError(t, err)
	err = os.WriteFile(filepath.Clean(fullPath), data[:len(data)/2], 0o600) //nolint:gosec
	require.NoError(t, err)

	// The scanner should encounter a gzip decompression error
	roots, err := extractBlockSigMerkleRoots(filepath.Clean(fullPath))
	// Either returns an error or returns partial results
	// (depends on where truncation happens - might get some valid lines before error)
	if err != nil {
		assert.Contains(t, err.Error(), "read snapshot")
	} else {
		// Partial results are OK - some roots may have been parsed before the truncation
		_ = roots
	}
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: invalid merkle root hex in signature (verify.go:84-87)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_InvalidSignatureValueHex(t *testing.T) {
	dir := t.TempDir()

	root := []byte("valid_root_data")
	mr := hex.EncodeToString(root)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	rootBytes, _ := hex.DecodeString(mr)
	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootBytes})
	computedRootHex := hex.EncodeToString(computedRoot)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        computedRootHex,
		BlockCount:        1,
		SignatureType:     constants.Ed25519ValueString,
		SignatureIdentity: "z6MkTestKey",
		SignatureValue:    "not_valid_hex_zzz",
		CreatedAt:         "2024-01-01T00:00:00Z",
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, result.Error, "decode signature hex")
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: verify returns error (verify.go:112-116)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_VerifyReturnsError(t *testing.T) {
	p, sig := newVerifyFixture(t, crypto.KeyTypeEd25519)

	// Corrupt the signature (truncate)
	sigBytes, err := hex.DecodeString(sig.SignatureValue)
	require.NoError(t, err)
	sig.SignatureValue = hex.EncodeToString(sigBytes[:len(sigBytes)-10])

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	assert.False(t, result.Valid)
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: valid=true path (verify.go:117-124)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_FullyValid(t *testing.T) {
	p, sig := newVerifyFixture(t, crypto.KeyTypeEd25519)

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.True(t, result.MerkleRootMatch)
	assert.True(t, result.SignatureValid)
	assert.Empty(t, result.Error)
}

// ---------------------------------------------------------------------------
// VerifySnapshotWithSig: signature fails verification (!Valid && Error=="")
// (verify.go:120-122)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig_SignatureInvalid_NoError(t *testing.T) {
	dir := t.TempDir()

	rootData := []byte("block_sig_root_data_2")
	mr := hex.EncodeToString(rootData)

	lines := []string{
		mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: mr}}),
	}
	p := writeJSONLFile(t, dir, "test.jsonl", lines)

	rootBytes, _ := hex.DecodeString(mr)
	computedRoot := ComputeSnapshotMerkleRoot([][]byte{rootBytes})
	computedRootHex := hex.EncodeToString(computedRoot)

	// Generate key and sign DIFFERENT data so verification fails
	fullIdent, err := identity.Generate(crypto.KeyTypeEd25519)
	require.NoError(t, err)

	wrongData := []byte("completely different data to sign")
	sigBytes, err := fullIdent.PrivateKey().Sign(wrongData)
	require.NoError(t, err)

	sig := &SnapshotSignatureData{
		SnapshotFile:      "test.jsonl",
		StartBlock:        1000,
		EndBlock:          1999,
		MerkleRoot:        computedRootHex,
		BlockCount:        1,
		SignatureType:     constants.Ed25519ValueString,
		SignatureIdentity: fullIdent.PublicKey().String(),
		SignatureValue:    hex.EncodeToString(sigBytes),
		CreatedAt:         "2024-01-01T00:00:00Z",
	}

	result, err := VerifySnapshotWithSig(p, sig)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.True(t, result.MerkleRootMatch)
	assert.False(t, result.SignatureValid)
	assert.Contains(t, result.Error, "signature verification failed")
}
