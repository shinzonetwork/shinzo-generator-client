# Integration Tests

## Mock Tests
```bash
make integration-test
```
Self-contained suite with synthetic mock data (build tag `integration`). No
chain endpoint, no credentials, no external dependencies.

## Live Tests
End-to-end tests with real chain data. Both are gated on `GETH_RPC_URL`. The
Ethereum suite additionally requires `GETH_WS_URL` and `GETH_API_KEY`; for BSC
they are optional (see its table below).

```bash
# Set environment variables first
source .env
```

### Ethereum

```bash
# Run with build tag
make ethereum-live-test
```

### BSC

```bash
# Run with build tag
make bsc-live-test
```

End-to-end tests against live BSC mainnet (or `BSC_LIVE_NETWORK`), exercising the
same EVM pipeline used for Ethereum — chain identity comes purely from config
(`BSC__Mainnet__*` collections).

The suite boots from the bundled `config/config_bsc.yaml` — the same file
operators run via `block_poster -config config/config_bsc.yaml` — so production
tuning (badger store settings, batch sizes, `blocks_per_minute`, pruner and
snapshot lifecycle) is exercised as shipped, along with the standard
`GETH_*` / `DEFRADB_*` / `INDEXER_*` / `SCHEMA_AUTH_MODE` environment override
machinery. Only suite-specific knobs are applied after load: store path
(`./.defra`, wiped each run), P2P off, throwaway keyring secret fallback, health
port `9877`, and schema auth `none` (token mode is fail-closed without
`SCHEMA_API_KEYS`, and the suite asserts the schema endpoint's contents).

A stray `CHAIN_NAME` in the environment (e.g. left over from Ethereum work)
fails the run loudly before the indexer starts — the suite never silently
indexes a different chain.

| Variable | Purpose |
|----------|---------|
| `GETH_RPC_URL` | HTTP JSON-RPC endpoint (required; no public fallback). `GETH_*` names are historical — the Generator is chain-agnostic, so the BSC suite reuses them |
| `GETH_WS_URL` | WebSocket endpoint; opt-in (`TestLiveBSCWSNotificationIndexing` skips without it) |
| `GETH_API_KEY` / `GETH_API_KEY_TYPE` | API key + header type for authenticated endpoints |
| `BSC_LIVE_NETWORK` | Network override applied after load (default: the yaml's `Mainnet`) |

Skip semantics: with `GETH_RPC_URL` unset the suite skips before starting
anything (exit 0, same as the Ethereum suite). Once an endpoint is configured,
the suite waits up to 180 s for the first indexed block and **exits 1** on
timeout — with no public fallback to blame, a rate-limited or stalled provider
is a real failure, like on Ethereum. Tests that need indexed data skip when the
indexer did not warm up.


