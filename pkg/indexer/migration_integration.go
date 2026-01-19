package indexer

import (
	"context"
	"fmt"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/migration"
)

// MigrationConfig holds configuration for historical data migration
type MigrationConfig struct {
	// Enabled controls whether migration runs before live indexing
	Enabled bool `yaml:"enabled"`
	// StartBlock is the first block to migrate (default: 0)
	StartBlock int64 `yaml:"start_block"`
	// EndBlock is the last block to migrate (0 = up to current indexed block)
	EndBlock int64 `yaml:"end_block"`
	// BatchSize is the number of blocks per batch (default: 1000)
	BatchSize int `yaml:"batch_size"`
	// Workers is the number of parallel workers (default: 4)
	Workers int `yaml:"workers"`
	// OutputDir is where downloaded snapshot data is cached
	OutputDir string `yaml:"output_dir"`
	// Provider is the data source (aws, bigquery, cryo)
	Provider string `yaml:"provider"`
	// EnableValidation enables RPC validation of imported data
	EnableValidation bool `yaml:"enable_validation"`
	// ValidateSample is number of blocks to validate (default: 100)
	ValidateSample int `yaml:"validate_sample"`
}

// DefaultMigrationConfig returns sensible defaults for migration
func DefaultMigrationConfig() *MigrationConfig {
	return &MigrationConfig{
		Enabled:        false,
		StartBlock:     0,
		EndBlock:       0,
		BatchSize:      1000,
		Workers:        4,
		OutputDir:      "./snapshot_data",
		Provider:       "aws",
		Validate:       false,
		ValidateSample: 100,
	}
}

// RunMigration runs historical data migration using the indexer's embedded DefraDB node.
// This should be called AFTER DefraDB is started but BEFORE live indexing begins.
//
// Example usage in StartIndexing():
//
//	if cfg.Migration.Enabled {
//	    if err := i.RunMigration(ctx, &cfg.Migration); err != nil {
//	        logger.Sugar.Errorf("Migration failed: %v", err)
//	        // Decide whether to continue with live indexing or fail
//	    }
//	}
func (i *ChainIndexer) RunMigration(ctx context.Context, migCfg *MigrationConfig) error {
	if i.defraNode == nil {
		return fmt.Errorf("migration requires embedded DefraDB node (defraNode is nil)")
	}

	if migCfg == nil {
		migCfg = DefaultMigrationConfig()
	}

	// Set defaults
	if migCfg.BatchSize <= 0 {
		migCfg.BatchSize = 1000
	}
	if migCfg.Workers <= 0 {
		migCfg.Workers = 4
	}
	if migCfg.OutputDir == "" {
		migCfg.OutputDir = "./snapshot_data"
	}
	if migCfg.Provider == "" {
		migCfg.Provider = "aws"
	}

	// If EndBlock is 0, migrate up to the configured start height
	// (which is where live indexing will begin)
	endBlock := migCfg.EndBlock
	if endBlock == 0 && i.cfg != nil {
		endBlock = int64(i.cfg.Indexer.StartHeight) - 1
		if endBlock < 0 {
			endBlock = 0
		}
	}

	// Skip if nothing to migrate
	if migCfg.StartBlock >= endBlock {
		logger.Sugar.Infof("No blocks to migrate (start=%d >= end=%d)", migCfg.StartBlock, endBlock)
		return nil
	}

	logger.Sugar.Infof("Starting historical migration: blocks %d to %d", migCfg.StartBlock, endBlock)

	// Build migration config
	cfg := &migration.Config{
		Provider:         migration.Provider(migCfg.Provider),
		StartBlock:       migCfg.StartBlock,
		EndBlock:         endBlock,
		BatchSize:        migCfg.BatchSize,
		Workers:          migCfg.Workers,
		OutputDir:        migCfg.OutputDir,
		EnableValidation: migCfg.EnableValidation,
		ValidateSample:   migCfg.ValidateSample,
		AWSBucket:        "aws-public-blockchain",
		AWSPrefix:        "v1.0/eth",
	}

	// Add RPC URL for validation if available
	if migCfg.EnableValidation && i.cfg != nil {
		cfg.RPCURL = i.cfg.Geth.NodeURL
	}

	// Validate config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid migration config: %w", err)
	}

	// Run migration with the indexer's DefraDB node
	result, err := migration.RunMigrationWithNode(ctx, i.defraNode, cfg)
	if err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	// Log results
	logger.Sugar.Infof("Migration complete: %s", result.Status)
	logger.Sugar.Infof("  Blocks processed: %d", result.BlocksProcessed)
	logger.Sugar.Infof("  Blocks imported: %d", result.BlocksImported)
	logger.Sugar.Infof("  Transactions: %d", result.TransactionsImported)
	logger.Sugar.Infof("  Logs: %d", result.LogsImported)
	logger.Sugar.Infof("  Errors: %d", result.ErrorCount)

	if len(result.ValidationErrors) > 0 {
		logger.Sugar.Warnf("  Validation errors: %d", len(result.ValidationErrors))
		for i, verr := range result.ValidationErrors {
			if i >= 5 {
				logger.Sugar.Warnf("    ... and %d more", len(result.ValidationErrors)-5)
				break
			}
			logger.Sugar.Warnf("    Block %d: %s", verr.BlockNumber, verr.Message)
		}
	}

	return nil
}

// RunMigrationAsync runs migration in the background while allowing the caller to continue.
// Returns a channel that will receive the result when migration completes.
//
// Example:
//
//	resultChan := i.RunMigrationAsync(ctx, migCfg)
//	// Continue with other setup...
//	result := <-resultChan
//	if result.Err != nil {
//	    log.Printf("Background migration failed: %v", result.Err)
//	}
func (i *ChainIndexer) RunMigrationAsync(ctx context.Context, migCfg *MigrationConfig) <-chan MigrationResult {
	resultChan := make(chan MigrationResult, 1)

	go func() {
		err := i.RunMigration(ctx, migCfg)
		resultChan <- MigrationResult{Err: err}
		close(resultChan)
	}()

	return resultChan
}

// MigrationResult holds the result of an async migration
type MigrationResult struct {
	Err error
}

// GetDefraNode returns the embedded DefraDB node (for use by migration package).
// Returns nil if using external DefraDB.
func (i *ChainIndexer) GetDefraNode() interface{} {
	return i.defraNode
}
