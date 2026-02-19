package main

import (
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

	// Channel to listen for interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start indexer in a goroutine
	errChan := make(chan error, 1)
	go func() {
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
		fmt.Println("Shutdown complete")
	}
}
