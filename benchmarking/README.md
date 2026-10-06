# Benchmarking — BSC Tip-Indexing Acceptance

Answers the question **"can we index BSC blocks at the tip"** with a hard,
repeatable verdict: capture real blocks once, replay them through the full
production pipeline on any machine, and assert the average per-block
processing time stays within the chain's block interval (450 ms on BSC
mainnet since the Fermi hardfork, Jan 2026).

```
capture (one-time, needs a real endpoint)          acceptance (make bsc-acceptance-test)
┌─────────────┐  eth_getBlockByNumber + receipts   ┌──────────────────────────────────┐
│             │ ─────────────────────────────────► │ fixture JSON                     │
│ cmd/        │                                    │ benchmarking/testdata/           │
│ fetch_blocks │                                   │ (gitignored, per-machine)        │
└─────────────┘                                    └──────────────┬───────────────────┘
                                                                  │ verbatim responses
                                                                  ▼
                                                   ┌──────────────────────────────────┐
                                                   │ mock JSON-RPC server             │
                                                   └──────────────┬───────────────────┘
                                                                  │
                                                                  ▼
                                                   Fetcher → Converter → BlockHandler.Store
                                                                  │
                                                                  ▼
                                                   avg block time ≤ target?  (hard assert)
```

## 1. Capture a fixture (one-time)

```sh
export GETH_RPC_URL=https://your-bsc-archival-node:8545   # GETH_* names are historical — any JSON-RPC endpoint works, but it must be ARCHIVAL (see below)
export GETH_API_KEY=...                          # optional
export GETH_API_KEY_TYPE=x-api-key               # optional header name

make build                              # produces ./bin/fetch_blocks (loads .env itself)
./bin/fetch_blocks --chain bsc --network mainnet --from 126050700 --to 126050850

make bsc-bench-fetch                    # same, via go run (100 blocks ending at the current tip)
make bsc-bench-fetch FROM=1000 TO=1099  # explicit range
go run ./cmd/fetch_blocks --out my_fixture.json  # custom path
go run ./cmd/fetch_blocks --chain arbitrum --network mainnet  # any EVM-compatible chain
```

Semantics:

- **Archival node required**: an arbitrary historical block range needs full
  receipt/state history, which pruned/full nodes only serve near the tip.
  Pointing `GETH_RPC_URL` at a non-archival node fails deeper captures with
  null blocks or missing receipts.
- **Chain agnostic**: `--chain` (default `bsc`) and `--network` (default
  `mainnet`) only label the fixture and derive its filename; the capture is
  plain EVM-compatible JSON-RPC, so any compatible endpoint works.
- Stores the node's `eth_getBlockByNumber(full=true)` and receipt result bytes
  **verbatim**, so the replay mock serves exactly what the node served.
- Prefers `eth_getBlockReceipts`; falls back to per-transaction
  `eth_getTransactionReceipt` when the node rejects the batch call (recorded
  as `receipt_mode` per block).
- Retries transport errors and HTTP 429/5xx with `Retry-After`/exponential
  backoff (500 ms doubling, capped at 8 s); 250 ms between requests by default
  (`--delay`).
- **Interrupt-aware**: SIGINT/SIGTERM saves what was captured so far as a
  partial fixture (`capture_partial: true` in the meta).
- Saves atomically (temp file + rename).
- Fixtures land in the shared `benchmarking/testdata/` directory named
  `<chain>_blocks_<from>_<to>.json` (`bsc_blocks_4901_5000.json`), so fixtures
  for other chains coexist side by side.
- Endpoint URLs and API keys are **never written** into the fixture.

## 2. Run the acceptance suite

```sh
make bsc-acceptance-test
```

- Build tag `acceptance`: excluded from `go test ./...` and CI, like the live
  suites.
- Fixture resolution: `BSC_REPLAY_FIXTURE` env wins; otherwise the newest
  `benchmarking/testdata/bsc_blocks_*.json` is used. **Missing fixture →
  the test skips** with regeneration instructions.
- `BSC_REPLAY_MAX_BLOCKS=50` caps the sample for slow hosts.
- `BSC_TARGET_BLOCK_TIME=750ms` overrides the target block interval (e.g. for
  a Maxwell-era fixture); the default is `450ms`.

The harness drives the pipeline directly — it **deliberately bypasses
`blocks_per_minute` pacing** because it measures raw pipeline capacity against
the chain's BPS. `max_docs_per_txn` and the per-collection batch sizes stay
moderate to respect badger's ~9.7 MB per-transaction ceiling.

## 3. The report

```
BSC replay acceptance report:
  fixture:          benchmarking/testdata/bsc_blocks_4901_5000.json (BSC Mainnet, blocks 4901..5000, capture_partial=false)
  blocks processed: 100
  min / p50 / p95 / max: 38ms / 61ms / 210ms / 480ms
  average:          74ms
  target:           450ms
  headroom:         83.6%
  outlier: block 4987: 480ms (6.5x avg)
```

Two verdicts, both asserted:

1. **Throughput**: average per-block processing time ≤ the target block
   interval. Missing it fails the test — the chain outruns the indexer and
   the backlog grows forever.
2. **Correctness**: the GraphQL `_count` on the block collection equals the
   blocks processed (stores are duplicate-rejecting, so a mismatch means a
   block silently failed), and `BlockSignature` docs exist for the stored
   range.
