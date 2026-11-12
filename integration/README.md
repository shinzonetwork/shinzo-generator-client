# Integration Tests

## Mock Tests
```bash
# Ethereum and Bitcoin suites in one run
make integration-test

# Or per chain
make ethereum-integration-test
make bitcoin-integration-test
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
make integration-test

# Or per chain
make ethereum-integration-test
make bitcoin-integration-test
```
End-to-end live tests. The Ethereum legs require `GETH_RPC_URL`, `GETH_WS_URL`, `GETH_API_KEY`; the Bitcoin legs require `BITCOIN_RPC_URL`. Each leg skips with a notice while its endpoint is unset.

## Bitcoin Tests

Both Bitcoin suites read the collection prefix from the deployment config:
`Bitcoin__<Network>` for a Bitcoin Core endpoint.

### Mock suite (scripted Bitcoin Core fake)
```bash
go test -tags=integration -v ./integration/bitcoin/
```
A full indexer run against a scripted Bitcoin Core JSON-RPC fake with an
embedded DefraDB node. One ordered lifecycle test covers startup field
integrity, a one-block fork flip, and a stop/extend/restart resume; a health
test polls the live health, registration, metrics, and schema endpoints and
audits the authentication coverage on the recorded wire. No external
dependencies.

### Live smoke (real Bitcoin Core)
```bash
export BITCOIN_RPC_URL=http://127.0.0.1:18443   # required
export BITCOIN_ARCHIVE_RPC_URL=...              # optional archive fallback
export BITCOIN_LIVE_NETWORK=Mainnet             # optional, default Mainnet
make bitcoin-live-test
```
Skipped with exit 0 while `BITCOIN_RPC_URL` is unset, so the mock legs stay
green. The endpoint needs basic auth (user and password environment
variables) or a cookie file. The suite waits up to 30m for two newly
produced heights, which is what Bitcoin's roughly ten-minute block cadence
requires, asserts block and transaction field integrity plus one signed
BlockSignature document per stored height, and verifies the height count
only advances across an idle window. A local regtest node is the suggested
endpoint since it needs no external account; mine periodically with
`bitcoin-cli -regtest generatetoaddress 1 <address>` so the two-height wait
completes inside the 30m window.

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


