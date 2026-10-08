<!--
  This README covers local setup, Docker, and deployment only.
  Do not add: architecture explanations, API reference, configuration 
  deep-dives, or troubleshooting guides. Those belong in the Shinzo 
  docs site. If you're tempted to add a section, link to the docs instead.
-->

# Shinzo Generator Client

![Build Status](https://img.shields.io/github/actions/workflow/status/shinzonetwork/shinzo-generator-client/.github/workflows/go-test.yml)
![License](https://img.shields.io/github/license/shinzonetwork/shinzo-generator-client)
![Docker](https://img.shields.io/docker/v/shinzonetwork/shinzo-generator-client)

Sidecar application that connects to blockchain nodes and stores block, transaction, and log data in DefraDB.

## Getting started

Copy the sample env file and fill in your node credentials:

```shell
cp .env.sample .env
```

Then start the Generator client with Docker Compose:

```shell
docker compose up
```

Further instructions, as well as hardware recommendations, can be found at [docs.shinzo.network](https://docs.shinzo.network/generator/install).

> [!TIP]
> See [BUILD.md](./BUILD.md) for build-from-source instructions.

## Metrics

Metrics are off by default. To export them over OTLP (e.g. to Grafana Cloud), add to `.env`:

```shell
OTEL_METRICS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp-gateway-<region>.grafana.net/otlp
OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic%20<base64 of instanceID:token>
OTEL_RESOURCE_ATTRIBUTES=service.instance.id=<unique-node-id>
```

## Deployment

See the [Shinzo documentation site](https://docs.shinzo.network/generator/overview) for production deployment instructions.

## Contributing

Open an issue before submitting a PR. See [CONTRIBUTING.md](./CONTRIBUTING.md) for guidelines.

## License

[MIT](./LICENSE)
