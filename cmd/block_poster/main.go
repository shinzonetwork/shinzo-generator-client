package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/indexer"

	// Import chain implementations to register them
	_ "github.com/shinzonetwork/shinzo-indexer-client/pkg/chains/ethereum"
	_ "github.com/shinzonetwork/shinzo-indexer-client/pkg/chains/solana"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "Path to configuration file")
	chainName := flag.String("chain", "", "Chain to index (ethereum, solana). Overrides config file.")
	listChains := flag.Bool("list-chains", false, "List available chains and exit")
	flag.Parse()

	// List available chains if requested
	if *listChains {
		fmt.Println("Available chains:")
		fmt.Println("  - ethereum (Ethereum Mainnet)")
		fmt.Println("  - solana   (Solana Mainnet)")
		fmt.Println("\nUsage: block_poster --chain <chain_name>")
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Override active chain if specified via flag
	if *chainName != "" {
		cfg.Chains.Active = *chainName
	}

	// Get active chain configuration
	activeChain := cfg.GetActiveChain()
	chainCfg, err := cfg.GetChainConfig(activeChain)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get chain config for '%s': %v\n", activeChain, err)
		os.Exit(1)
	}

	fmt.Printf("Starting indexer for chain: %s (%s)\n", activeChain, chainCfg.Network)

	if cfg.Indexer.PprofPort > 0 {
		go func() {
			addr := fmt.Sprintf(":%d", cfg.Indexer.PprofPort)
			fmt.Printf("Starting pprof server on %s\n", addr)
			http.ListenAndServe(addr, nil)
		}()
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
