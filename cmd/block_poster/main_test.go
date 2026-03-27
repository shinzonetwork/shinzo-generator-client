package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sourcenetwork/defradb/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateID(t *testing.T) {
	t.Run("empty string returns empty", func(t *testing.T) {
		result := truncateID("")
		assert.Equal(t, "", result)
	})

	t.Run("short string under 20 chars is unchanged", func(t *testing.T) {
		result := truncateID("abc123")
		assert.Equal(t, "abc123", result)
	})

	t.Run("exactly 20 chars is unchanged", func(t *testing.T) {
		input := strings.Repeat("a", 20)
		result := truncateID(input)
		assert.Equal(t, input, result)
		assert.Len(t, result, 20)
	})

	t.Run("longer than 20 chars is truncated with ellipsis", func(t *testing.T) {
		input := strings.Repeat("x", 30)
		result := truncateID(input)
		assert.Equal(t, strings.Repeat("x", 20)+"...", result)
		assert.Len(t, result, 23)
	})
}

func TestVerifySnapshots(t *testing.T) {
	t.Run("no args returns usage error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := verifySnapshots(nil, &stdout, &stderr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage:")
	})

	t.Run("empty args returns usage error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := verifySnapshots([]string{}, &stdout, &stderr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage:")
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := verifySnapshots([]string{"/nonexistent/path/file.jsonl.gz"}, &stdout, &stderr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed verification")
		assert.Contains(t, stderr.String(), "FAIL:")
	})

	t.Run("invalid snapshot file returns error", func(t *testing.T) {
		// Create a temp dir with a fake snapshot and sig file
		tmpDir := t.TempDir()
		snapshotPath := filepath.Join(tmpDir, "test.jsonl.gz")
		sigPath := filepath.Join(tmpDir, "test.sig.json")

		// Write a non-gzip file as the snapshot
		err := os.WriteFile(snapshotPath, []byte("not a gzip file"), 0644)
		require.NoError(t, err)

		// Write an invalid sig file
		err = os.WriteFile(sigPath, []byte("not json"), 0644)
		require.NoError(t, err)

		var stdout, stderr bytes.Buffer
		err = verifySnapshots([]string{snapshotPath}, &stdout, &stderr)
		require.Error(t, err)
		assert.Contains(t, stderr.String(), "FAIL:")
	})

	t.Run("valid sig file with bad snapshot returns failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		snapshotPath := filepath.Join(tmpDir, "test.jsonl.gz")
		sigPath := filepath.Join(tmpDir, "test.sig.json")

		// Write a non-gzip file as the snapshot
		err := os.WriteFile(snapshotPath, []byte("not a gzip file"), 0644)
		require.NoError(t, err)

		// Write a valid JSON sig file (VerifySnapshot will parse it, then fail on snapshot read)
		sigJSON := `{
			"snapshot_file": "test.jsonl.gz",
			"start_block": 1,
			"end_block": 10,
			"block_count": 10,
			"merkle_root": "abc123",
			"signature_value": "def456",
			"signature_type": "Ed25519",
			"signature_identity": "someid"
		}`
		err = os.WriteFile(sigPath, []byte(sigJSON), 0644)
		require.NoError(t, err)

		var stdout, stderr bytes.Buffer
		err = verifySnapshots([]string{snapshotPath}, &stdout, &stderr)
		require.Error(t, err)
		// Should get a FAIL in stderr for the bad snapshot
		assert.Contains(t, stderr.String(), "FAIL:")
	})

	t.Run("multiple files with first failing", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := verifySnapshots(
			[]string{"/nonexistent/file1.jsonl.gz", "/nonexistent/file2.jsonl.gz"},
			&stdout, &stderr,
		)
		require.Error(t, err)
		// Both files should produce FAIL output
		failCount := strings.Count(stderr.String(), "FAIL:")
		assert.Equal(t, 2, failCount, "expected 2 FAIL lines, got: %s", stderr.String())
	})
}

func TestRun(t *testing.T) {
	t.Run("verify subcommand with no args returns error", func(t *testing.T) {
		err := run([]string{"verify"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "usage:")
	})

	t.Run("verify subcommand with nonexistent file returns error", func(t *testing.T) {
		err := run([]string{"verify", "/nonexistent/snapshot.jsonl.gz"})
		require.Error(t, err)
	})

	t.Run("invalid config path returns error", func(t *testing.T) {
		err := run([]string{"-config", "/nonexistent/config.yaml"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load config")
	})

	t.Run("default config path that does not exist returns error", func(t *testing.T) {
		// Change to a temp dir so the default config/config.yaml doesn't exist
		origDir, err := os.Getwd()
		require.NoError(t, err)
		tmpDir := t.TempDir()
		require.NoError(t, os.Chdir(tmpDir))
		defer func() { _ = os.Chdir(origDir) }()

		err = run([]string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load config")
	})

	t.Run("invalid flag returns error", func(t *testing.T) {
		err := run([]string{"-invalid-flag-that-does-not-exist"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse flags")
	})

	t.Run("valid config with embedded defra fails at StartIndexing", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// Embedded=true means useExternalDefra=false, so StartIndexing(false) is called.
		// StartDefraInstance will start a real DefraDB node in the temp dir.
		// The test will fail at the Ethereum connection step (invalid geth URL).
		configContent := fmt.Sprintf(`
defradb:
  url: ""
  embedded: true
  p2p:
    enabled: false
  store:
    path: "%s/defra"
geth:
  node_url: "http://127.0.0.1:1"
  ws_url: ""
indexer:
  start_height: 1
  concurrent_blocks: 1
  receipt_workers: 1
  max_docs_per_txn: 100
  health_server_port: -1
  start_buffer: 10
pruner:
  enabled: false
snapshot:
  enabled: false
logger:
  development: true
`, tmpDir)

		err := os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err)

		err = run([]string{"-config", configPath})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to start indexing")
	})

	t.Run("valid config with external defra fails at StartIndexing", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// Embedded=false means useExternalDefra=true, so StartIndexing(true) is called.
		// WaitForDefraDB will fail because the URL is unreachable.
		configContent := fmt.Sprintf(`
defradb:
  url: "http://127.0.0.1:1"
  embedded: false
  p2p:
    enabled: false
  store:
    path: "%s/defra"
geth:
  node_url: "http://127.0.0.1:1"
  ws_url: ""
indexer:
  start_height: 1
  concurrent_blocks: 1
  receipt_workers: 1
  max_docs_per_txn: 100
  health_server_port: -1
  start_buffer: 10
pruner:
  enabled: false
snapshot:
  enabled: false
logger:
  development: true
`, tmpDir)

		err := os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err)

		err = run([]string{"-config", configPath})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to start indexing")
	})

	t.Run("signal handling shuts down gracefully", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// Use external DefraDB with a non-routable TEST-NET-2 (RFC 5737) address.
		// TCP SYN to this IP gets no response, so WaitForDefraDB's HTTP client
		// hangs for its 5s timeout on the first attempt. The SIGTERM at 500ms
		// wins the select race before errChan receives anything.
		// Note: 127.0.0.1:1 can't be used because some CI runners have port 1
		// accessible, causing WaitForDefraDB to return immediately.
		configContent := fmt.Sprintf(`
defradb:
  url: "http://198.51.100.1:1"
  embedded: false
  p2p:
    enabled: false
  store:
    path: "%s/defra"
geth:
  node_url: "http://127.0.0.1:1"
  ws_url: ""
indexer:
  start_height: 1
  concurrent_blocks: 1
  receipt_workers: 1
  max_docs_per_txn: 100
  health_server_port: -1
  start_buffer: 10
pruner:
  enabled: false
snapshot:
  enabled: false
logger:
  development: true
`, tmpDir)

		err := os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err)

		// Send SIGTERM after a short delay to trigger graceful shutdown path.
		// The goroutine will be blocked in WaitForDefraDB retries, so the
		// select in run() will pick up our signal.
		go func() {
			time.Sleep(500 * time.Millisecond)
			p, _ := os.FindProcess(os.Getpid())
			_ = p.Signal(syscall.SIGTERM)
		}()

		err = run([]string{"-config", configPath})
		// Signal handling path returns nil
		assert.NoError(t, err)
	})

	t.Run("invalid config content returns config error", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// Write YAML that is valid YAML but produces an invalid config
		// (start_height < 0 fails validation)
		configContent := `
defradb:
  url: "http://localhost:9181"
  embedded: false
geth:
  node_url: "http://127.0.0.1:1"
indexer:
  start_height: -1
`
		err := os.WriteFile(configPath, []byte(configContent), 0644)
		require.NoError(t, err)

		err = run([]string{"-config", configPath})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load config")
	})
}

// createTestSnapshot creates a gzipped JSONL snapshot file containing block_signature
// entries with the given merkle root hex strings.
func createTestSnapshot(t *testing.T, dir, filename string, merkleRootHexes []string) string {
	t.Helper()
	snapshotPath := filepath.Join(dir, filename)
	f, err := os.Create(snapshotPath)
	require.NoError(t, err)

	gw := gzip.NewWriter(f)
	for i, mrHex := range merkleRootHexes {
		entry := map[string]any{
			"type": "block_signature",
			"data": map[string]any{
				"blockNumber": i + 1,
				"merkleRoot":  mrHex,
			},
		}
		line, err := json.Marshal(entry)
		require.NoError(t, err)
		_, err = gw.Write(append(line, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())
	return snapshotPath
}

// computeMerkleRoot replicates the snapshot.ComputeSnapshotMerkleRoot logic
// for test fixture creation.
func computeMerkleRoot(roots [][]byte) []byte {
	if len(roots) == 0 {
		return nil
	}
	hashes := make([][]byte, len(roots))
	for i, root := range roots {
		hash := sha256.Sum256(root)
		hashes[i] = hash[:]
	}
	combined := make([]byte, 64)
	for len(hashes) > 1 {
		var newHashes [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				copy(combined[:32], hashes[i])
				copy(combined[32:], hashes[i+1])
				hash := sha256.Sum256(combined)
				newHashes = append(newHashes, hash[:])
			} else {
				newHashes = append(newHashes, hashes[i])
			}
		}
		hashes = newHashes
	}
	return hashes[0]
}

func TestVerifySnapshots_ValidSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate an Ed25519 key pair for signing
	privKey, err := crypto.GenerateKey(crypto.KeyTypeEd25519)
	require.NoError(t, err)
	pubKey := privKey.GetPublic()

	// Create block signature merkle roots (arbitrary 32-byte values)
	root1 := sha256.Sum256([]byte("block1"))
	root2 := sha256.Sum256([]byte("block2"))
	merkleRootHexes := []string{
		hex.EncodeToString(root1[:]),
		hex.EncodeToString(root2[:]),
	}

	// Create the gzipped JSONL snapshot file
	snapshotPath := createTestSnapshot(t, tmpDir, "valid.jsonl.gz", merkleRootHexes)

	// Compute the expected snapshot merkle root
	roots := [][]byte{root1[:], root2[:]}
	snapshotMerkleRoot := computeMerkleRoot(roots)
	snapshotMerkleRootHex := hex.EncodeToString(snapshotMerkleRoot)

	// Sign the merkle root
	sigBytes, err := privKey.Sign(snapshotMerkleRoot)
	require.NoError(t, err)

	// Create the sidecar sig.json
	sigData := map[string]any{
		"version":            1,
		"snapshot_file":      "valid.jsonl.gz",
		"start_block":        1,
		"end_block":          2,
		"block_count":        2,
		"merkle_root":        snapshotMerkleRootHex,
		"signature_type":     "Ed25519",
		"signature_identity": pubKey.String(),
		"signature_value":    hex.EncodeToString(sigBytes),
	}
	sigJSON, err := json.Marshal(sigData)
	require.NoError(t, err)
	sigPath := filepath.Join(tmpDir, "valid.sig.json")
	err = os.WriteFile(sigPath, sigJSON, 0644)
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	err = verifySnapshots([]string{snapshotPath}, &stdout, &stderr)
	require.NoError(t, err, "stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), "PASS:")
	assert.Contains(t, stdout.String(), "blocks 1-2")
	assert.Contains(t, stdout.String(), "2 block sigs")
	assert.Empty(t, stderr.String())
}

func TestVerifySnapshots_MerkleRootMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a snapshot with known block sig merkle roots
	root1 := sha256.Sum256([]byte("block1"))
	merkleRootHexes := []string{
		hex.EncodeToString(root1[:]),
	}
	snapshotPath := createTestSnapshot(t, tmpDir, "mismatch.jsonl.gz", merkleRootHexes)

	// Create sig.json with a WRONG merkle root (so result.Valid=false due to mismatch)
	sigData := map[string]any{
		"version":            1,
		"snapshot_file":      "mismatch.jsonl.gz",
		"start_block":        1,
		"end_block":          1,
		"block_count":        1,
		"merkle_root":        "0000000000000000000000000000000000000000000000000000000000000000",
		"signature_type":     "Ed25519",
		"signature_identity": "fakeid",
		"signature_value":    "fakesig",
	}
	sigJSON, err := json.Marshal(sigData)
	require.NoError(t, err)
	sigPath := filepath.Join(tmpDir, "mismatch.sig.json")
	err = os.WriteFile(sigPath, sigJSON, 0644)
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	err = verifySnapshots([]string{snapshotPath}, &stdout, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed verification")
	assert.Contains(t, stderr.String(), "FAIL:")
	assert.Contains(t, stderr.String(), "merkle root mismatch")
}

func TestVerifySnapshots_AllValid_ReturnsNil(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate an Ed25519 key pair
	privKey, err := crypto.GenerateKey(crypto.KeyTypeEd25519)
	require.NoError(t, err)
	pubKey := privKey.GetPublic()

	// Create a single-block snapshot
	root1 := sha256.Sum256([]byte("singleblock"))
	merkleRootHexes := []string{hex.EncodeToString(root1[:])}
	snapshotPath := createTestSnapshot(t, tmpDir, "single.jsonl.gz", merkleRootHexes)

	// Compute merkle root and sign
	snapshotMerkleRoot := computeMerkleRoot([][]byte{root1[:]})
	sigBytes, err := privKey.Sign(snapshotMerkleRoot)
	require.NoError(t, err)

	sigData := map[string]any{
		"version":            1,
		"snapshot_file":      "single.jsonl.gz",
		"start_block":        1,
		"end_block":          1,
		"block_count":        1,
		"merkle_root":        hex.EncodeToString(snapshotMerkleRoot),
		"signature_type":     "Ed25519",
		"signature_identity": pubKey.String(),
		"signature_value":    hex.EncodeToString(sigBytes),
	}
	sigJSON, err := json.Marshal(sigData)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "single.sig.json"), sigJSON, 0644)
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	err = verifySnapshots([]string{snapshotPath}, &stdout, &stderr)
	// This covers the return nil path (line 114) when all snapshots are valid
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "PASS:")
}

func TestMain_ErrorExitsNonZero(t *testing.T) {
	// Use the subprocess test pattern: re-invoke the test binary with a
	// sentinel environment variable, then verify the exit code.
	if os.Getenv("TEST_MAIN_EXIT") == "1" {
		// When invoked as a subprocess, run main() with args that will cause
		// run() to fail (invalid config path).
		os.Args = []string{"block_poster", "-config", "/nonexistent/config.yaml"}
		main() // calls os.Exit(1) inside
		return
	}

	// Parent process: run ourselves as a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_ErrorExitsNonZero$")
	cmd.Env = append(os.Environ(), "TEST_MAIN_EXIT=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	// Expect a non-zero exit code
	require.Error(t, err, "expected non-zero exit code from main() on error")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected *exec.ExitError, got %T", err)
	assert.NotEqual(t, 0, exitErr.ExitCode())
	// The error message should be written to stderr
	assert.Contains(t, stderr.String(), "failed to load config")
}

func TestMain_VerifyValidSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate key and create valid snapshot (same as above)
	privKey, err := crypto.GenerateKey(crypto.KeyTypeEd25519)
	require.NoError(t, err)
	pubKey := privKey.GetPublic()

	root1 := sha256.Sum256([]byte("maintest"))
	merkleRootHexes := []string{hex.EncodeToString(root1[:])}
	snapshotPath := createTestSnapshot(t, tmpDir, "main_test.jsonl.gz", merkleRootHexes)

	snapshotMerkleRoot := computeMerkleRoot([][]byte{root1[:]})
	sigBytes, err := privKey.Sign(snapshotMerkleRoot)
	require.NoError(t, err)

	sigData := map[string]any{
		"version":            1,
		"snapshot_file":      "main_test.jsonl.gz",
		"start_block":        1,
		"end_block":          1,
		"block_count":        1,
		"merkle_root":        hex.EncodeToString(snapshotMerkleRoot),
		"signature_type":     "Ed25519",
		"signature_identity": pubKey.String(),
		"signature_value":    hex.EncodeToString(sigBytes),
	}
	sigJSON, err := json.Marshal(sigData)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "main_test.sig.json"), sigJSON, 0644)
	require.NoError(t, err)

	// Save and restore os.Args
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	// Set os.Args to simulate: block_poster verify <valid_snapshot>
	// Since run() returns nil for a valid verify, main() won't call os.Exit
	os.Args = []string{"block_poster", "verify", snapshotPath}

	// This covers the main() function's first statement (line 18: calling run())
	// Since run() returns nil, the if-block is not entered.
	main()
}
