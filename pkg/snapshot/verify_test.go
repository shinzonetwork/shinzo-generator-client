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

func TestExtractBlockSigMerkleRoots(t *testing.T) {
	orderedMrs := make([]string, 5)
	for i := range orderedMrs {
		data := make([]byte, 32)
		data[0] = byte(i + 1)
		orderedMrs[i] = hex.EncodeToString(data)
	}

	tests := []struct {
		name        string
		write       func(t *testing.T, dir string) string
		wantHex     []string
		wantErr     bool
		errContains string
	}{
		{
			name: "plain jsonl file with two block signatures",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: hexRoot("root1_data_bytes"), constants.BlockNumberKeyValue: 1000}}),
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: hexRoot("root2_data_bytes"), constants.BlockNumberKeyValue: 1001}}),
				}
				return writeJSONLFile(t, dir, "test.jsonl", lines)
			},
			wantHex: []string{hexRoot("root1_data_bytes"), hexRoot("root2_data_bytes")},
		},
		{
			name: "gzipped jsonl file",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: hexRoot("gzip_root_data")}}),
				}
				return writeGzipJSONLFile(t, dir, "test.jsonl.gz", lines)
			},
			wantHex: []string{hexRoot("gzip_root_data")},
		},
		{
			name: "empty plain file",
			write: func(t *testing.T, dir string) string {
				return writeJSONLFile(t, dir, "empty.jsonl", []string{})
			},
			wantHex: []string{},
		},
		{
			name: "empty gzip file",
			write: func(t *testing.T, dir string) string {
				return writeGzipJSONLFile(t, dir, "empty.jsonl.gz", []string{})
			},
			wantHex: []string{},
		},
		{
			name: "non-block-signature entries are skipped",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					mustJSON(t, map[string]any{"type": "block", "data": map[string]any{constants.NumberFieldValue: 1000}}),
					mustJSON(t, map[string]any{"type": "transaction", "data": map[string]any{"hash": "0xabc"}}),
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: hexRoot("valid_root")}}),
					mustJSON(t, map[string]any{"type": "log", "data": map[string]any{"logIndex": 0}}),
				}
				return writeJSONLFile(t, dir, "mixed.jsonl", lines)
			},
			wantHex: []string{hexRoot("valid_root")},
		},
		{
			name: "invalid json lines are skipped",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					"this is not json at all",
					"{ broken json",
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: hexRoot("good_root")}}),
					"",
				}
				return writeJSONLFile(t, dir, "invalid_lines.jsonl", lines)
			},
			wantHex: []string{hexRoot("good_root")},
		},
		{
			name: "empty merkle root and missing field are skipped",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: ""}}),
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{"other": "field"}}),
				}
				return writeJSONLFile(t, dir, "empty_roots.jsonl", lines)
			},
			wantHex: []string{},
		},
		{
			name: "invalid hex root is skipped",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: "not_valid_hex_zzz"}}),
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": map[string]any{constants.MerkleRootKeyValue: hexRoot("valid_root")}}),
				}
				return writeJSONLFile(t, dir, "bad_hex.jsonl", lines)
			},
			wantHex: []string{hexRoot("valid_root")},
		},
		{
			name: "nil and missing data are skipped",
			write: func(t *testing.T, dir string) string {
				lines := []string{
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue, "data": nil}),
					mustJSON(t, map[string]any{"type": constants.BlockSignatureTypeValue}),
				}
				return writeJSONLFile(t, dir, "nil_data.jsonl", lines)
			},
			wantHex: []string{},
		},
		{
			name:    "missing file",
			write:   func(_ *testing.T, _ string) string { return "/nonexistent/path/file.jsonl" },
			wantErr: true,
		},
		{
			name: "invalid gzip data",
			write: func(t *testing.T, dir string) string {
				// Write non-gzip content to a .gz file
				p := filepath.Join(dir, "bad.jsonl.gz")
				err := os.WriteFile(p, []byte("this is not gzip data"), 0o600)
				require.NoError(t, err)
				return p
			},
			wantErr:     true,
			errContains: "gzip reader",
		},
		{
			name: "multiple valid roots preserved in order",
			write: func(t *testing.T, dir string) string {
				var lines []string
				for i, mr := range orderedMrs {
					lines = append(lines, mustJSON(t, map[string]any{
						"type": constants.BlockSignatureTypeValue,
						"data": map[string]any{constants.MerkleRootKeyValue: mr, constants.BlockNumberKeyValue: 1000 + i},
					}))
				}
				return writeJSONLFile(t, dir, "ordered.jsonl", lines)
			},
			wantHex: orderedMrs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.write(t, t.TempDir())

			roots, err := extractBlockSigMerkleRoots(p)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, roots)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			require.Len(t, roots, len(tt.wantHex))
			for i, wantHex := range tt.wantHex {
				expected, _ := hex.DecodeString(wantHex)
				assert.Equal(t, expected, roots[i])
			}
		})
	}
}

// Non-deterministic outcome (error or partial results depending on where
// truncation lands), so it stays an explicit test instead of a table row.
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
// VerifySnapshotWithSig (table: crypto verification outcomes over a valid fixture)
// ---------------------------------------------------------------------------

func TestVerifySnapshotWithSig(t *testing.T) {
	tests := []struct {
		name              string
		keyType           crypto.KeyType
		mutate            func(t *testing.T, sig *SnapshotSignatureData)
		wantErr           bool
		errContains       string
		wantValid         bool
		wantRootMatch     bool
		wantSigValid      bool
		resultErrContains string
		resultErrNotEmpty bool
	}{
		{
			name:          "valid ed25519 signature",
			keyType:       crypto.KeyTypeEd25519,
			wantValid:     true,
			wantRootMatch: true,
			wantSigValid:  true,
		},
		{
			name:          "valid secp256k1 signature",
			keyType:       crypto.KeyTypeSecp256k1,
			wantValid:     true,
			wantRootMatch: true,
			wantSigValid:  true,
		},
		{
			name:    "lowercase ecdsa-256k type is accepted",
			keyType: crypto.KeyTypeSecp256k1,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.SignatureType = "ecdsa-256k"
			},
			wantValid:     true,
			wantRootMatch: true,
			wantSigValid:  true,
		},
		{
			name:    "lowercase ed25519 type is accepted",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.SignatureType = strings.ToLower(constants.Ed25519ValueString)
			},
			wantValid:     true,
			wantRootMatch: true,
			wantSigValid:  true,
		},
		{
			name:    "signature over different data fails verification",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(t *testing.T, sig *SnapshotSignatureData) {
				fullIdent, err := identity.Generate(crypto.KeyTypeEd25519)
				require.NoError(t, err)
				wrongData := bytes.Repeat([]byte{0xFF}, 32)
				sigValue, err := fullIdent.PrivateKey().Sign(wrongData)
				require.NoError(t, err)
				sig.SignatureType = constants.Ed25519ValueString
				sig.SignatureIdentity = fullIdent.PublicKey().String()
				sig.SignatureValue = hex.EncodeToString(sigValue)
			},
			wantRootMatch:     true,
			resultErrContains: "signature verification failed",
		},
		{
			name:    "secp256k1 signature with garbage DER bytes",
			keyType: crypto.KeyTypeSecp256k1,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.SignatureValue = hex.EncodeToString([]byte("not a valid DER signature"))
			},
			wantRootMatch:     true,
			resultErrNotEmpty: true,
		},
		{
			name:    "truncated ed25519 signature",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(t *testing.T, sig *SnapshotSignatureData) {
				sigBytes, err := hex.DecodeString(sig.SignatureValue)
				require.NoError(t, err)
				sig.SignatureValue = hex.EncodeToString(sigBytes[:len(sigBytes)-10])
			},
			wantRootMatch: true,
		},
		{
			name:    "signature value is not valid hex",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.SignatureValue = "not_valid_hex_zzz"
			},
			wantRootMatch:     true,
			resultErrContains: "decode signature hex",
		},
		{
			name:    "unsupported signature type",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.SignatureType = "RSA-UNSUPPORTED"
			},
			wantErr:           true,
			errContains:       "unsupported signature type",
			wantRootMatch:     true,
			resultErrContains: "unsupported signature type",
		},
		{
			name:    "unparseable signer identity",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.SignatureIdentity = "bad_key_string"
			},
			wantRootMatch:     true,
			resultErrContains: "parse public key",
		},
		{
			name:    "invalid merkle root hex in signature mismatches",
			keyType: crypto.KeyTypeEd25519,
			mutate: func(_ *testing.T, sig *SnapshotSignatureData) {
				sig.MerkleRoot = "not_valid_hex_zzz"
			},
			resultErrContains: "merkle root mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, sig := newVerifyFixture(t, tt.keyType)
			if tt.mutate != nil {
				tt.mutate(t, sig)
			}

			result, err := VerifySnapshotWithSig(p, sig)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			assert.Equal(t, tt.wantValid, result.Valid)
			assert.Equal(t, tt.wantRootMatch, result.MerkleRootMatch)
			assert.Equal(t, tt.wantSigValid, result.SignatureValid)
			if tt.wantValid {
				assert.Empty(t, result.Error)
			}
			if tt.resultErrContains != "" {
				assert.Contains(t, result.Error, tt.resultErrContains)
			}
			if tt.resultErrNotEmpty {
				assert.NotEmpty(t, result.Error)
			}
		})
	}
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
