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
)

func main() {
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
