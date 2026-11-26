.PHONY: deps env tron-build evm-build build defradb gitpush testrpc playground docker-build docker-up docker-down deploy help tron-live-test tron-golden-test tron-acceptance-test tron-replay-fetch tron-start

# Load environment variables from .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# Capture range for replay-fetch. 0 = auto: latest 100 blocks from the
# current chain tip. Without these defaults a bare `make replay-fetch`
# would expand to a bare `--from --to` and the flag package would eat the
# next token as the value, failing the run.
FROM ?= 0
TO ?= 0
# Pinned block height for tron-golden-fetch. Empty = the capture script
# scans backwards from the tip for a suitable block.
HEIGHT ?=
# Replace switch for tron-golden-fetch. Empty = the capture script refuses
# to run when golden/tron/testdata already holds a fixture; FORCE=1 makes a
# recapture replace the existing pair (removed only after the new block
# qualifies).
FORCE ?=
# Tron wallet-API env vars for the tron live smoke (tron-live-test and the
# TRON phase of integration-test). The suite boots the bundled production
# config/config_tron.yml through the standard loader, so CHAIN_NETWORK and
# the TRON_* variables override the yaml the same way they do for a deploy.
TRON_RPC_URL ?=
TRON_SOLIDITY_RPC_URL ?=
TRON_API_KEY ?=

# Version injected into the binary at build time via -ldflags (git tag plus
# commit offset and dirty state; falls back to "dev" outside a git repo).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# One generator binary per chain adapter (see specs/features/BINARY-SPLIT.md):
# the evm adapter produces bin/block_poster_evm. LDFLAGS keeps the version
# injection identical between evm-build and any future per-chain build target.
LDFLAGS := -ldflags "-X github.com/shinzonetwork/shinzo-generator-client/config.Version=$(VERSION)"

build: evm-build tron-build
	go build -o bin/fetch_blocks ./cmd/fetch_blocks/

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

tron-start:
	./bin/block_poster_tron -config config/config_tron.yml

tron-golden-test:
	go test -tags=integration ./golden/... -run 'TestGoldenTypedPath/tron' -count=1 -v

tron-live-test:
	@echo "🌅 Running tron live smoke test with your wallet-API endpoint..."
	@if [ -z "$(TRON_RPC_URL)" ]; then \
		echo "❌ TRON_RPC_URL not set. Please export it first:"; \
		echo "   export TRON_RPC_URL=<your-wallet-api-url>  (for example https://nile.trongrid.io)"; \
		exit 1; \
	fi
	@echo "✅ Using endpoint: $(TRON_RPC_URL)"
	@go test -tags=live -v ./integration/live/tron/ -timeout=120s

# tron-acceptance-test replays the newest captured fixture through the full
# production pipeline (mock wallet API → Fetcher → Converter →
# BlockHandler.Store) and hard-asserts the effective per-block interval
# stays within Tron's 3s block interval. TEST_TRON_TARGET_BLOCK_TIME
# overrides the target; a missing fixture skips with regeneration instructions.
# -count=1 forces the run every time, mirroring the ethereum/bsc suites.
tron-acceptance-test:
	go test -tags=acceptance ./replay/tron -run TestTronReplayAcceptance -count=1 -v -timeout 30m

# tron-replay-fetch captures a raw replay fixture over the Tron wallet API
# via cmd/fetch_blocks --adapter tron (one-time; needs a TronGrid or
# java-tron endpoint). FROM/TO are overridable block numbers; the default
# captures 100 blocks ending at the current tip. Fixtures land in
# replay/testdata/ (gitignored) as tron_blocks_<from>_<to>.json. TronGrid
# serves full block and receipt history by height, so the archival-node
# caveat of the EVM capture does not apply.
tron-replay-fetch:
	@if [ -z "$(TRON_RPC_URL)" ]; then \
		echo "❌ TRON_RPC_URL not set - fixture capture needs a Tron wallet-API endpoint"; \
		exit 1; \
	fi
	@mkdir -p replay/testdata
	go run ./cmd/fetch_blocks --chain tron --network mainnet --adapter tron --from $(FROM) --to $(TO)

# tron-acceptance-test replays the newest captured fixture through the full
# production pipeline (mock wallet API → Fetcher → Converter →
# BlockHandler.Store) and hard-asserts the effective per-block interval
# stays within Tron's 3s block interval. TEST_TRON_TARGET_BLOCK_TIME
# overrides the target; a missing fixture skips with regeneration instructions.
# -count=1 forces the run every time, mirroring the ethereum/bsc suites.
tron-acceptance-test:
	go test -tags=acceptance ./replay/tron -run TestTronReplayAcceptance -count=1 -v -timeout 30m

# tron-golden-fetch captures one suitable mainnet block for the golden
# freeze via scripts/fetch_tron_golden.sh (one-time; TRON_RPC_URL required,
# TRON_API_KEY optional). It scans back from the tip for a block exercising
# the full pipeline (both contract outcomes, logs, internal transfers with
# a non-zero callValue, complete pinned receipts) and writes the verbatim
# wallet-API bodies into golden/tron/testdata/. HEIGHT pins a block and
# skips the scan; a pinned block is still validated. The freeze is
# single-block: the script refuses when a fixture exists and FORCE=1
# replaces it (the old pair is removed only after the new block qualifies).
tron-golden-fetch:
	@if [ -z "$(TRON_RPC_URL)" ]; then \
		echo "❌ TRON_RPC_URL must be set - golden capture needs a Tron wallet-API endpoint (the API key is optional; providers like Alchemy embed it in the URL)"; \
		exit 1; \
	fi
	@FORCE="$(FORCE)" HEIGHT="$(HEIGHT)" ./scripts/fetch_tron_golden.sh



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
