# Ethereum Snapshot Migration Tool v2

A high-performance tool for importing historical Ethereum blockchain data from snapshot providers into DefraDB using **direct embedded node access** (no HTTP API required).

## Key Improvement: Embedded DefraDB Support

This version supports direct integration with your embedded DefraDB node, bypassing the HTTP API entirely. This is critical because the shinzo-indexer-client uses an embedded DefraDB that doesn't expose HTTP endpoints externally.

## Quick Start

### Option 1: Standalone CLI (Spins up its own DefraDB)

```bash
# Build
go build -o bin/snapshot_migrate ./cmd/snapshot_migrate

# Run with embedded DefraDB (default)
./bin/snapshot_migrate \
    --provider aws \
    --start 20000000 \
    --end 20001000 \
    --embedded \
    --defra-data ./data/defra
```

### Option 2: Integrate into Main Indexer (Recommended)

Add migration capability directly to your indexer by calling the migration package with your existing DefraDB node:

```go
import (
    "context"
    "github.com/shinzonetwork/shinzo-indexer-client/pkg/migration"
)

func runHistoricalBackfill(ctx context.Context, defraNode *node.Node) error {
    // Using the fluent API
    result, err := migration.NewMigrationOptions().
        WithBlockRange(0, 20000000).
        WithBatchSize(5000).
        WithWorkers(8).
        Run(ctx, defraNode)
    
    if err != nil {
        return err
    }
    
    log.Printf("Imported %d blocks", result.BlocksImported)
    return nil
}
```

Or add it to your main.go:

```go
// In cmd/indexer/main.go or wherever you start DefraDB

// After starting your DefraDB node:
defraNode, err := startDefraDB(ctx)
if err != nil {
    log.Fatal(err)
}

// Check if historical backfill is needed
if needsBackfill {
    result, err := migration.RunMigrationWithNode(ctx, defraNode, &migration.Config{
        Provider:   migration.ProviderAWS,
        StartBlock: 0,
        EndBlock:   latestBackfillBlock,
        BatchSize:  5000,
        Workers:    8,
        OutputDir:  "./snapshot_data",
    })
    if err != nil {
        log.Printf("Backfill warning: %v", err)
    } else {
        log.Printf("Backfill complete: %d blocks imported", result.BlocksImported)
    }
}

// Continue with normal indexing...
```

## Command Line Options

```
Usage: snapshot_migrate [options]

Data Source Options:
  --provider        Data provider: aws, bigquery, cryo (default: aws)
  --aws-bucket      AWS S3 bucket name (default: aws-public-blockchain)  
  --aws-prefix      AWS S3 prefix path (default: v1.0/eth)

Block Range Options:
  --start           Start block number (default: 0)
  --end             End block number (0 = latest available)
  --resume          Resume from specific block (overrides checkpoint)

Performance Options:
  --batch           Blocks per batch (default: 1000)
  --workers         Number of parallel workers (default: 4)

DefraDB Options:
  --embedded        Use embedded DefraDB node (default: true)
  --defra-data      DefraDB data directory (default: ./data/defra)

Output Options:
  --output          Directory for downloaded data (default: ./snapshot_data)
  --config          Path to config file (default: config/config.yaml)

Validation Options:
  --validate        Validate imported data against RPC
  --validate-sample Number of blocks to sample (default: 100)
  --rpc             RPC URL for validation

Other Options:
  --dry-run         Download and parse without importing
  --help            Show help message
```

## Architecture

### Direct Node Access vs HTTP

| Approach | Pros | Cons |
|----------|------|------|
| **Direct Node (v2)** | No HTTP overhead, works with embedded DefraDB, faster | Requires same process or shared node |
| **HTTP API (v1)** | Can run separately | Requires HTTP API to be exposed |

### Data Flow

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  AWS S3 Bucket  │────▶│  Parquet Reader  │────▶│  Transformer    │
│  (Public Data)  │     │  (Parallel)      │     │  (AWS → Defra)  │
└─────────────────┘     └──────────────────┘     └────────┬────────┘
                                                          │
                                                          ▼
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│ DefraDB (Direct)│◀────│ BlockHandler     │◀────│  Block Grouper  │
│ node.DB.Exec()  │     │ WithNode()       │     │  (Txs + Logs)   │
└─────────────────┘     └──────────────────┘     └─────────────────┘
```

## Integration Methods

### Method 1: Add Command to Existing CLI

Add a `migrate` subcommand to your existing indexer:

```go
// cmd/indexer/migrate.go
func runMigrateCommand(cfg *config.Config) {
    ctx := context.Background()
    
    // Start DefraDB (same way as normal indexer)
    defraNode, err := startDefraDB(ctx, cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer defraNode.Close(ctx)
    
    // Run migration
    result, err := migration.NewMigrationOptions().
        WithBlockRange(cfg.Migration.StartBlock, cfg.Migration.EndBlock).
        WithBatchSize(cfg.Migration.BatchSize).
        WithWorkers(cfg.Migration.Workers).
        Run(ctx, defraNode)
    
    // Print results...
}
```

### Method 2: Background Migration During Startup

Run migration in background while indexer continues:

```go
func main() {
    ctx := context.Background()
    defraNode := startDefraDB(ctx)
    
    // Start background migration
    go func() {
        result, err := migration.RunMigrationWithNode(ctx, defraNode, &migration.Config{
            Provider:   migration.ProviderAWS,
            StartBlock: 0,
            EndBlock:   getHighestIndexedBlock(),
            BatchSize:  1000,
            Workers:    2, // Lower to not interfere with live indexing
        })
        if err != nil {
            log.Printf("Background migration error: %v", err)
        }
    }()
    
    // Continue with live indexing
    startLiveIndexer(ctx, defraNode)
}
```

### Method 3: Standalone Migration (Separate Process)

If you want to run migration separately, it will create its own DefraDB instance:

```bash
# This creates its own DefraDB at ./data/defra
./bin/snapshot_migrate \
    --embedded \
    --defra-data ./data/defra \
    --start 0 \
    --end 20000000

# Then start your indexer pointing to the same data directory
./bin/indexer --defra-data ./data/defra
```

⚠️ **Important**: Don't run both the migrator and indexer at the same time if they share the same data directory!

## Performance Expectations

| Configuration | Throughput | Notes |
|--------------|------------|-------|
| Default | ~50-100 blocks/sec | Safe for most systems |
| High Performance | ~200-500 blocks/sec | Requires good I/O |
| With Validation | ~30-50 blocks/sec | RPC calls slow it down |

## Troubleshooting

### "failed to create block handler with node"
Make sure you're passing a valid, started DefraDB node.

### "schema add warning"
This is normal if the schema is already loaded. The migration will continue.

### Slow performance
- Increase batch size: `--batch 5000`
- Increase workers: `--workers 8`
- Ensure good network to AWS S3
- Use SSD storage for DefraDB data

### Memory issues
- Reduce batch size: `--batch 500`
- Reduce workers: `--workers 2`

## Files

```
cmd/snapshot_migrate/
├── main.go              # CLI with embedded DefraDB support

pkg/migration/
├── config.go            # Configuration types
├── migrator.go          # Main orchestrator with node support
├── integration.go       # Helper functions for in-process use
├── aws_provider.go      # AWS S3 parquet reader
└── validator.go         # Data validation
```
