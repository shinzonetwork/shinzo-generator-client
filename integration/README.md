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

### BSC

```bash
# Optional: point at your own BSC node (defaults to the free public endpoint
# https://bsc-dataseed.bnbchain.org when unset)
export GETH_RPC_URL=https://your-bsc-node:8545

# Run the suite (also enabled by BSC_LIVE=1 alone)
make bsc-live-test
```

End-to-end tests against live BSC mainnet (or `BSC_LIVE_NETWORK`), exercising the
same EVM pipeline used for Ethereum — chain identity comes purely from config
(`BSC__Mainnet__*` collections).

Environment variables:

| Variable | Purpose |
|----------|---------|
| `GETH_RPC_URL` | HTTP JSON-RPC endpoint (default: public `https://bsc-dataseed.bnbchain.org`). `GETH_*` names are historical — the Generator is chain-agnostic, so the BSC suite reuses them |
| `GETH_WS_URL` | WebSocket endpoint; opt-in (`TestLiveBSCWSNotificationIndexing` skips without it) |
| `GETH_API_KEY` / `GETH_API_KEY_TYPE` | API key + header type for authenticated endpoints |
| `BSC_LIVE_NETWORK` | Network name used in collection names (default `Mainnet`) |
| `BSC_LIVE` | Any value enables `make bsc-live-test` without a custom RPC URL |

Skip semantics: the public default endpoint is aggressively rate-limited, so the
suite waits up to 180 s for the first indexed block and **exits 0** (treated as
skipped) on timeout — rate limiting never fails the run. Tests that need
indexed data skip when the indexer did not warm up.


