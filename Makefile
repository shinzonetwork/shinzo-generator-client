.PHONY: deps env build defradb gitpush testrpc playground docker-build docker-up docker-down deploy help 

# Load environment variables from .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# Version injected into the binary at build time via -ldflags (git tag plus
# commit offset and dirty state; falls back to "dev" outside a git repo).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X github.com/shinzonetwork/shinzo-generator-client/config.Version=$(VERSION)" -o bin/block_poster ./cmd/block_poster/
	@if [ "$(VERSION)" = "dev" ]; then \
		echo "⚠️  VERSION fell back to 'dev' (no git tags or not a git repo)"; \
	elif grep -aFq "$(VERSION)" bin/block_poster; then \
		echo "✅ version injected: $(VERSION)"; \
	else \
		echo "❌ version injection failed: binary does not contain '$(VERSION)' — check the -X symbol path"; exit 1; \
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
	@echo "  build              - Build the generator binary"
	@echo ""
	@echo "🔗 Connectivity Testing:"
	@echo "  defra-status       - Check DefraDB status"
	@echo ""
	@echo "🏃 Services:"
	@echo "  defra-start        - Start DefraDB"
	@echo ""