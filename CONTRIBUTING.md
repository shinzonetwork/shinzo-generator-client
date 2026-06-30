# Contributing

## Before you start

Open an issue to discuss your proposed change before submitting a PR. This avoids wasted effort if the change isn't a good fit. PRs without issues attached will be closed.

## Making changes

```plaintext
cmd/                 Application entry point (block_poster)
config/              YAML + env-var configuration loading
pkg/
  constants/         Collection name helpers
  defra/             DefraDB block handler and helpers
  errors/            Structured error types and retry logic
  indexer/           Core indexing engine and concurrent processor
  logger/            Uber Zap logger wrapper
  rpc/               Ethereum JSON-RPC and WebSocket client
  schema/            Embedded GraphQL schema for DefraDB
  server/            Health check HTTP server
  snapshot/          Snapshot export, import, signing, and verification
  testutils/         Mock server, DefraDB helper, logger helper
  types/             Go structs for blocks, transactions, and logs
  utils/             Hex conversion utilities
integration/         Integration tests (mock and live)
scripts/             Shell helpers for bootstrapping and schema management
queries/             Sample GraphQL queries
```

## Submitting a PR

- Keep PRs focused. One change per PR.
- Describe what you changed and why in the PR description.
- Use the structured error system in `pkg/errors` for all new error handling.
- Make sure `make test` passes before requesting review.
- Include additional tests to cover any new functionality that your PR adds.
- Comment your code generously. Don't assume reviewers have the same context you do.
