package migration

import (
	"fmt"
	"os"
	"time"
)

// Provider represents a data provider type
type Provider string

const (
	ProviderAWS      Provider = "aws"
	ProviderBigQuery Provider = "bigquery"
	ProviderCryo     Provider = "cryo"
)

// Config holds migration configuration
type Config struct {
	Provider         Provider
	StartBlock       int64
	EndBlock         int64
	BatchSize        int
	Workers          int
	EnableValidation bool
	ValidateSample   int
	DryRun           bool
	OutputDir        string
	ResumeFrom       int64
	AWSBucket        string
	AWSPrefix        string
	RPCURL           string
	DefraURL         string
	UseBulkAPI       bool // Use Collection API instead of GraphQL (faster)
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	switch c.Provider {
	case ProviderAWS, ProviderBigQuery, ProviderCryo:
	default:
		return fmt.Errorf("invalid provider: %s (must be aws, bigquery, or cryo)", c.Provider)
	}

	if c.StartBlock < 0 {
		return fmt.Errorf("start block must be >= 0")
	}
	if c.EndBlock != 0 && c.EndBlock < c.StartBlock {
		return fmt.Errorf("end block must be >= start block")
	}

	if c.BatchSize < 1 {
		return fmt.Errorf("batch size must be >= 1")
	}
	if c.BatchSize > 10000 {
		return fmt.Errorf("batch size must be <= 10000")
	}

	if c.Workers < 1 {
		return fmt.Errorf("workers must be >= 1")
	}
	if c.Workers > 32 {
		return fmt.Errorf("workers must be <= 32")
	}

	if c.OutputDir == "" {
		c.OutputDir = "./snapshot_data"
	}

	if err := os.MkdirAll(c.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	if c.EnableValidation && c.RPCURL == "" {
		return fmt.Errorf("RPC URL required for validation")
	}

	return nil
}

// Result holds migration results
type Result struct {
	Status                    string
	BlocksProcessed           int64
	BlocksImported            int64
	BlocksSkipped             int64
	TransactionsImported      int64
	LogsImported              int64
	AccessListEntriesImported int64
	ErrorCount                int
	LastCheckpoint            int64
	ValidationErrors          []ValidationError
	DownloadDuration          time.Duration
	ImportDuration            time.Duration
}

// ValidationError represents a validation error
type ValidationError struct {
	BlockNumber int64
	Field       string
	Expected    string
	Actual      string
	Message     string
}

// Checkpoint represents a migration checkpoint
type Checkpoint struct {
	LastBlock         int64  `json:"last_block"`
	Provider          string `json:"provider"`
	StartedAt         string `json:"started_at"`
	LastUpdated       string `json:"last_updated"`
	BlocksProcessed   int64  `json:"blocks_processed"`
	TransactionsCount int64  `json:"transactions_count"`
	LogsCount         int64  `json:"logs_count"`
}
