package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/indexer"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/snapshot"
)

func main() {
	// Check for subcommands before parsing flags
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "verify":
			runVerify(os.Args[2:])
			return
		}
	}

	configPath := flag.String("config", "config/config.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Create and start indexer
	chainIndexer, err := indexer.CreateIndexer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create indexer: %v\n", err)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Failed to start indexing: %v\n", err)
		os.Exit(1)
	case sig := <-sigChan:
		fmt.Printf("\nReceived signal %v, shutting down gracefully...\n", sig)
		chainIndexer.StopIndexing()
		cancel()
		fmt.Println("Shutdown complete")
	}
}

func runVerify(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s verify <snapshot-file.jsonl.gz> [snapshot-file...]\n", os.Args[0])
		os.Exit(1)
	}

	allValid := true
	for _, file := range args {
		result, err := snapshot.VerifySnapshot(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %s — %v\n", file, err)
			allValid = false
			continue
		}

		if result.Valid {
			fmt.Printf("PASS: %s (blocks %d-%d, %d block sigs, signed by %s)\n",
				file, result.StartBlock, result.EndBlock, result.BlockSigsFound, truncateID(result.SignerIdentity))
		} else {
			fmt.Fprintf(os.Stderr, "FAIL: %s — %s\n", file, result.Error)
			allValid = false
		}
	}

	if !allValid {
		os.Exit(1)
	}
}

func truncateID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:20] + "..."
}
