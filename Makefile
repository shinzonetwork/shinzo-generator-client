.PHONY: deps env build start clean defradb gitpush test testrpc coverage playground stop integration-test docker-build docker-up docker-down deploy lint lint-fix fmt node-status test-local help solana-bench-fetch solana-bench-replay solana-bench-synthetic

# Load environment variables from .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# The GETH_* env var names below are historical. The Generator is chain-agnostic and accepts any compatible JSON-RPC and WebSocket endpoint, not just Geth.
GETH_RPC_URL ?=
GETH_WS_URL ?=
GETH_API_KEY ?=

# Version injected into the binary at build time via -ldflags (git tag plus
# commit offset and dirty state; falls back to "dev" outside a git repo).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X github.com/shinzonetwork/shinzo-generator-client/pkg/indexer.Version=$(VERSION)" -o bin/block_poster cmd/block_poster/main.go
	@if [ "$(VERSION)" = "dev" ]; then \
		echo "⚠️  VERSION fell back to 'dev' (no git tags or not a git repo)"; \
	elif grep -aFq "$(VERSION)" bin/block_poster; then \
		echo "✅ version injected: $(VERSION)"; \
	else \
		echo "❌ version injection failed: binary does not contain '$(VERSION)' — check the -X symbol path"; exit 1; \
	fi

start:
	./bin/block_poster

clean:
	rm -rf bin/ && rm -r logs/logfile.log && touch logs/logfile.log

# node-status probes an Ethereum-compatible JSON-RPC endpoint via eth_blockNumber. The Generator itself is chain-agnostic; see https://docs.shinzo.network/generator/overview.
node-status:
	@if [ -z "$(GETH_RPC_URL)" ]; then \
		echo "❌ GETH_RPC_URL not set"; \
		exit 1; \
	fi
	@BLOCK_RESPONSE=$$(curl -s -X POST -H "Content-Type: application/json" \
		-H "X-goog-api-key: $(GETH_API_KEY)" \
		--data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
		$(GETH_RPC_URL) 2>/dev/null); \
	if echo "$$BLOCK_RESPONSE" | jq -e '.result' >/dev/null 2>&1; then \
		BLOCK_HEX=$$(echo "$$BLOCK_RESPONSE" | jq -r '.result'); \
		BLOCK_NUM=$$(printf "%d" $$BLOCK_HEX 2>/dev/null || echo "unknown"); \
		echo "✅ Block: $$BLOCK_NUM"; \
	else \
		echo "❌ Failed: $$BLOCK_RESPONSE"; \
	fi

test:
	@echo "🧪 Running all tests with summary output..."
	@go test ./... -v -count=1 | tee /tmp/test_output.log; \
	exit_code=$$?; \
	echo ""; \
	echo "📊 TEST SUMMARY:"; \
	echo "================"; \
	if [ $$exit_code -eq 0 ]; then \
		echo "✅ ALL TESTS PASSED"; \
		echo "📈 Passed packages:"; \
		grep "^ok" /tmp/test_output.log | sed 's/^/  ✓ /'; \
	else \
		echo "❌ SOME TESTS FAILED (Exit Code: $$exit_code)"; \
		echo ""; \
		echo "📈 Passed packages:"; \
		grep "^ok" /tmp/test_output.log | sed 's/^/  ✓ /' || echo "  (none)"; \
		echo ""; \
		echo "❌ Failed packages:"; \
		grep "^FAIL" /tmp/test_output.log | sed 's/^/  ✗ /' || echo "  (check output above for details)"; \
		echo ""; \
		echo "🔍 Failed test details:"; \
		grep -A 5 -B 1 "FAIL:" /tmp/test_output.log | sed 's/^/  /' || echo "  (check full output above)"; \
	fi; \
	echo ""; \
	rm -f /tmp/test_output.log; \
	exit $$exit_code

test-local:
	@echo "🧪 Running local generator test with your blockchain node endpoint..."
	@if [ -z "$(GETH_RPC_URL)" ]; then \
		echo "❌ GETH_RPC_URL not set. Please export it first:"; \
		echo "   export GETH_RPC_URL=<your-node-url>"; \
		exit 1; \
	fi
	@echo "✅ Using node endpoint: $(GETH_RPC_URL)"
	@go test ./pkg/indexer -v -run TestIndexing

integration-test:
	@echo "🧪 Running integration tests..."
	@echo "📦 Mock tests (fast):"
	@go test -tags=integration -v ./integration/
	@echo ""
	@echo "🌐 Live tests (requires environment variables):"
	@if [ -n "$(GETH_RPC_URL)" ]; then \
		go test -tags=live -v ./integration/live/ -timeout=20s; \
	else \
		echo "⚠️  Skipping live tests - GETH_RPC_URL not set"; \
	fi

# Solana live integration suite: RPC from SOLANA_RPC_URL (defaults to the
# public devnet endpoint inside the suite) or a paid endpoint plus
# SOLANA_LIVE_NETWORK=Mainnet for realistic volumes.
.PHONY: solana-live-test
solana-live-test:
	@echo "🧪 Running solana live integration tests..."
	@if [ -z "$(SOLANA_RPC_URL)" ] && [ -z "$(SOLANA_LIVE)" ]; then \
		echo "⚠️  Set SOLANA_RPC_URL (paid endpoint) or SOLANA_LIVE=1 (public devnet) to run"; \
		exit 1; \
	fi
	@go test -tags=live -v ./integration/live/solana/ -timeout=400s

# Replay benchmark: captures a real mainnet slot range (SOLANA_RPC_URL from
# .env) and times the full production pipeline against the 100ms/block
# target. FROM/TO are overridable: make solana-bench-fetch FROM=... TO=...
FROM_SLOT ?= 449791000
TO_SLOT ?= 449791099

.PHONY: solana-bench-fetch
solana-bench-fetch:
	@echo "⬇️  Capturing Solana blocks $(FROM_SLOT)..$(TO_SLOT) into the replay fixture..."
	@go run ./cmd/bench_fetch --from $(FROM_SLOT) --to $(TO_SLOT)

.PHONY: solana-bench-replay
solana-bench-replay:
	@echo "🏁 Running the Solana replay benchmark (target: 100ms/block avg)..."
	@go clean -testcache 
	@go test -tags bench ./benchmarking/solana -run 'TestSolanaReplayProcessingBenchmark' -v -timeout 30m

.PHONY: solana-bench-synthetic
solana-bench-synthetic:
	@echo "⛰️  Running the synthetic store/index benchmarks..."
	@go test -tags bench ./benchmarking/solana -run '^$' -bench 'BenchmarkSolana(StoreSlot|StoreHotSlotBatch|IndexSlots)' -benchtime 5x -timeout 30m

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

lint:
	@echo "🔍 Running golangci-lint..."
	@golangci-lint run ./...
	@echo "📏 Checking file sizes..."
	@./scripts/check_loc.sh

lint-fix:
	@echo "🔧 Running golangci-lint with auto-fix..."
	@golangci-lint run --fix ./...

fmt:
	@echo "📝 Formatting code..."
	@gofmt -s -w .
	@goimports -w -local github.com/shinzonetwork/shinzo-generator-client .

playground:
	@if [ -z "$(DEFRA_PATH)" ]; then \
		echo "ERROR: You must pass DEFRA_PATH. Usage:"; \
		echo "  make playground DEFRA_PATH=../path/to/defradb"; \
		exit 1; \
	fi
	@$(MAKE) bootstrap PLAYGROUND=1 DEFRA_PATH="$(DEFRA_PATH)"

stop:
	@echo "===> Stopping defradb if running..."
	@DEFRA_ROOTDIR="$(shell pwd)/.defra"; \
	DEFRA_PIDS=$$(ps aux | grep '[d]efradb start --rootdir ' | grep "$$DEFRA_ROOTDIR" | awk '{print $$2}'); \
	if [ -n "$$DEFRA_PIDS" ]; then \
	  echo "Killing defradb PIDs: $$DEFRA_PIDS"; \
	  echo "$$DEFRA_PIDS" | xargs -r kill -9 2>/dev/null; \
	  echo "Stopped all defradb processes using $$DEFRA_ROOTDIR"; \
	else \
	  echo "No defradb processes found for $$DEFRA_ROOTDIR"; \
	fi; \
	rm -f .defra/defradb.pid;
	@echo "===> Stopping block_poster if running..."
	@BLOCK_PIDS=$$(ps aux | grep '[b]lock_poster' | awk '{print $$2}'); \
	if [ -n "$$BLOCK_PIDS" ]; then \
	  echo "Killing block_poster PIDs: $$BLOCK_PIDS"; \
	  echo "$$BLOCK_PIDS" | xargs -r kill -9 2>/dev/null; \
	  echo "Stopped all block_poster processes"; \
	else \
	  echo "No block_poster processes found"; \
	fi; \
	rm -f .defra/block_poster.pid;

help:
	@echo "🚀 Shinzo Network Generator - Available Make Targets"
	@echo "=================================================="
	@echo ""
	@echo "📦 Build & Test:"
	@echo "  build              - Build the generator binary"
	@echo "  test               - Run all tests with summary"
	@echo "  coverage           - Run tests with coverage report"
	@echo "  clean              - Clean build artifacts"
	@echo ""
	@echo "🔍 Code Quality:"
	@echo "  lint               - Run golangci-lint"
	@echo "  lint-fix           - Run golangci-lint with auto-fix"
	@echo "  fmt                - Format code with gofmt and goimports"
	@echo ""
	@echo "🔗 Connectivity Testing:"
	@echo "  node-status        - Check blockchain node connectivity and current block"
	@echo "  defra-status       - Check DefraDB status"
	@echo ""
	@echo "🏃 Services:"
	@echo "  defra-start        - Start DefraDB"
	@echo "  start              - Start the generator"
	@echo "  stop               - Stop all services"
	@echo ""
	@echo "🔧 Environment Variables for node-status:"
	@echo "  GETH_RPC_URL   - HTTP RPC endpoint (required)"
	@echo "  GETH_API_KEY   - API key for authentication (optional)"
	@echo "  GETH_WS_URL    - WebSocket endpoint (optional)"
	@echo "  (GETH_* names are historical; any compatible JSON-RPC/WS endpoint works)"
	@echo ""
	@echo "💡 Example Usage:"
	@echo "  export GETH_RPC_URL=http://xx.xx.xx.xx:port"
	@echo "  export GETH_API_KEY=your-api-key-here"
	@echo "  make node-status"
