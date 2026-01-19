package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shinzonetwork/shinzo-indexer-client/config"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/migration"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
	"github.com/sourcenetwork/defradb/node"
)

func main() {
	// Parse command line flags
	provider := flag.String("provider", "aws", "Data provider: aws, bigquery, cryo")
	startBlock := flag.Int64("start", 0, "Start block number")
	endBlock := flag.Int64("end", 0, "End block number (0 = latest available)")
	batchSize := flag.Int("batch", 1000, "Blocks per batch")
	workers := flag.Int("workers", 4, "Number of parallel workers")
	validate := flag.Bool("validate", false, "Validate imported data against RPC")
	validateSample := flag.Int("validate-sample", 100, "Number of blocks to sample for validation")
	dryRun := flag.Bool("dry-run", false, "Download and parse data without importing")
	configPath := flag.String("config", "config/config.yaml", "Path to config file")
	outputDir := flag.String("output", "./snapshot_data", "Directory for downloaded snapshot data")
	resumeFrom := flag.Int64("resume", 0, "Resume from specific block (overrides checkpoint)")
	awsBucket := flag.String("aws-bucket", "aws-public-blockchain", "AWS S3 bucket name")
	awsPrefix := flag.String("aws-prefix", "v1.0/eth", "AWS S3 prefix path")
	rpcURL := flag.String("rpc", "", "RPC URL for validation (overrides config)")
	useEmbedded := flag.Bool("embedded", true, "Use embedded DefraDB node (default: true)")
	defraDataDir := flag.String("defra-data", "./data/defra", "DefraDB data directory (for embedded mode)")
	flag.Parse()

	// Initialize logger
	logger.Init(true)

	// Load config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		logger.Sugar.Fatalf("Failed to load config: %v", err)
	}

	// Override RPC URL if provided
	if *rpcURL != "" {
		cfg.Geth.NodeURL = *rpcURL
	}

	// Create migration config
	migrationCfg := &migration.Config{
		Provider:         migration.Provider(*provider),
		StartBlock:       *startBlock,
		EndBlock:         *endBlock,
		BatchSize:        *batchSize,
		Workers:          *workers,
		EnableValidation: *validate,
		ValidateSample:   *validateSample,
		DryRun:           *dryRun,
		OutputDir:        *outputDir,
		ResumeFrom:       *resumeFrom,
		AWSBucket:        *awsBucket,
		AWSPrefix:        *awsPrefix,
		RPCURL:           cfg.Geth.NodeURL,
		DefraURL:         cfg.DefraDB.Url,
	}

	// Validate config
	if err := migrationCfg.Validate(); err != nil {
		logger.Sugar.Fatalf("Invalid configuration: %v", err)
	}

	// Print configuration
	printConfig(migrationCfg, *useEmbedded)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logger.Sugar.Infof("Received signal %v, initiating graceful shutdown...", sig)
		cancel()
	}()

	var migrator *migration.Migrator
	var defraNode *node.Node

	if *useEmbedded && !*dryRun {
		// Start embedded DefraDB node
		logger.Sugar.Info("Starting embedded DefraDB node...")
		defraNode, err = startEmbeddedDefra(ctx, *defraDataDir)
		if err != nil {
			logger.Sugar.Fatalf("Failed to start embedded DefraDB: %v", err)
		}
		defer func() {
			logger.Sugar.Info("Shutting down DefraDB node...")
			if err := defraNode.Close(context.Background()); err != nil {
				logger.Sugar.Errorf("Error closing DefraDB node: %v", err)
			}
		}()

		// Create migrator with embedded node
		migrator, err = migration.NewMigratorWithNode(migrationCfg, defraNode)
		if err != nil {
			logger.Sugar.Fatalf("Failed to create migrator: %v", err)
		}
	} else {
		// Create migrator (uses HTTP or dry-run)
		migrator, err = migration.NewMigrator(migrationCfg)
		if err != nil {
			logger.Sugar.Fatalf("Failed to create migrator: %v", err)
		}
	}

	startTime := time.Now()

	// Run migration
	result, err := migrator.Run(ctx)
	if err != nil {
		if ctx.Err() != nil {
			logger.Sugar.Info("Migration cancelled by user")
		} else {
			logger.Sugar.Fatalf("Migration failed: %v", err)
		}
	}

	// Print results
	printResults(result, time.Since(startTime))
}

// startEmbeddedDefra starts an embedded DefraDB node
func startEmbeddedDefra(ctx context.Context, dataDir string) (*node.Node, error) {
	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Configure DefraDB node
	opts := []node.Option{
		node.WithStoreType(node.BadgerStore),
		node.WithStorePath(dataDir),
		node.WithDisableP2P(true),  // Disable P2P for migration
		node.WithDisableAPI(true),  // Disable HTTP API - we use direct access
	}

	// Create and start node
	defraNode, err := node.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create DefraDB node: %w", err)
	}

	if err := defraNode.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start DefraDB node: %w", err)
	}

	// Load schema
	schemaStr := schema.GetSchemaForBuild()
	if _, err := defraNode.DB.AddSchema(ctx, schemaStr); err != nil {
		// Schema might already exist, try to continue
		logger.Sugar.Warnf("Schema add warning (may be already loaded): %v", err)
	}

	logger.Sugar.Infof("DefraDB node started with data dir: %s", dataDir)
	return defraNode, nil
}

func printConfig(cfg *migration.Config, useEmbedded bool) {
	fmt.Println("\n" + repeatString("=", 60))
	fmt.Println("ETHEREUM SNAPSHOT MIGRATION TOOL")
	fmt.Println(repeatString("=", 60))
	fmt.Printf("Provider:        %s\n", cfg.Provider)
	fmt.Printf("Block Range:     %d - %d\n", cfg.StartBlock, cfg.EndBlock)
	fmt.Printf("Batch Size:      %d blocks\n", cfg.BatchSize)
	fmt.Printf("Workers:         %d\n", cfg.Workers)
	fmt.Printf("Output Dir:      %s\n", cfg.OutputDir)
	fmt.Printf("Dry Run:         %v\n", cfg.DryRun)
	fmt.Printf("Validation:      %v\n", cfg.EnableValidation)
	fmt.Printf("Embedded DefraDB: %v\n", useEmbedded)
	if cfg.EnableValidation {
		fmt.Printf("Validate Sample: %d blocks\n", cfg.ValidateSample)
	}
	fmt.Println(repeatString("=", 60) + "\n")
}

func printResults(result *migration.Result, duration time.Duration) {
	fmt.Println("\n" + repeatString("=", 60))
	fmt.Println("MIGRATION RESULTS")
	fmt.Println(repeatString("=", 60))
	fmt.Printf("Status:              %s\n", result.Status)
	fmt.Printf("Duration:            %s\n", duration.Round(time.Second))
	fmt.Printf("Blocks Processed:    %d\n", result.BlocksProcessed)
	fmt.Printf("Blocks Imported:     %d\n", result.BlocksImported)
	fmt.Printf("Transactions:        %d\n", result.TransactionsImported)
	fmt.Printf("Logs:                %d\n", result.LogsImported)
	fmt.Printf("Access List Entries: %d\n", result.AccessListEntriesImported)
	fmt.Printf("Errors:              %d\n", result.ErrorCount)
	if result.BlocksProcessed > 0 && duration.Seconds() > 0 {
		blocksPerSec := float64(result.BlocksProcessed) / duration.Seconds()
		fmt.Printf("Throughput:          %.2f blocks/sec\n", blocksPerSec)
	}
	if result.LastCheckpoint > 0 {
		fmt.Printf("Last Checkpoint:     %d\n", result.LastCheckpoint)
	}
	if len(result.ValidationErrors) > 0 {
		fmt.Printf("\nValidation Errors (%d):\n", len(result.ValidationErrors))
		for i, err := range result.ValidationErrors {
			if i >= 10 {
				fmt.Printf("  ... and %d more\n", len(result.ValidationErrors)-10)
				break
			}
			fmt.Printf("  - Block %d: %s\n", err.BlockNumber, err.Message)
		}
	}
	fmt.Println(repeatString("=", 60))
}

// Helper function to repeat a string
func repeatString(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}
