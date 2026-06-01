# Build from source

## Prerequisites

- Go 1.26+
- Make
- An Ethereum node with JSON-RPC and WebSocket access

## Steps

```shell
git clone git@github.com:shinzonetwork/shinzo-indexer-client.git
cd shinzo-indexer-client
cp .env.sample .env   # fill in your node credentials
go mod download
make build
```

The compiled binary goes into `./bin`.

## Useful commands

| Command | What it does |
| --- | --- |
| `make build` | Build the indexer binary (standard mode). |
| `make start` | Run the compiled binary. |
| `make test` | Run all tests with a summary. |
| `make integration-test` | Run mock and live integration tests. |
| `make coverage` | Generate an HTML coverage report. |
| `make geth-status` | Check Geth node connectivity and current block number. |
| `make clean` | Remove build artifacts. |
| `make stop` | Stop running indexer and DefraDB processes. |
| `make help` | Show all available targets. |
