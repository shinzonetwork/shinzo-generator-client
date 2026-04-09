# Shinzo Network Blockchain Indexer

Pulls Ethereum blocks, transactions, logs, and access list entries into a local database queryable over GraphQL. Runs as a node on the Shinzo P2P network.

See the [Shinzo Network docs](https://docs.shinzo.network) for context on what the network is and why you'd want to run an indexer.

## Table of contents

- [Hardware requirements](#hardware-requirements)
- [Prerequisites](#prerequisites)
- [Quick start (Docker)](#quick-start-docker)
- [Configuration reference](#configuration-reference)
- [Choosing a start height](#choosing-a-start-height)
- [Bootstrapping from a snapshot](#bootstrapping-from-a-snapshot)
- [Node registration](#node-registration)
- [Monitoring](#monitoring)
- [Querying indexed data](#querying-indexed-data)
- [Building from source](#building-from-source)
- [Further reading](#further-reading)
- [Contributing](#contributing)
- [License](#license)

## Hardware requirements

While these requirements are pretty solid, they're subject to change.

| Component | Minimum | Recommended |
| --- | --- | --- |
| CPU | 8 vCPUs | 16 vCPUs |
| Memory (RAM) | 16 GB | 32–64 GB |
| Storage | 3 TB NVMe | 4+ TB NVMe |
| OS | Ubuntu 24.04 | Ubuntu 24.04 |

## Prerequisites

- A self-hosted Ethereum node with JSON-RPC (HTTP) and WebSocket endpoints.
- Docker and Docker Compose.


## Quick start

This method uses Docker. However, you can always [build from source](#building-from-source) if you'd prefer.

1. Clone the repository:

   ```shell
   git clone https://github.com/shinzonetwork/shinzo-indexer-client.git
   cd shinzo-indexer-client
   ```

1. Create your environment file:

   ```shell
   cp .env.sample .env
   ```

   Open `.env` and fill in the values:

   ```shell
   # Both endpoints are required
   GETH_RPC_URL=http://your-node:8545
   GETH_WS_URL=ws://your-node:8546

   # API key for your node, if it requires one
   GETH_API_KEY=your-api-key

   # Block height to start indexing from (see "Choosing a start height" below)
   INDEXER_START_HEIGHT=21000000

   # Encrypts the local database keyring. Any random string works.
   # Generate one with: openssl rand -hex 32
   # Don't change this after first run or your node will lose its identity.
   DEFRADB_KEYRING_SECRET=your-secret-here
   ```

1. Pull and start the indexer:

   ```shell
   docker pull ghcr.io/shinzonetwork/shinzo-indexer-client:latest
   docker-compose up -d
   ```

1. Check it's running:

   ```shell
   curl http://localhost:8080/health
   ```

   You should get back a JSON response with `"status": "healthy"` and a `current_block` that increments over time.

## Configuration reference

Most of these you won't need to change. The required ones are in the quick start above; the rest are here if you want to fine tune your indexer.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `GETH_RPC_URL` | Yes | — | HTTP JSON-RPC endpoint of your Ethereum node. |
| `GETH_WS_URL` | Yes | — | WebSocket endpoint of your Ethereum node. |
| `GETH_API_KEY` | No | — | API key sent as `X-Api-Key` header (GCP nodes get `X-goog-api-key` automatically). |
| `DEFRADB_KEYRING_SECRET` | Yes | — | Any random string. Keep it consistent across restarts. |
| `INDEXER_START_HEIGHT` | Yes | `0` | Block to start indexing from. `0` auto-detects the chain tip. |
| `DEFRADB_P2P_ENABLED` | No | `true` | Share data with the Shinzo P2P network. |
| `DEFRADB_P2P_LISTEN_ADDR` | No | `/ip4/0.0.0.0/tcp/9171` | P2P listen address. |
| `DEFRADB_STORE_PATH` | No | `./.defra` | Where the local database is stored. |
| `INDEXER_CONCURRENT_BLOCKS` | No | `1` | Blocks to process in parallel. |
| `INDEXER_BLOCKS_PER_MINUTE` | No | `60` | Rate limit for block processing. |
| `INDEXER_HEALTH_SERVER_PORT` | No | `8080` | Port for the health/metrics HTTP server. |
| `PRUNER_ENABLED` | No | `true` | Remove old blocks from the local DB after snapshotting. |
| `PRUNER_MAX_BLOCKS` | No | `1000` | How many blocks to keep locally before pruning. |
| `SNAPSHOT_ENABLED` | No | `true` | Write snapshot files before pruning. |
| `SNAPSHOT_DIR` | No | `./.defra/snapshots` | Where snapshot files are written. |
| `LOGGER_DEBUG` | No | `false` | Verbose debug logging. |

Every available option is in `config/config.yaml` and `config/config.go`.

## Choosing a start height

`INDEXER_START_HEIGHT` controls where indexing begins.

Setting it to `0` auto-detects the current chain tip and starts there (minus a small buffer). Your node starts contributing to the network within seconds. This is fine if you don't need historical data.

Set it to a specific block number if you want to catch up from a known point. The further back you go, the longer the catch-up takes. Starting from block 0 (genesis) is technically possible but will take an extremely long time on a full Ethereum history.

For most people, the right move is to [bootstrap from a snapshot](#bootstrapping-from-a-snapshot (see below)) and set `INDEXER_START_HEIGHT` to the first block after the snapshot ends.

## Bootstrapping from a snapshot

Running indexers on the Shinzo Network export snapshot files that cover ranges of historical blocks. You can import one to skip re-indexing that history yourself.

1. List available snapshots from a peer node:

   ```shell
   curl http://<peer-node>:8080/snapshots
   ```

   The response lists available snapshot files with their block ranges and cryptographic signatures, e.g. `snapshot_21000000_21000999.kvsnap.gz`.

1. Import a snapshot into your running indexer:

   ```shell
   # TODO: confirm the correct end-to-end workflow for importing a snapshot from a remote peer.
   # The import endpoint below exists and works, but the steps for getting a file
   # from a peer node into the local snapshot directory need to be verified and documented.
   curl -X POST "http://localhost:8080/snapshots/import?file=snapshot_21000000_21000999.kvsnap.gz"
   ```

1. Set `INDEXER_START_HEIGHT` to the block after the snapshot's end block, then restart.

Each snapshot is signed by the node that produced it. The indexer verifies the signature on import. To verify a snapshot file manually:

```shell
./bin/block_poster verify snapshot_21000000_21000999.kvsnap.gz
```

## Node registration

Each indexer gets a unique cryptographic identity from its keyring. You must register to participate in the Shinzo Network.

Once the indexer is running, open this URL in your browser:

```
http://localhost:8080/registration-app
```

It redirects to [registration.shinzo.network](https://registration.shinzo.network) with your node's signed credentials pre-filled. Complete the form there.

To fetch the raw credentials (public key and signed messages) without the redirect:

```shell
curl http://localhost:8080/registration
```

> Changing `DEFRADB_KEYRING_SECRET` gives your node a different identity. If you change it, you'll need to register again.

## Monitoring

Port `8080` by default, configurable via `INDEXER_HEALTH_SERVER_PORT`.

| Endpoint | Method | Description |
| --- | --- | --- |
| `/health` | GET | Status, current block, uptime, P2P peer info. Returns HTML for browsers, JSON with `Accept: application/json`. |
| `/metrics` | GET | Current block, last processed time, uptime (JSON). |
| `/registration` | GET | Node public key and signed registration credentials (JSON). |
| `/registration-app` | GET | Redirects to the registration site with credentials pre-filled. |
| `/snapshots` | GET | Lists available snapshot files with block ranges and signatures (JSON). |
| `/snapshots/{filename}` | GET | Downloads a snapshot file. |
| `/snapshots/import` | POST | Imports a snapshot: `?file=snapshot_X_Y.kvsnap.gz`. |

To watch indexing progress:

```shell
watch -n 10 'curl -s -H "Accept: application/json" http://localhost:8080/health | python3 -m json.tool | grep current_block'
```

## Querying indexed data

Data is available at `http://localhost:9181/api/v0/graphql`. Fetch a block with its transactions, logs, and access list entries:

```graphql
{
  Ethereum__Mainnet__Block(filter: { number: { _eq: 21000000 } }) {
    hash
    number
    timestamp
    transactions {
      hash
      from
      to
      value
      gasPrice
      accessList {
        address
        storageKeys
      }
      logs {
        logIndex
        address
        topics
        data
      }
    }
  }
}
```

Fetch recent transactions from an address:

```graphql
{
  Ethereum__Mainnet__Transaction(
    filter: { from: { _eq: "0xYourAddress" } }
    order: { blockNumber: DESC }
    limit: 10
  ) {
    hash
    blockNumber
    to
    value
  }
}
```

The schema covers `Block`, `Transaction`, `Log`, and `AccessListEntry` with bidirectional relations. See `pkg/schema/schema.graphql` for the full definition.

## Building from source

Most operators should just use Docker. If you need to build from source, you'll need Go 1.24+, Wasmtime v15, and Wasmer v4.2.5 (the embedded database requires them).

```shell
go mod download
make build
make start

# or in one step
go run cmd/block_poster/main.go
```

Makefile targets:

| Target | Description |
| --- | --- |
| `make build` | Build the indexer binary. |
| `make start` | Run the built binary. |
| `make test` | Run all unit tests. |
| `make integration-test` | Run integration tests (mock + live). |
| `make geth-status` | Check Ethereum node connectivity. |
| `make coverage` | Generate HTML test coverage report. |
| `make clean` | Remove build artifacts. |
| `make stop` | Stop running indexer processes. |

There's also a `branchable` build tag that switches to sequential block processing with DefraDB's branchable collections. It's an internal option not intended for general use.

```shell
make build TAGS=branchable
```

## Further reading

- [Shinzo Network documentation](https://docs.shinzo.network)
- [DefraDB documentation](https://github.com/sourcenetwork/defradb)
- [Geth documentation](https://geth.ethereum.org/docs/)

## Contributing

1. Create an issue in this repo explaining the change you're intending to make. PRs without an associated issue will be closed. This is to mitigate spam PRs somewhat.
1. Fork the repository.
1. Create a feature branch: `git checkout -b feature/your-feature`
1. Commit your changes: `git commit -m 'Add your feature'`
1. Push and open a pull request.
