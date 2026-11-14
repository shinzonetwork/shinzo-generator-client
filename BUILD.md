# Build from source

## Prerequisites

- Go 1.27.3+
- Make
- A blockchain node with JSON-RPC and WebSocket access.

## Steps

```shell
git clone git@github.com:shinzonetwork/shinzo-generator-client.git
cd shinzo-generator-client
cp .env.sample .env   # fill in your node credentials
go mod download
make build
```

The compiled binary goes into `./bin`. The generator ships one binary per chain adapter: `make build` produces `bin/block_poster_evm` (the evm adapter, used for Ethereum, BSC, and any EVM-compatible chain) plus `bin/fetch_blocks`.

## Useful commands

| Command | What it does |
| --- | --- |
| `make build` | Build all binaries (evm generator + fetch_blocks). |
| `make evm-build` | Build only `bin/block_poster_evm`. |
| `make ethereum-start` | Run `bin/block_poster_evm` with the Ethereum config. |
| `make bsc-start` | Run `bin/block_poster_evm` with the BSC config. |
| `make test` | Run all tests with a summary. |
| `make integration-test` | Run the fast mock integration suite (embedded DefraDB, synthetic data, no chain endpoint). |
| `make ethereum-live-test` | Run live integration tests for Ethereum |
| `make bsc-live-test` | Run live integration tests for BSC |
| `make bsc-bench-fetch` | Captures replay fixture for BSC acceptance test from BSC RPC |
| `make bsc-acceptance-test` | Run replay acceptance test for BSC |
| `make coverage` | Generate an HTML coverage report. |
| `make node-status` | Check connectivity and current block number for a blockchain node. Probes Ethereum-compatible JSON-RPC endpoints via `eth_blockNumber`; the Generator itself is chain-agnostic and accepts any compatible JSON-RPC/WebSocket endpoint. |
| `make clean` | Remove build artifacts. |
| `make stop` | Stop running generator and DefraDB processes. |
| `make help` | Show all available targets. |
