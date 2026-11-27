# Build from source

## Prerequisites

- mise (https://mise.jdx.dev)
- A blockchain node with JSON-RPC and WebSocket access.

Go and the other toolchain versions are pinned in `mise.toml`; `mise install`
sets them up, so no separate Go installation is needed.

## Steps

```shell
git clone git@github.com:shinzonetwork/shinzo-generator-client.git
cd shinzo-generator-client
cp .env.sample .env   # fill in your node credentials
mise install
mise run evm-build
```

The compiled binary goes into `./bin`. The generator ships one binary per chain adapter: `mise run evm-build` produces `bin/block_poster_evm` (the evm adapter, used for Ethereum, BSC, and any EVM-compatible chain). `mise run tron-build` produces `bin/block_poster_tron` (the tron adapter, configured via `config/config_tron.yml).
`mise run build-all` builds all adapter binaries + `bin/fetch_blocks` and `bin/build_schema`.

## Useful commands

| Command | What it does |
| --- | --- |
| `mise run evm-build` | Build the EVM generator binary (`bin/block_poster_evm`). |
| `mise run tron-build`| Build the Tron generator binary (`bin/block_poster_tron`). |
| `mise run build-all` | Build all `block_poster` binaries, `fetch_blocks`, and `build_schema`. |
| `mise run ethereum-start` | Run `bin/block_poster_evm` with the Ethereum config. |
| `mise run bsc-start` | Run `bin/block_poster_evm` with the BSC config. |
| `mise run tron-start` | Run `bin/block_poster_tron` with the Tron config. |
| `mise run test-unit` | Run unit tests with coverage |
| `mise run evm-integration` | Run the fast mock integration suite for EVM (embedded DefraDB, synthetic data, no chain endpoint). |
| `mise run tron-integration` | Run the fast mock integration suite for Tron (embedded DefraDB, synthetic data, no chain endpoint). |
| `mise run ethereum-golden` | Run the Ethereum golden tests. |
| `mise run ethereum-live-test` | Run live integration tests for Ethereum |
| `mise run bsc-live-test` | Run live integration tests for BSC |
| `mise run tron-live-test` | Run the Tron live smoke against `TRON_RPC_URL` (wallet-API base URL, for example `https://nile.trongrid.io`; optional `TRON_SOLIDITY_RPC_URL` and `TRON_API_KEY`). Boots the bundled `config/config_tron.yml` through the standard loader like Ethereum: `CHAIN_NETWORK` selects the network (`Mainnet` by default, also `Nile` and `Shasta`, driving the collection names and the endpoint's genesis check). Fails fast with a setup hint when the variable is unset. |
| `mise run test` | Run all unit + integration + live + golden test suites. |
| `mise run replay-fetch` | Capture a replay fixture for the acceptance tests (100 blocks ending at the current tip by default; `--from`/`--to` for an explicit range, `--chain` to select the chain). |
| `mise run ethereum-replay` | Run replay acceptance test for Ethereum |
| `mise run bsc-replay` | Run replay acceptance test for BSC |
| `mise run tron-replay` | Run replay acceptance test for Tron |
| `mise run lint` | Run golangci-lint. |
| `mise run format` | Format code with gofmt and goimports. |
| `mise run node-status` | Check connectivity and current block number for a blockchain node. Probes Ethereum-compatible JSON-RPC endpoints via `eth_blockNumber`; the Generator itself is chain-agnostic and accepts any compatible JSON-RPC/WebSocket endpoint. |
| `mise run clean` | Remove build artifacts. |
| `mise run stop` | Stop running generator and DefraDB processes. |
| `mise tasks --extended` | Show all available tasks and their aliases. |
