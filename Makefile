.PHONY: deps env build start clean defradb gitpush test test-branchable testrpc coverage bootstrap playground stop integration-test docker-build docker-up docker-down deploy

# Load environment variables from .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

GETH_RPC_URL ?=
GETH_WS_URL ?=
GETH_API_KEY ?=

build:
	go build -o bin/block_poster cmd/block_poster/main.go

build-branchable:
	go build -tags branchable -o bin/block_poster cmd/block_poster/main.go

start:
	./bin/block_poster

clean:
	rm -rf bin/ && rm -r logs/logfile && touch logs/logfile

geth-status:
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

test-branchable:
	go test -tags branchable ./pkg/schema/ -v -coverprofile=schema_branch.out
	@echo ""
	@echo "Coverage:"
	@go tool cover -func=schema_branch.out | tail -1

test-local:
	@echo "🧪 Running local indexer test with Geth endpoint..."
	@if [ -z "$(GETH_RPC_URL)" ]; then \
		echo "❌ GETH_RPC_URL not set. Please export it first:"; \
		echo "   export GETH_RPC_URL=<your-geth-url>"; \
		exit 1; \
	fi
	@echo "✅ Using Geth endpoint: $(GETH_RPC_URL)"
	@go test ./pkg/indexer -v -run TestIndexing

integration-test:
	@echo "🧪 Running integration tests..."
	@echo "📦 Mock tests (fast):"
	@go test -v ./integration/
	@echo ""
	@echo "🌐 Live tests (requires environment variables):"
	@if [ -n "$(GETH_RPC_URL)" ]; then \
		go test -tags live -v ./integration/live/ -timeout=20s; \
	else \
		echo "⚠️  Skipping live tests - GETH_RPC_URL not set"; \
	fi

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

bootstrap:
	@if [ -z "$(DEFRA_PATH)" ]; then \
		echo "ERROR: You must pass DEFRA_PATH. Usage:"; \
		echo "  make bootstrap DEFRA_PATH=../path/to/defradb"; \
		exit 1; \
	fi
	@scripts/bootstrap.sh "$(DEFRA_PATH)" "$(PLAYGROUND)"

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
	@echo "🚀 Shinzo Network Indexer - Available Make Targets"
	@echo "=================================================="
	@echo ""
	@echo "📦 Build & Test:"
	@echo "  build              - Build the indexer binary"
	@echo "  test               - Run all tests with summary"
	@echo "  clean              - Clean build artifacts"
	@echo ""
	@echo "🔗 Connectivity Testing:"
	@echo "  geth-status        - Comprehensive Geth node diagnostics"
	@echo "  defra-status       - Check DefraDB status"
	@echo ""
	@echo "🏃 Services:"
	@echo "  defra-start        - Start DefraDB"
	@echo "  start              - Start the indexer"
	@echo "  stop               - Stop all services"
	@echo ""
	@echo "🔧 Environment Variables for geth-status:"
	@echo "  GETH_RPC_URL   - HTTP RPC endpoint (required)"
	@echo "  GETH_API_KEY   - API key for authentication (optional)"
	@echo "  GETH_WS_URL    - WebSocket endpoint (optional)"
	@echo ""
	@echo "💡 Example Usage:"
	@echo "  export GETH_RPC_URL=http://xx.xx.xx.xx:port"
	@echo "  export GETH_API_KEY=your-api-key-here"
	@echo "  make geth-status"
