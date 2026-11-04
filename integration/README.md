# Integration Tests

## Mock Tests
```bash
# Ethereum and Bitcoin suites in one run
make integration-test

# Or per chain
make ethereum-integration-test
make bitcoin-integration-test
```
Fast tests with mock data. No external dependencies.

## Live Tests  
```bash
# Set environment variables first
source .env

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



