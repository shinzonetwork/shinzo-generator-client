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
	batchSize := flag.Int("batch", 1000, "Blocks per download batch")
	workers := flag.Int("workers", 4, "Number of parallel workers")
	multiBlockBatch := flag.Int("multi-block", 20, "Blocks per DB transaction (higher = faster but more memory)")
	validate := flag.Bool("validate", false, "Validate imported data against RPC")
	validateSample := flag.Int("validate-sample", 100, "Number of blocks to sample for validation")
	dryRun := flag.Bool("dry-run", false, "Download and parse data without importing")
	configPath := flag.String("config", "config/config.yaml", "Path to config file")
	outputDir := flag.String("output", "./snapshot_data", "Directory for downloaded snapshot data")
	resumeFrom := flag.Int64("resume", 0, "Resume from specific block (overrides checkpoint)")
	awsBucket := flag.String("aws-bucket", "aws-public-blockchain", "AWS S3 bucket name")
	awsPrefix := flag.String("aws-prefix", "v1.0/eth", "AWS S3 prefix path")
	rpcURL := flag.String("rpc", "", "RPC URL for validation (overrides config)")
	useEmbedded := flag.Bool("embedded", true, "Use embedded DefraDB node")
	defraDataDir := flag.String("defra-data", "./data/defra", "DefraDB data directory")
	useBulkAPI := flag.Bool("bulk", true, "Use Collection API instead of GraphQL (faster)")
	flag.Parse()

	logger.Init(true)

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		logger.Sugar.Fatalf("Failed to load config: %v", err)
	}

	if *rpcURL != "" {
		cfg.Geth.NodeURL = *rpcURL
	}

	migrationCfg := &migration.Config{
		Provider:         migration.Provider(*provider),
		StartBlock:       *startBlock,
		EndBlock:         *endBlock,
		BatchSize:        *batchSize,
		Workers:          *workers,
		MultiBlockBatch:  *multiBlockBatch,
		EnableValidation: *validate,
		ValidateSample:   *validateSample,
		DryRun:           *dryRun,
		OutputDir:        *outputDir,
		ResumeFrom:       *resumeFrom,
		AWSBucket:        *awsBucket,
		AWSPrefix:        *awsPrefix,
		RPCURL:           cfg.Geth.NodeURL,
		DefraURL:         cfg.DefraDB.Url,
		UseBulkAPI:       *useBulkAPI,
	}

	if err := migrationCfg.Validate(); err != nil {
		logger.Sugar.Fatalf("Invalid configuration: %v", err)
	}

	printConfig(migrationCfg, *useEmbedded)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logger.Sugar.Infof("Received signal %v, shutting down...", sig)
		cancel()
	}()

	var migrator *migration.Migrator
	var defraNode *node.Node

	if *useEmbedded && !*dryRun {
		logger.Sugar.Info("Starting embedded DefraDB node...")
		defraNode, err = startEmbeddedDefra(ctx, *defraDataDir)
		if err != nil {
			logger.Sugar.Fatalf("Failed to start embedded DefraDB: %v", err)
		}
		defer func() {
			logger.Sugar.Info("Shutting down DefraDB node...")
			defraNode.Close(context.Background())
		}()

		migrator, err = migration.NewMigratorWithNode(migrationCfg, defraNode)
		if err != nil {
			logger.Sugar.Fatalf("Failed to create migrator: %v", err)
		}
	} else {
		migrator, err = migration.NewMigrator(migrationCfg)
		if err != nil {
			logger.Sugar.Fatalf("Failed to create migrator: %v", err)
		}
	}

	startTime := time.Now()
	result, err := migrator.Run(ctx)
	if err != nil {
		if ctx.Err() != nil {
			logger.Sugar.Info("Migration cancelled by user")
		} else {
			logger.Sugar.Fatalf("Migration failed: %v", err)
		}
	}

	printResults(result, time.Since(startTime))
}

func startEmbeddedDefra(ctx context.Context, dataDir string) (*node.Node, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	opts := []node.Option{
		node.WithStoreType(node.BadgerStore),
		node.WithStorePath(dataDir),
		node.WithDisableP2P(true),
		node.WithDisableAPI(true),
	}

	defraNode, err := node.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create DefraDB node: %w", err)
	}

	if err := defraNode.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start DefraDB node: %w", err)
	}

	schemaStr := schema.GetSchemaForBuild()
	if _, err := defraNode.DB.AddSchema(ctx, schemaStr); err != nil {
		logger.Sugar.Warnf("Schema add warning (may be already loaded): %v", err)
	}

	logger.Sugar.Infof("DefraDB node started with data dir: %s", dataDir)
	return defraNode, nil
}

func printConfig(cfg *migration.Config, useEmbedded bool) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("ETHEREUM SNAPSHOT MIGRATION TOOL")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Provider:          %s\n", cfg.Provider)
	fmt.Printf("Block Range:       %d - %d\n", cfg.StartBlock, cfg.EndBlock)
	fmt.Printf("Download Batch:    %d blocks\n", cfg.BatchSize)
	fmt.Printf("DB Batch:          %d blocks/txn\n", cfg.MultiBlockBatch)
	fmt.Printf("Workers:           %d\n", cfg.Workers)
	fmt.Printf("Output Dir:        %s\n", cfg.OutputDir)
	fmt.Printf("Dry Run:           %v\n", cfg.DryRun)
	fmt.Printf("Embedded DefraDB:  %v\n", useEmbedded)
	fmt.Printf("Use Bulk API:      %v\n", cfg.UseBulkAPI)
	fmt.Println(strings.Repeat("=", 60) + "\n")
}

func printResults(result *migration.Result, totalDuration time.Duration) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("MIGRATION RESULTS")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Status:              %s\n", result.Status)
	fmt.Printf("Total Duration:      %s\n", totalDuration.Round(time.Millisecond))
	fmt.Println(strings.Repeat("-", 60))

	fmt.Println("TIMING BREAKDOWN:")
	fmt.Printf("  Download Time:     %s\n", result.DownloadDuration.Round(time.Millisecond))
	fmt.Printf("  Import Time:       %s\n", result.ImportDuration.Round(time.Millisecond))
	otherTime := totalDuration - result.DownloadDuration - result.ImportDuration
	if otherTime > 0 {
		fmt.Printf("  Other (overhead):  %s\n", otherTime.Round(time.Millisecond))
	}
	fmt.Println(strings.Repeat("-", 60))

	fmt.Println("DATA IMPORTED:")
	fmt.Printf("  Blocks Processed:    %d\n", result.BlocksProcessed)
	fmt.Printf("  Blocks Imported:     %d\n", result.BlocksImported)
	fmt.Printf("  Transactions:        %d\n", result.TransactionsImported)
	fmt.Printf("  Logs:                %d\n", result.LogsImported)
	fmt.Printf("  Access List Entries: %d\n", result.AccessListEntriesImported)
	fmt.Printf("  Errors:              %d\n", result.ErrorCount)
	fmt.Println(strings.Repeat("-", 60))

	fmt.Println("PERFORMANCE (Import Only):")
	if result.ImportDuration.Seconds() > 0 && result.BlocksImported > 0 {
		blocksPerSec := float64(result.BlocksImported) / result.ImportDuration.Seconds()
		txsPerSec := float64(result.TransactionsImported) / result.ImportDuration.Seconds()
		logsPerSec := float64(result.LogsImported) / result.ImportDuration.Seconds()
		msPerBlock := result.ImportDuration.Milliseconds() / result.BlocksImported

		fmt.Printf("  Blocks/sec:          %.2f\n", blocksPerSec)
		fmt.Printf("  Transactions/sec:    %.2f\n", txsPerSec)
		fmt.Printf("  Logs/sec:            %.2f\n", logsPerSec)
		fmt.Printf("  ms/block:            %d\n", msPerBlock)
	}

	if result.LastCheckpoint > 0 {
		fmt.Println(strings.Repeat("-", 60))
		fmt.Printf("Last Checkpoint:     %d\n", result.LastCheckpoint)
	}
	fmt.Println(strings.Repeat("=", 60))
}

var strings = struct {
	Repeat func(string, int) string
}{
	Repeat: func(s string, n int) string {
		result := ""
		for i := 0; i < n; i++ {
			result += s
		}
		return result
	},
}
