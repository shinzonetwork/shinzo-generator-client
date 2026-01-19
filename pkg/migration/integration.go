package migration

import (
	"context"
	"fmt"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/sourcenetwork/defradb/node"
)

// RunMigrationWithNode is a convenience function to run migration with an existing DefraDB node.
// This is useful when integrating migration into the main indexer process.
//
// Example usage from main indexer:
//
//	result, err := migration.RunMigrationWithNode(ctx, defraNode, &migration.Config{
//	    Provider:   migration.ProviderAWS,
//	    StartBlock: 0,
//	    EndBlock:   20000000,
//	    BatchSize:  5000,
//	    Workers:    8,
//	    OutputDir:  "./snapshot_data",
//	})
func RunMigrationWithNode(ctx context.Context, defraNode *node.Node, cfg *Config) (*Result, error) {
	if defraNode == nil {
		return nil, fmt.Errorf("defraNode is required")
	}

	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Set defaults
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "./snapshot_data"
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderAWS
	}
	if cfg.AWSBucket == "" {
		cfg.AWSBucket = "aws-public-blockchain"
	}
	if cfg.AWSPrefix == "" {
		cfg.AWSPrefix = "v1.0/eth"
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Create migrator with node
	migrator, err := NewMigratorWithNode(cfg, defraNode)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}

	logger.Sugar.Infof("Starting migration: blocks %d to %d", cfg.StartBlock, cfg.EndBlock)

	// Run migration
	result, err := migrator.Run(ctx)
	if err != nil {
		return result, fmt.Errorf("migration failed: %w", err)
	}

	return result, nil
}

// MigrationOptions provides a fluent API for configuring migrations
type MigrationOptions struct {
	cfg *Config
}

// NewMigrationOptions creates a new MigrationOptions builder
func NewMigrationOptions() *MigrationOptions {
	return &MigrationOptions{
		cfg: &Config{
			Provider:         ProviderAWS,
			BatchSize:        1000,
			Workers:          4,
			OutputDir:        "./snapshot_data",
			AWSBucket:        "aws-public-blockchain",
			AWSPrefix:        "v1.0/eth",
			ValidateSample:   100,
			EnableValidation: false,
		},
	}
}

// WithProvider sets the data provider
func (o *MigrationOptions) WithProvider(p Provider) *MigrationOptions {
	o.cfg.Provider = p
	return o
}

// WithBlockRange sets the block range to migrate
func (o *MigrationOptions) WithBlockRange(start, end int64) *MigrationOptions {
	o.cfg.StartBlock = start
	o.cfg.EndBlock = end
	return o
}

// WithBatchSize sets the batch size
func (o *MigrationOptions) WithBatchSize(size int) *MigrationOptions {
	o.cfg.BatchSize = size
	return o
}

// WithWorkers sets the number of parallel workers
func (o *MigrationOptions) WithWorkers(n int) *MigrationOptions {
	o.cfg.Workers = n
	return o
}

// WithOutputDir sets the output directory for downloaded data
func (o *MigrationOptions) WithOutputDir(dir string) *MigrationOptions {
	o.cfg.OutputDir = dir
	return o
}

// WithValidation enables validation against RPC
func (o *MigrationOptions) WithValidation(rpcURL string, sampleSize int) *MigrationOptions {
	o.cfg.EnableValidation = true
	o.cfg.RPCURL = rpcURL
	o.cfg.ValidateSample = sampleSize
	return o
}

// WithDryRun enables dry run mode (no actual import)
func (o *MigrationOptions) WithDryRun() *MigrationOptions {
	o.cfg.DryRun = true
	return o
}

// WithResume sets the resume block number
func (o *MigrationOptions) WithResume(blockNum int64) *MigrationOptions {
	o.cfg.ResumeFrom = blockNum
	return o
}

// Build returns the configured Config
func (o *MigrationOptions) Build() *Config {
	return o.cfg
}

// Run executes the migration with the given DefraDB node
func (o *MigrationOptions) Run(ctx context.Context, defraNode *node.Node) (*Result, error) {
	return RunMigrationWithNode(ctx, defraNode, o.cfg)
}
