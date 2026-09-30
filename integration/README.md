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

## Solana Live Tests
`integration/live/solana/` starts the real generator with `CHAIN_ADAPTER=solana`
against a live Solana JSON-RPC endpoint, waits for the first indexed slot, then
exercises the GraphQL collections, block signatures, and health endpoints.

```bash
# Public devnet (no account needed; aggressively rate-limited):
SOLANA_LIVE=1 make solana-live-test

# Paid endpoint / mainnet for realistic block volumes:
export SOLANA_RPC_URL=https://<paid-endpoint>
export SOLANA_API_KEY=... # optional header auth
export SOLANA_LIVE_NETWORK=Mainnet   # collection prefix Solana__Mainnet__*
make solana-live-test
```

- `SOLANA_RPC_URL` — Solana RPC endpoint; defaults to
  `https://api.devnet.solana.com` (devnet) when unset.
- `SOLANA_LIVE_NETWORK` — `Devnet` (default) or `Mainnet`; determines the
  collection prefix (`Solana__Devnet__*` / `Solana__Mainnet__*`).
- The suite skips (exit 0, no failure) when no slot is indexed within the
  180 s warmup budget, so devnet rate-limit storms degrade gracefully.

