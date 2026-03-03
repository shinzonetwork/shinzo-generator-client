package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/snapshot"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Check for subcommands before parsing flags
	if len(args) > 0 {
		switch args[0] {
		case "verify":
			return verifySnapshots(args[1:], os.Stdout, os.Stderr)
		}
	}

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
			fmt.Fprintf(stderr, "FAIL: %s — %v\n", file, err)
			allValid = false
			continue
		}

		if result.Valid {
			fmt.Fprintf(stdout, "PASS: %s (blocks %d-%d, %d block sigs, signed by %s)\n",
				file, result.StartBlock, result.EndBlock, result.BlockSigsFound, truncateID(result.SignerIdentity))
		} else {
			fmt.Fprintf(stderr, "FAIL: %s — %s\n", file, result.Error)
			allValid = false
		}
	}

	if !allValid {
		return fmt.Errorf("one or more snapshots failed verification")
	}

	return nil
}

func truncateID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:20] + "..."
}
