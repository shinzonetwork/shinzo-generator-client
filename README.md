# Shinzo Network Blockchain Indexer

A high-performance blockchain indexing solution built with Source Network, DefraDB, and LensVM.

## Architecture

- **GoLang**: High-performance indexing engine with concurrent processing
- **DefraDB**: Decentralized P2P datastore for blockchain data storage and querying

- **Managed Blockchain Node**: Dual WebSocket/HTTP connections to Google Cloud managed Ethereum nodes
- **Uber Zap**: Structured logging with global logger integration
- **GraphQL**: Flexible query interface for indexed blockchain data
- **Viper Configuration**: YAML-based configuration with environment variable overrides

### Recent Improvements

- **Enhanced Error Handling**: Production-ready error system with categorization, retry logic, and structured context
- **Logger Stabilization**: Global logger initialization with proper test support and no file dependencies
- **Schema Updates**: Removed Event entity, added AccessListEntry support for EIP-2930 transactions
- **Test Suite Fixes**: Resolved all logger-related panics and GraphQL response parsing issues
- **Smart Retry Logic**: Intelligent retry behavior based on error types and severity

## Features

- Real-time blockchain data indexing
- GraphQL API for querying indexed data
- Support for blocks, transactions, logs, and access list entries
- Bi-directional relationships between blockchain entities
- Deterministic document IDs
- Graceful shutdown handling
- Concurrent indexing with duplicate block protection
- **Comprehensive Error Handling System**:
  - Categorized error types (Network, Data, Storage, System)
  - Severity levels (Info, Warning, Error, Critical)
  - Smart retry logic with exponential backoff
  - Structured error context and logging
  - Production-ready monitoring and alerting
- Global logger integration with Uber Zap
- Context-aware error reporting with block numbers and transaction hashes

## Hardware Recommendations

| Component | Minimum | Recommended |
| --- | --- | --- |
| CPU | 8 vCPUs | 16 vCPUs |
| Memory (RAM) | 16 GB | 32–64 GB |
| Storage | 3 TB NVMe | 4+ TB NVMe |
| OS | Ubuntu 24.04 | Ubuntu 24.04 |

## Prerequisites

- Go 1.24+
- Ethereum node with JSON-RPC and WebSocket access (GCP Managed Blockchain Node recommended)

## Setup

1. **Set up Ethereum Node Access**:
   - Use a local or deployed Geth Ethereum Node
   - Note the JSON-RPC and WebSocket endpoints  
   - Configure API key authentication if required

## Installation

1. Clone the repository:
   ```bash
   git clone https://github.com/shinzonetwork/indexer.git
   cd indexer
   ```

2. Install Go dependencies:
   ```bash
   go mod download
   ```

3. Create environment variables:
   ```bash
   cat >> .env << EOF
   GETH_RPC_URL=<your-geth-node-url>
   GETH_WS_URL=<your-geth-ws-url>
   GETH_API_KEY=<your-geth-api-key>
   
   # DefraDB Configuration
   DEFRADB_URL=http://localhost:9181 || empty for embedded defradb
   DEFRADB_KEYRING_SECRET=<your-defradb-keyring-secret>
   DEFRADB_PLAYGROUND=true
   DEFRADB_P2P_ENABLED=true
   DEFRADB_P2P_BOOTSTRAP_PEERS=[]
   DEFRADB_P2P_LISTEN_ADDR="/ip4/127.0.0.1/tcp/9171"
   
   # Indexer Configuration
   INDEXER_START_HEIGHT=23000000
   
   # Logger Configuration
   LOGGER_DEBUG=true
   EOF
   ```

## Configuration

1. The configuration uses `config/config.yaml` with environment variable overrides:
   ```yaml
   # Default configuration - environment variables will override these
   defradb:
     url: ""  # Empty = embedded DefraDB
     keyring_secret: ""
     playground: true
     p2p:
       enabled: true
       bootstrap_peers: []
       listen_addr: "/ip4/0.0.0.0/tcp/9171"
     store:
       path: "./.defra"
   
   geth:
     node_url: "<your-geth-node-url>"  #  blockchain node
     ws_url: "<your-geth-ws-url>"
     api_key: "<your-geth-api-key>"    # Recommend using a .env file
   
   indexer:
     start_height: 23500000
   logger:
     development: true
   ```

## Production Deployment

1. SSH into your VM
2. Paste the contents of `indexer-prod-setup.sh` into the terminal and press Enter — this creates the `docker-compose.yml` with your configuration
3. Paste the contents of `indexer-prod.sh` into the terminal and press Enter — this installs Docker, sets up data directories, and starts the indexer

The indexer should now be up and running.

## How to Run (Development)

### Using Makefile (Recommended)
```bash
# Build the indexer (standard version - non-branchable schema)
make build

# Build with branchable schema support
make build TAGS=branchable

# Start the indexer
make start

# Or build and run in one step
go run cmd/block_poster/main.go

# Build and run with branchable schema
go run -tags=branchable cmd/block_poster/main.go
```

### Build Tags

The indexer supports build tags to control schema behavior:

- **Standard Build** (default): Uses non-branchable schema for parallel processing
- **Branchable Build** (`TAGS=branchable`): Uses branchable schema for sequential processing

```bash
# Standard build (parallel processing)
make build

# Branchable build (sequential processing)
make build TAGS=branchable

# Docker builds
docker build -t shinzo-indexer:standard .
docker build --build-arg BUILD_TAGS=branchable -t shinzo-indexer:branchable .
```

### Docker Images
Two separate Docker images are built and published:

- **Standard Image**: `ghcr.io/shinzonetwork/indexer:standard` - Non-branchable schema
- **Branchable Image**: `ghcr.io/shinzonetwork/indexer:branchable` - Branchable schema

```bash
# Pull and run standard version
docker pull ghcr.io/shinzonetwork/indexer:standard
docker run -d --name shinzo-indexer-standard ghcr.io/shinzonetwork/indexer:standard

# Pull and run branchable version
docker pull ghcr.io/shinzonetwork/indexer:branchable
docker run -d --name shinzo-indexer-branchable ghcr.io/shinzonetwork/indexer:branchable
```

### Using Docker (Alternative)
```bash
# Build and run with Docker Compose
docker-compose up --build
```
### Manual Build
```bash
# Build binary (standard version)
go build -o bin/block_poster cmd/block_poster/main.go

# Build binary with branchable schema
go build -tags=branchable -o bin/block_poster cmd/block_poster/main.go

# Run binary
./bin/block_poster
```

### Available Makefile Targets
- `make build` - Build the indexer binary (standard version)
- `make build TAGS=branchable` - Build with branchable schema
- `make start` - Start the indexer
- `make test` - Run all tests with summary
- `make integration-test` - Run integration tests (mock + live)
- `make test-local` - Run local indexer tests with Geth endpoint
- `make geth-status` - Check Geth node connectivity and block number
- `make coverage` - Generate test coverage report
- `make clean` - Clean build artifacts
- `make stop` - Stop all services
- `make help` - Show all available targets with descriptions

## Test Coverage

| Package | Coverage |
| --- | --- |
| `cmd/block_poster` | 98.0% |
| `config` | 100.0% |
| `pkg/defra` | 98.6% |
| `pkg/errors` | 100.0% |
| `pkg/indexer` | 95.3% |
| `pkg/logger` | 100.0% |
| `pkg/rpc` | 98.3% |
| `pkg/schema` | 100.0% |
| `pkg/server` | 99.0% |
| `pkg/snapshot` | 86.6% |
| `pkg/utils` | 100.0% |
| **Total** | **95.6%** |

Run `make coverage` to generate an HTML coverage report.

## Testing

### Unit Tests
To run unit tests, you can run `make test` or simply `go test ./...` per standard go.

### Integration Tests
```bash
make integration-test
```
This runs both mock tests (fast) and live tests (if environment variables are set). No external dependencies required - uses embedded DefraDB.

### Live Integration Tests with  Endpoint
To run live integration tests with your  managed blockchain node:

1. **Set up environment variables**:
   ```bash
   export GETH_RPC_URL=<your-geth-node-url>
   export GETH_WS_URL=<your-geth-ws-url>
   export GETH_API_KEY=<your-geth-api-key>
```

2. **Or use a .env file**:
   ```bash
   # Create .env file
   cat > .env << EOF
   # Ethereum Node Configuration
   GETH_RPC_URL=<your-geth-node-url>
   GETH_WS_URL=<your-geth-ws-url>
   GETH_API_KEY=<your-geth-api-key>
   
   # DefraDB Configuration (optional - uses embedded by default)
   DEFRADB_KEYRING_SECRET=<your-defradb-keyring-secret>
   
   # Indexer Configuration
   INDEXER_START_HEIGHT=23500000
   EOF
   ```

2. **Test  node connectivity**:
   ```bash
   make geth-status
   ```

3. **Run local tests**:
   ```bash
   ./test_local.sh
   ```
   
   Or use the Makefile target:
   ```bash
   make test-local
   ```

This will run the indexer tests locally with your  managed blockchain node, providing comprehensive diagnostics and avoiding public node limitations.

## Data Model

### Schema Variants

The indexer supports two schema variants based on build tags:

#### Standard Schema (Default)
- **Processing**: Parallel transaction processing for better performance
- **Use Case**: High-throughput indexing where transaction order doesn't affect data consistency
- **Entities**: Block, Transaction, Log, AccessListEntry without @branchable directive

#### Branchable Schema (`TAGS=branchable`)
- **Processing**: Sequential transaction processing for data consistency
- **Use Case**: Scenarios requiring strict transaction ordering and conflict prevention
- **Entities**: Same entities with @branchable directive for DefraDB branchable collections

### Entities and Relationships
- **Block**
  - Primary key: `hash` (unique index)
  - Secondary index: `number`
  - Has many transactions (`block_transactions`)
- **Transaction**
  - Primary key: `hash` (unique index)
  - Secondary indices: `blockHash`, `blockNumber`
  - Belongs to block (`block_transactions`)
  - Has many logs (`transaction_logs`)
- **Log**
  - Indices: `blockNumber`
  - Belongs to block and transaction
- **AccessListEntry**
  - Belongs to transaction
  - Contains address and storage keys

**Note**: The Event entity was removed from the schema in recent updates.

### Relationship Definitions

DefraDB relationships use the `@relation(name: "relationship_name")` syntax. Example:

```graphql
type Block {
  transactions: [Transaction] @relation(name: "block_transactions")
}

type Transaction {
  block: Block @relation(name: "block_transactions")
}
```

## Error Handling System

Shinzo implements a comprehensive error handling system designed for production-ready distributed blockchain indexing:

### Error Types
- **NetworkError**: RPC/HTTP communication issues (often retryable)
- **DataError**: Parsing/validation failures (usually non-retryable)
- **StorageError**: Database operation failures (sometimes retryable)
- **SystemError**: Critical system-level failures (requires attention)

### Severity Levels
- **Info**: Informational messages
- **Warning**: Issues that don't stop processing
- **Error**: Significant issues requiring attention
- **Critical**: Severe issues that may require immediate action

### Retry Behavior
- **NonRetryable**: Errors that should not be retried (e.g., data validation)
- **Retryable**: Simple retry without backoff
- **RetryableWithBackoff**: Exponential backoff retry for network issues

### Structured Context
All errors include rich context:
- Component and operation names
- Block numbers and transaction hashes when applicable
- Timestamps and error codes
- Underlying error chains
- Metadata for debugging

### Smart Retry Logic
The system implements intelligent retry logic:
```go
if errors.IsRetryable(err) {
    retryDelay := errors.GetRetryDelay(err, retryAttempts)
    time.Sleep(retryDelay)
    // Retry operation
}
```

### Logging Strategy

The indexer uses Uber Zap for structured logging:
- Global logger initialization via `logger.Init()` | `logger.InitConsoleOnly()` | `logger.InitWithFiles()`
- Context-aware error logging with `logger.LogError()`
- Structured fields for monitoring and debugging
- Different log levels based on error severity
- No file output during tests (stdout/stderr only)

### Querying Data

Access indexed data through DefraDB's GraphQL API at `http://localhost:9181/api/v0/graphql`

Example query:
```graphql
{
  Block(filter: { number: { _eq: 18100003 } }) {
    hash
    number
    transactions {
      hash
      value
      gasPrice
      accessList {
        address
        storageKeys
      }
      logs {
        logIndex
        data
        address
        topics
      }
    }
  }
}
```

## Documentation Links

- [DefraDB Documentation](https://github.com/sourcenetwork/defradb)
- [Source Network Documentation](https://docs.sourcenetwork.io)
- [Geth Documentation](https://geth.ethereum.org/docs/)

## Development Status

### ✅ Completed (Phase 1)
- **Error System**: Comprehensive IndexerError system with NetworkError, DataError, StorageError, and SystemError types
- **Logger Integration**: Global Zap logger with structured logging and test compatibility
- **Main Application**: Smart retry logic and structured error handling in block_poster
- **Test Stability**: All test suites pass without logger panics or GraphQL parsing issues

### 🔧 In Progress (Phase 2)
- **RPC Client**: Enhanced network error handling with timeout and retry logic
- **Type Conversions**: Improved data parsing error reporting

### 📋 Planned (Phase 3)
- **Advanced Logging**: Additional helper functions for error analysis
- **Monitoring Integration**: Metrics and alerting based on error codes
- **Performance Optimizations**: Error handling performance improvements

### Key Benefits Achieved
1. **Operational Excellence**: Clear retry guidance for different error types
2. **Observability**: Structured error logging with context and codes
3. **Debugging**: Rich context (block numbers, tx hashes, metadata)
4. **Reliability**: Smart retry logic based on error characteristics
5. **Maintainability**: Consistent error handling patterns across codebase

## Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Development Guidelines
- Use the new error system for all error handling
- Follow structured logging patterns with context
- Include retry logic based on error types
- Write tests that work with the global logger
- Document significant error handling changes in commit messages

## License

This project is licensed under the MIT License - see the LICENSE file for details.
