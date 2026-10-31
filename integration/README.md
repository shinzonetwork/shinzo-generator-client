# Integration Tests

## Mock Tests
```bash
go test -v ./integration/
```
Fast tests with mock data. No external dependencies.

## Live Tests  
```bash
# Set environment variables first
source .env

# Run with build tag
make integration-test
```
End-to-end tests with real Ethereum data. Requires `GETH_RPC_URL`, `GETH_WS_URL`, `GETH_API_KEY`.

## Solana Mock Suite
```bash
go test -tags=integration -v ./integration/solana/
```
Fast tests against a scripted JSON-RPC chain fake: startup and field assertions, skipped-slot advancement (a null getBlock below the confirmed tip must advance the indexer without a false reorg), duplicate-content instruction docIDs (distinct documents split by the positional ordinals), and restart/resume from the DB tip with no refetch below the resume height. A general fork-flip case is deliberately absent: the generic walk-back resolves chain-side ancestor headers by hash, and the Solana endpoint offers no header-by-hash RPC (the adapter serves only headers it fetched itself), so a flip's uncached discriminator cannot resolve on the Solana adapter; see the Phase 6 note in specs/features/SOLANA-INTEGRATION.md.

## Solana Live Smoke
```bash
make solana-live-test
```
Runs against a real Solana JSON-RPC endpoint when `SOLANA_RPC_URL` is set (default suggestion: the public devnet `https://api.devnet.solana.com`); the chain network segment comes from `SOLANA_LIVE_NETWORK` (Devnet default, `Solana__Devnet__*` collections). Waits for a few real slots, asserts block and transaction field integrity plus one signed BlockSignature per stored slot, verifies the slot count only advances across an idle window (devnet produces skipped slots constantly), and proves the adapter wires a real endpoint into the whole pipeline (not throughput). When `SOLANA_RPC_URL` is unset the `integration-test` target skips it with exit 0.


