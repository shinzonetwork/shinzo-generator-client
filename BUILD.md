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

The compiled binary goes into `./bin`.

## Useful commands

| Command | What it does |
| --- | --- |
| `make build` | Build the generator binary (standard mode). |
| `make evm-start` | Run the compiled binary for EVM chains. |
| `make solana-start` | Run the compiled binary for Solana. |
| `make test` | Run all tests with a summary. |
| `make integration-test` | Run mock and live integration tests for the EVM and Solana suites; the Solana live smoke runs when `SOLANA_RPC_URL` is set. |
| `make solana-live-test` | Run the Solana live smoke against a real JSON-RPC endpoint (e.g. `https://api.devnet.solana.com`). Optional env: `SOLANA_WS_URL` (slot hints), `SOLANA_API_KEY`. Fails fast with a setup hint when `SOLANA_RPC_URL` is unset. |
| `make coverage` | Generate an HTML coverage report. |
| `make node-status` | Check connectivity and current block number for a blockchain node. Probes Ethereum-compatible JSON-RPC endpoints via `eth_blockNumber`; the Generator itself is chain-agnostic and accepts any compatible JSON-RPC/WebSocket endpoint. |
| `make clean` | Remove build artifacts. |
| `make stop` | Stop running generator and DefraDB processes. |
| `make help` | Show all available targets. |
