.PHONY: deps env tron-build evm-build build defradb gitpush testrpc playground docker-build docker-up docker-down deploy help tron-live-test tron-golden-test tron-acceptance-test tron-replay-fetch tron-start

# Load environment variables from .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif


# Version injected into the binary at build time via -ldflags (git tag plus
# commit offset and dirty state; falls back to "dev" outside a git repo).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# One generator binary per chain adapter (see specs/features/BINARY-SPLIT.md):
# the evm adapter produces bin/block_poster_evm. LDFLAGS keeps the version
# injection identical between evm-build and any future per-chain build target.
LDFLAGS := -ldflags "-X github.com/shinzonetwork/shinzo-generator-client/config.Version=$(VERSION)"

build: evm-build tron-build

evm-build:
	go build $(LDFLAGS) -o bin/block_poster_evm ./cmd/block_poster/evm/
	@if [ "$(VERSION)" = "dev" ]; then \
		echo "⚠️  VERSION fell back to 'dev' (no git tags or not a git repo)"; \
	elif grep -aFq "$(VERSION)" bin/block_poster_evm; then \
		echo "✅ version injected: $(VERSION)"; \
	else \
		echo "❌ version injection failed: binary does not contain '$(VERSION)' — check the -X symbol path"; exit 1; \
	fi

tron-build:
	go build $(LDFLAGS) -o bin/block_poster_tron ./cmd/block_poster/tron/
	@if [ "$(VERSION)" = "dev" ]; then \
		echo "⚠️  VERSION fell back to 'dev' (no git tags or not a git repo)"; \
	elif grep -aFq "$(VERSION)" bin/block_poster_tron; then \
		echo "✅ version injected: $(VERSION)"; \
	else \
		echo "❌ version injection failed: binary does not contain '$(VERSION)' - check the -X symbol path"; exit 1; \
	fi


playground:
	@if [ -z "$(DEFRA_PATH)" ]; then \
		echo "ERROR: You must pass DEFRA_PATH. Usage:"; \
		echo "  make playground DEFRA_PATH=../path/to/defradb"; \
		exit 1; \
	fi
	@$(MAKE) bootstrap PLAYGROUND=1 DEFRA_PATH="$(DEFRA_PATH)"

help:
	@echo "🚀 Shinzo Network Generator - Available Make Targets"
	@echo "=================================================="
	@echo ""
	@echo "📦 Build:"
	@echo "  build              - Build all generator binaries"
	@echo "  evm-build          - Build the EVM generator binary"
	@echo "  tron-build         - Build the Tron generator binary"
	@echo ""
	@echo "🔗 Connectivity Testing:"
	@echo "  defra-status       - Check DefraDB status"
	@echo ""
	@echo "🏃 Services:"
	@echo "  defra-start        - Start DefraDB"
	@echo ""
