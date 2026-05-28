<!--
  This README covers local setup, Docker, and deployment only.
  Do not add: architecture explanations, API reference, configuration 
  deep-dives, or troubleshooting guides. Those belong in the Shinzo 
  docs site. If you're tempted to add a section, link to the docs instead.
-->

# Shinzo Indexer Client

![Build Status](https://img.shields.io/github/actions/workflow/status/shinzonetwork/shinzo-indexer-client/.github/workflows/go-test.yml)
![License](https://img.shields.io/github/license/shinzonetwork/shinzo-indexer-client)
![Docker](https://img.shields.io/docker/v/shinzonetwork/shinzo-indexer-client)

Blockchain indexing client that connects to EVM nodes and stores block, transaction, and log data in DefraDB.

## Getting started

Copy the sample env file and fill in your Ethereum node credentials:

```shell
cp .env.sample .env
```

Then start the indexer with Docker Compose:

```shell
docker compose up
```

> [!TIP]
> See [BUILD.md](./BUILD.md) for build-from-source instructions.

## Deployment

See the [Shinzo documentation site](https://docs.shinzo.network) for production deployment instructions.

## Contributing

Open an issue before submitting a PR. See [CONTRIBUTING.md](./CONTRIBUTING.md) for guidelines.

## License

[MIT](./LICENSE)
