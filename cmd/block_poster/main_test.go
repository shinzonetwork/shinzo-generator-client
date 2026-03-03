package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}
