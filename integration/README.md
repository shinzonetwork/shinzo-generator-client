# Integration Tests

## Mock Tests
```bash
# Ethereum, Tron, and Bitcoin suites in one run
make integration-test

# Or per chain
make ethereum-integration-test
make tron-integration-test
make bitcoin-integration-test
```
Self-contained suite with synthetic mock data (build tag `integration`). No
chain endpoint, no credentials, no external dependencies.

## Live Tests
End-to-end tests with real chain data, one target per chain. The Ethereum and
BSC legs skip with a notice while `GETH_RPC_URL` is unset (the Ethereum suite
additionally requires `GETH_WS_URL` and `GETH_API_KEY`; for BSC they are
optional, see its table below). The Tron and Bitcoin targets fail fast with
setup instructions while `TRON_RPC_URL` / `BITCOIN_RPC_URL` are unset.

## Tron Mock Suite
```bash
go test -tags=integration -v ./integration/tron/
```
Full lifecycle suite for the Tron adapter: `integration/tron/` serves a
scripted two-branch wallet API (branch A heights 100..109, branch B diverges
104..115, head 115) over an httptest server, then runs two real indexers,
each with its own embedded DefraDB node. The ordered phases cover:

- startup (config StartHeight 100 -> 10 branch-A blocks stored, API-key
  header wiring)
- field assertions (transfer tx, empty block, byte-duplicated event stored
  twice with distinct logIndex dockeys, TRC-10 tokenId from the wire's
  callValueInfo, one signed BlockSignature doc per block)
- reorg (branch flip at the 3s not-found retry of height 110, by-hash
  walk-back to the common ancestor at 103, rollback of 104..109, re-index of
  branch B 104..115)
- restart (stop + reopen the same store, extend branch B to 118, resume at
  116 from the DB tip, no wallet call below the resume height)

Run it via `make integration-test` (the mock phase includes this suite).

## Live Tests  
```bash
# Set environment variables first
source .env
```

### Ethereum

```bash
make ethereum-live-test
```
End-to-end live tests against a Geth-compatible endpoint: `GETH_RPC_URL` is
required, plus `GETH_WS_URL` and `GETH_API_KEY`; the leg skips with a notice
while the endpoint is unset. The Bitcoin, BSC, and Tron live legs have their
own sections below.

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
`make bitcoin-live-test` fails fast with setup instructions while
`BITCOIN_RPC_URL` is unset, so a bare mock run never touches a chain
endpoint. The endpoint needs basic auth (user and password environment
variables) or a cookie file. The suite waits up to 30m for two newly
produced heights, which is what Bitcoin's roughly ten-minute block cadence
requires, asserts block and transaction field integrity plus one signed
BlockSignature document per stored height, and verifies the height count
only advances across an idle window. A local regtest node is the suggested
endpoint since it needs no external account; mine periodically with
`bitcoin-cli -regtest generatetoaddress 1 <address>` so the two-height wait
completes inside the 30m window.

### BSC

```bash
# Run with build tag
make bsc-live-test
```

End-to-end tests against live BSC mainnet (or `BSC_LIVE_NETWORK`), exercising the
same EVM pipeline used for Ethereum — chain identity comes purely from config
(`BSC__Mainnet__*` collections).

The suite boots from the bundled `config/config_bsc.yaml` — the same file
operators run via `block_poster_evm -config config/config_bsc.yaml` — so production
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

## Tron Live Smoke
```bash
export TRON_RPC_URL=https://nile.trongrid.io
make tron-live-test
```
`integration/live/tron/` runs one smoke test against a real wallet-API
endpoint (Nile testnet or TronGrid mainnet): it waits for 3 real blocks,
asserts block and transaction field integrity, and checks one signed
BlockSignature doc per stored block. Like the Ethereum suite, it boots the
bundled `config/config_tron.yml` through the standard config loader, so the
`CHAIN_*` and `TRON_*` environment overrides all apply: `CHAIN_NETWORK`
(`Mainnet` by default, also `Nile` and `Shasta`) drives the collection names
(`Tron__Nile`), and the fetcher refuses to connect when the endpoint's
genesis block is not that network's, so a URL/network mismatch fails at
startup instead of indexing the wrong chain. Suite-specific knobs applied
after load: temp store, dynamic-port embedded DefraDB, no pruning,
snapshots, throttling, or start buffer. The suite skips with exit 0 when
`TRON_RPC_URL` is unset, and the target carries a tight `-timeout=120s`.
