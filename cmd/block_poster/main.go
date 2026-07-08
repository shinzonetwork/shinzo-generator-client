package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/snapshot"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Check for subcommands before parsing flags
	if len(args) > 0 {
		if args[0] == "verify" { //nolint:goconst
			return verifySnapshots(args[1:], os.Stdout, os.Stderr)
		}
	}

	// Optionally expose pprof profiling endpoints (opt-in via PPROF_ENABLED).
	// DefraDB runs embedded in this process, so the profiles cover it too.
	startPprofServer()

	fs := flag.NewFlagSet("block_poster", flag.ContinueOnError)
	configPath := fs.String("config", "config/config.yaml", "Path to configuration file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create and start indexer
	chainIndexer, err := indexer.CreateIndexer(cfg)
	if err != nil {
		return fmt.Errorf("failed to create indexer: %w", err)
	}

	// Set up graceful shutdown
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel to listen for interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start indexer in a goroutine
	errChan := make(chan error, 1)
	go func() {
		// Determine whether we're using an external DefraDB instance or embedded
		// External DefraDB is used when a URL is configured and Embedded is false
		useExternalDefra := !cfg.DefraDB.Embedded

		if err := chainIndexer.StartIndexing(useExternalDefra); err != nil {
			errChan <- err
		}
	}()

	// Wait for either an error or shutdown signal
	select {
	case err := <-errChan:
		return fmt.Errorf("failed to start indexing: %w", err)
	case sig := <-sigChan:
		fmt.Printf("\nReceived signal %v, shutting down gracefully...\n", sig)
		chainIndexer.StopIndexing()
		cancel()
		fmt.Println("Shutdown complete")
	}

	return nil
}

// verifySnapshots verifies one or more snapshot files and reports results.
// It returns an error if any snapshot is invalid or verification fails.
func verifySnapshots(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: block_poster verify <snapshot-file.jsonl.gz> [snapshot-file...]")
	}

	allValid := true
	for _, file := range args {
		result, err := snapshot.VerifySnapshot(file)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "FAIL: %s — %v\n", file, err)
			allValid = false
			continue
		}

		if result.Valid {
			_, _ = fmt.Fprintf(stdout, "PASS: %s (blocks %d-%d, %d block sigs, signed by %s)\n",
				file, result.StartBlock, result.EndBlock, result.BlockSigsFound, truncateID(result.SignerIdentity))
		} else {
			_, _ = fmt.Fprintf(stderr, "FAIL: %s — %s\n", file, result.Error)
			allValid = false
		}
	}

	if !allValid {
		return fmt.Errorf("one or more snapshots failed verification")
	}

	return nil
}

func truncateID(id string) string {
	if len(id) <= 20 { //nolint:mnd
		return id
	}
	return id[:20] + "..."
}

// startPprofServer starts a dedicated HTTP server exposing Go's net/http/pprof
// endpoints when PPROF_ENABLED is set to a truthy value (1/true/yes/on). It is a
// no-op otherwise, so it is safe to leave the call in place for production builds.
//
// DefraDB runs embedded in this process, so these profiles cover DefraDB as well
// as the indexer itself. Endpoints (default listen address :6060):
//
//	/debug/pprof/           index of available profiles
//	/debug/pprof/profile    CPU profile, 30s default (?seconds=N to change)
//	/debug/pprof/heap       heap allocations (live memory)
//	/debug/pprof/allocs     all past allocations
//	/debug/pprof/goroutine  goroutine stacks (?debug=2 for full dump)
//	/debug/pprof/block      blocking profile (needs PPROF_BLOCK_RATE > 0)
//	/debug/pprof/mutex      mutex contention (needs PPROF_MUTEX_FRACTION > 0)
//	/debug/pprof/trace      execution trace (?seconds=N)
func startPprofServer() {
	if !isTruthy(os.Getenv("PPROF_ENABLED")) {
		return
	}

	addr := os.Getenv("PPROF_ADDR")
	if addr == "" {
		addr = ":6060"
	}

	// Block and mutex profiling are off by default because they add runtime
	// overhead; enable them explicitly when hunting contention in DefraDB.
	if v := os.Getenv("PPROF_BLOCK_RATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			runtime.SetBlockProfileRate(n)
		}
	}
	if v := os.Getenv("PPROF_MUTEX_FRACTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			runtime.SetMutexProfileFraction(n)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// No timeouts: CPU/trace profiles are long-lived streaming responses.
	srv := &http.Server{Addr: addr, Handler: mux} //nolint:gosec // local profiling endpoint

	go func() {
		log.Printf("pprof server listening on %s (profiles under /debug/pprof/)", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pprof server error: %v", err)
		}
	}()
}

// isTruthy reports whether s is a common truthy string (case-insensitive).
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
