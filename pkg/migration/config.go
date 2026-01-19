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
	EnableValidation bool // Renamed from Validate to avoid conflict with method
	ValidateSample   int
	DryRun           bool
	OutputDir        string
	ResumeFrom       int64
	AWSBucket        string
	AWSPrefix        string
	RPCURL           string
	DefraURL         string
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Validate provider
	switch c.Provider {
	case ProviderAWS, ProviderBigQuery, ProviderCryo:
		// Valid
	default:
		return fmt.Errorf("invalid provider: %s (must be aws, bigquery, or cryo)", c.Provider)
	}

	// Validate block range
	if c.StartBlock < 0 {
		return fmt.Errorf("start block must be >= 0")
	}
	if c.EndBlock != 0 && c.EndBlock < c.StartBlock {
		return fmt.Errorf("end block must be >= start block")
	}

	// Validate batch size
	if c.BatchSize < 1 {
		return fmt.Errorf("batch size must be >= 1")
	}
	if c.BatchSize > 10000 {
		return fmt.Errorf("batch size must be <= 10000")
	}

	// Validate workers
	if c.Workers < 1 {
		return fmt.Errorf("workers must be >= 1")
	}
	if c.Workers > 32 {
		return fmt.Errorf("workers must be <= 32")
	}

	// Validate output directory
	if c.OutputDir == "" {
		c.OutputDir = "./snapshot_data"
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(c.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Validate RPC URL for validation
	if c.EnableValidation && c.RPCURL == "" {
		return fmt.Errorf("RPC URL required for validation")
	}

	// Validate DefraDB URL for non-dry-run
	if !c.DryRun && c.DefraURL == "" {
		// This is OK - we might be using embedded node directly
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
	
	// Timing information
	DownloadDuration time.Duration // Time spent downloading from provider
	ImportDuration   time.Duration // Time spent importing to DefraDB
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
