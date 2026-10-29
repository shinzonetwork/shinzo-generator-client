# Integration Tests

## Mock Tests
```bash
go test -v ./integration/
```
Fast tests with mock data. No external dependencies.

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
  twice with distinct logIndex dockeys, numeric-typed assetName
  normalization, one signed BlockSignature doc per block)
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

# Run with build tag
make integration-test
```
End-to-end tests with real Ethereum data. Requires `GETH_RPC_URL`, `GETH_WS_URL`, `GETH_API_KEY`.

## Tron Live Smoke
```bash
export TRON_RPC_URL=https://nile.trongrid.io
make tron-live-test
```
`integration/live/tron/` runs one smoke test against a real wallet-API
endpoint (Nile testnet or TronGrid mainnet): it waits for 3 real blocks,
asserts block and transaction field integrity, and checks one signed
BlockSignature doc per stored block. Optional env vars: `TRON_SOLIDITY_RPC_URL`
and `TRON_API_KEY`. The suite skips with exit 0 when `TRON_RPC_URL` is unset,
and the target carries a tight `-timeout=120s`.
