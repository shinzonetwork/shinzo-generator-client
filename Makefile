.PHONY: deps env build start start-bsc clean defradb gitpush test testrpc coverage playground stop integration-test bsc-live-test bsc-bench-fetch bsc-acceptance-test docker-build docker-up docker-down deploy lint lint-fix fmt node-status test-local help

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
	go build -o bin/fetch_blocks cmd/fetch_blocks/main.go
	@if [ "$(VERSION)" = "dev" ]; then \
		echo "⚠️  VERSION fell back to 'dev' (no git tags or not a git repo)"; \
	elif grep -aFq "$(VERSION)" bin/block_poster; then \
		echo "✅ version injected: $(VERSION)"; \
	else \
		echo "❌ version injection failed: binary does not contain '$(VERSION)' — check the -X symbol path"; exit 1; \
	fi

start:
	./bin/block_poster

start-bsc:
	./bin/block_poster -config config/config_bsc.yaml
	
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
	@go test tags=integration -v ./integration/
	@echo ""
	@echo "🌐 Live tests (requires environment variables):"
	@if [ -n "$(GETH_RPC_URL)" ]; then \
		go test tags=live -v ./integration/live/ -timeout=20s; \
	else \ 
		echo "⚠️  Skipping live tests - GETH_RPC_URL not set"; \
	fi

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# bsc-live-test runs the BSC live integration suite. It is guarded on
# GETH_RPC_URL/BSC_LIVE so CI (which sets neither) never hammers the public
# BSC endpoint by accident; without GETH_RPC_URL the suite itself falls back
# to the free public endpoint. GETH_* env names are historical — the
# Generator is chain-agnostic, so the BSC suite reuses them.
bsc-live-test:
	@if [ -z "$(GETH_RPC_URL)" ] && [ -z "$(BSC_LIVE)" ]; then \
		echo "⚠️  Skipping BSC live tests - GETH_RPC_URL or BSC_LIVE not set"; \
	else \
		go test -tags=live -v ./integration/live/bsc/ -timeout=400s; \
	fi

# bsc-bench-fetch captures a raw replay fixture via the chain-agnostic
# cmd/fetch_blocks CLI (one-time; needs a real endpoint). FROM/TO are
# overridable block numbers; the default captures 100 blocks ending at the
# current tip. Fixtures land in benchmarking/testdata/ (gitignored) as
# bsc_blocks_<from>_<to>.json.
bsc-bench-fetch:
	@if [ -z "$(GETH_RPC_URL)" ]; then \
		echo "❌ GETH_RPC_URL not set - fixture capture needs a JSON-RPC endpoint"; \
		exit 1; \
	fi
	@mkdir -p benchmarking/testdata
	go run ./cmd/fetch_blocks --chain bsc --network mainnet --from $(FROM) --to $(TO)

# bsc-acceptance-test replays the newest captured fixture through the full
# production pipeline (mock JSON-RPC → Fetcher → Converter → BlockHandler.Store)
# and hard-asserts the average per-block processing time stays within the
# chain's block interval. BSC_TARGET_BLOCK_TIME overrides the target; a
# missing fixture skips with regeneration instructions. -count=1 forces the
# run every time: Go's test cache key ignores runtime inputs (fixture files,
# BSC_* env vars), and unlike `go clean -testcache` it leaves the rest of the
# project's test results cached.
bsc-acceptance-test:
	go test -tags=acceptance ./benchmarking/bsc -run TestBSCReplayAcceptance -count=1 -v -timeout 30m

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
	@echo "  start-bsc          - Start the generator with the BSC config"
	@echo "  stop               - Stop all services"
	@echo ""
	@echo "🌐 Per-chain live tests:"
	@echo "  bsc-live-test      - BSC live integration suite (GETH_RPC_URL or BSC_LIVE; defaults to the public endpoint)"
	@echo ""
	@echo "⏱  BSC tip-indexing acceptance (replay):"
	@echo "  bsc-bench-fetch    - Capture a replay fixture (FROM/TO blocks; requires GETH_RPC_URL)"
	@echo "  bsc-acceptance-test- Replay the fixture and assert avg block time <= the chain's block interval"
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
