.PHONY: build clean test migrate migrate-dry migrate-test help

# Variables
BINARY_NAME=snapshot_migrate
BINARY_PATH=bin/$(BINARY_NAME)
GO=go
GOFLAGS=-v

# Default target
all: build

# Build the migration tool
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	$(GO) build $(GOFLAGS) -o $(BINARY_PATH) ./cmd/snapshot_migrate
	@echo "Build complete: $(BINARY_PATH)"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -rf snapshot_data/
	@echo "Clean complete"

# Run tests
test:
	@echo "Running tests..."
	$(GO) test -v ./pkg/migration/...

# Test providers (download samples)
test-providers:
	@echo "Testing snapshot providers..."
	chmod +x scripts/test_providers.sh
	./scripts/test_providers.sh 20000000 20001000

# Dry run migration (no import)
migrate-dry: build
	@echo "Running dry migration (no import)..."
	$(BINARY_PATH) \
		--provider aws \
		--start 20000000 \
		--end 20010000 \
		--batch 1000 \
		--workers 4 \
		--dry-run \
		--output ./snapshot_data

# Test migration with validation
migrate-test: build
	@echo "Running test migration with validation..."
	$(BINARY_PATH) \
		--provider aws \
		--start 20000000 \
		--end 20001000 \
		--batch 500 \
		--workers 2 \
		--validate \
		--validate-sample 50 \
		--output ./snapshot_data

# Full migration (use with caution!)
migrate: build
	@echo "Starting full migration..."
	@echo "WARNING: This will import all blocks. Press Ctrl+C to cancel."
	@sleep 3
	$(BINARY_PATH) \
		--provider aws \
		--start 0 \
		--batch 5000 \
		--workers 8 \
		--output ./snapshot_data

# Resume migration from checkpoint
migrate-resume: build
	@echo "Resuming migration from checkpoint..."
	chmod +x scripts/migrate.sh
	./scripts/migrate.sh --resume

# Download AWS sample data
download-sample:
	@echo "Downloading sample AWS data..."
	@mkdir -p snapshot_data/aws/blocks snapshot_data/aws/transactions snapshot_data/aws/logs
	aws s3 cp s3://aws-public-blockchain/v1.0/eth/blocks/date=2024-01-01/ \
		snapshot_data/aws/blocks/ --recursive --no-sign-request
	aws s3 cp s3://aws-public-blockchain/v1.0/eth/transactions/date=2024-01-01/ \
		snapshot_data/aws/transactions/ --recursive --no-sign-request
	aws s3 cp s3://aws-public-blockchain/v1.0/eth/logs/date=2024-01-01/ \
		snapshot_data/aws/logs/ --recursive --no-sign-request
	@echo "Download complete!"

# Install dependencies
deps:
	@echo "Installing dependencies..."
	$(GO) mod tidy
	$(GO) mod download
	@echo "Dependencies installed"

# Show help
help:
	@echo "Ethereum Snapshot Migration Tool"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build           Build the migration binary"
	@echo "  clean           Remove build artifacts and downloaded data"
	@echo "  test            Run unit tests"
	@echo "  test-providers  Test all snapshot providers with sample data"
	@echo "  migrate-dry     Run migration without importing (dry run)"
	@echo "  migrate-test    Run small test migration with validation"
	@echo "  migrate         Run full migration (use with caution!)"
	@echo "  migrate-resume  Resume migration from last checkpoint"
	@echo "  download-sample Download sample AWS data for testing"
	@echo "  deps            Install Go dependencies"
	@echo "  help            Show this help message"
	@echo ""
	@echo "Environment Variables:"
	@echo "  ETH_RPC_URL     Ethereum RPC URL for validation"
	@echo "  DEFRADB_URL     DefraDB URL for import"
	@echo ""
	@echo "Examples:"
	@echo "  make build && make migrate-dry"
	@echo "  make migrate-test"
	@echo "  ETH_RPC_URL=http://localhost:8545 make migrate-test"
# Migration targets - add these to your existing Makefile

.PHONY: build-migrate migrate-test migrate-dry migrate-100 migrate-1000

# Build the migration tool
build-migrate:
	@echo "Building snapshot_migrate..."
	go build -v -o bin/snapshot_migrate ./cmd/snapshot_migrate

# Test migration with 100 blocks (measures import time separately from download)
migrate-test: build-migrate
	@echo "Running migration test with 100 blocks..."
	@echo "This will show download time vs import time separately"
	./bin/snapshot_migrate \
		--provider aws \
		--start 20000000 \
		--end 20000099 \
		--batch 100 \
		--workers 4 \
		--output ./snapshot_data \
		--defra-data ./data/defra-migrate-test

# Dry run (download only, no import)
migrate-dry: build-migrate
	@echo "Running dry migration (download only)..."
	./bin/snapshot_migrate \
		--provider aws \
		--start 20000000 \
		--end 20000099 \
		--batch 100 \
		--dry-run \
		--output ./snapshot_data

# Test with 100 blocks
migrate-100: build-migrate
	@echo "Running migration with 100 blocks..."
	./bin/snapshot_migrate \
		--provider aws \
		--start 20000000 \
		--end 20000099 \
		--batch 100 \
		--workers 4 \
		--output ./snapshot_data \
		--defra-data ./data/defra-migrate-test

# Test with 1000 blocks
migrate-1000: build-migrate
	@echo "Running migration with 1000 blocks..."
	./bin/snapshot_migrate \
		--provider aws \
		--start 20000000 \
		--end 20000999 \
		--batch 500 \
		--workers 8 \
		--output ./snapshot_data \
		--defra-data ./data/defra-migrate-test

# Clean migration test data
migrate-clean:
	rm -rf ./data/defra-migrate-test
	rm -rf ./snapshot_data
