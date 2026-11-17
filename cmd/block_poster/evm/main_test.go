package main

import (
	"bytes"
	stderrors "errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain_ErrorExitsNonZero uses the subprocess test pattern: re-invoke the
// test binary with a sentinel environment variable, then verify the exit
// code. It lives next to the per-chain main because only the main package
// wires the process exit path.
func TestMain_ErrorExitsNonZero(t *testing.T) {
	if os.Getenv("TEST_MAIN_EXIT") == "1" {
		// When invoked as a subprocess, run main() with args that will
		// cause the runner to fail (invalid config path).
		os.Args = []string{"block_poster", "-config", "/nonexistent/config.yaml"}
		main() // calls os.Exit(1) inside
		return
	}

	// Parent process: run ourselves as a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=^TestMain_ErrorExitsNonZero$") //nolint:gosec
	cmd.Env = append(os.Environ(), "TEST_MAIN_EXIT=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	// Expect a non-zero exit code
	require.Error(t, err, "expected non-zero exit code from main() on error")
	var exitErr *exec.ExitError
	require.True(t, stderrors.As(err, &exitErr), "expected *exec.ExitError, got %T", err)
	assert.NotEqual(t, 0, exitErr.ExitCode())
	// The error message should be written to stderr
	assert.Contains(t, stderr.String(), "failed to load config")
}
