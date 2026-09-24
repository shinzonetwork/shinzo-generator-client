# Solana benchmarks

Heavy benchmark suite for the Solana indexer, kept out of the unit-test run
via the `//go:build bench` build tag. Nothing in this folder runs under
`go test ./...`, `make test`, or coverage — every command below must carry
`-tags bench` (the make targets already do).

Two harnesses live here:

| Harness | File | Measures |
|---|---|---|
| Replay | `replay_bench_test.go` | Real mainnet blocks through the full production pipeline (fetch decode → convert → DefraDB store + sign) |
| Synthetic | `synthetic_data_test.go` | Synthetic hot-slot shapes through Convert → Store, plus the full `ConcurrentBlockProcessor` path |

The shared stack (embedded DefraDB, BlockHandler, signing identity, fake RPC
client) lives in `helpers_test.go`. Node setup is delegated to the shared
`pkg/testutils` helpers.

### DefraDB backend toggle

By default the embedded node is disk-backed (temp dir), matching the
production default. Set `BENCH_DEFRADB_IN_MEMORY=true` to opt into a purely
in-memory store — useful for isolating pipeline cost from storage cost, but
measurably slower for large writes. An invalid value fails the run
immediately rather than silently measuring the wrong backend.

```sh
# default (disk-backed, matches prod)
make solana-bench-synthetic

# in-memory (slower store path)
BENCH_DEFRADB_IN_MEMORY=true make solana-bench-synthetic
BENCH_DEFRADB_IN_MEMORY=true make solana-bench-replay
```

## Replay benchmark (real mainnet blocks, 100ms/block target)

Measures the average wall time to process one block through the unmodified
production pipeline — `Fetcher` → `Client` (JSON parse + base64 tx decode +
conversion) → `Converter` → `BlockHandler.Store` — against a local mock
JSON-RPC server that replays captured node responses, so the measurement is
client-side processing, not network latency. Skipped slots are excluded from
the average and reported separately. Report-only: the 100ms verdict is
informational; the test fails only on genuine pipeline errors.

### 1. Capture the fixture (one-time, ~30s)

```sh
make solana-bench-fetch
```

Defaults to slots `449791000..449791099` and writes
`benchmarking/solana/testdata/bench_replay_449791000_449791099.json`
(hundreds of MB, gitignored). Requires `SOLANA_RPC_URL` (and optionally
`SOLANA_API_KEY` / `SOLANA_API_KEY_TYPE`) in `.env` or the environment.

Custom range or path:

```sh
make solana-bench-fetch FROM=449791000 TO=449791099
go run ./cmd/bench_fetch --from 449791000 --to 449791099 --delay 500ms \
    --out /tmp/my_capture.json
```

### 2. Run

```sh
make solana-bench-replay
```

or directly:

```sh
go test -tags bench ./benchmarking/solana \
    -run 'TestSolanaReplayProcessingBenchmark' -v -timeout 30m
```

A missing fixture skips the test with regeneration instructions. A different
capture can be selected via `SOLANA_REPLAY_FIXTURE=/path/to/file.json`.

For cheap runs (slow hosts, or the in-memory backend) cap the sample:

```sh
# index only the first 20 processable blocks (skipped slots don't count)
SOLANA_REPLAY_MAX_BLOCKS=20 make solana-bench-replay
```

The report notes the sample size; the default (unset or 0) indexes the
whole range.

### 3. Read the report

```
=== Solana replay benchmark: slots 449791000-449791099 ===
blocks processed: 97   skipped: 3
avg: 84.20ms   min: 12.10ms   p50: 71.00ms   p95: 240.00ms   max: 312.50ms
total: 8.20s for 97 blocks
TARGET 100.00ms avg: PASS (15.8% headroom)
```

`PASS` = average within budget; `EXCEEDED` = average over the 100ms target
(never a test failure). Per-block lines above the summary expose outliers.

## Synthetic store benchmarks (docs/sec into DefraDB)

Three `testing.B` benchmarks sweep the hot-slot profiles through the
production store path:

| Benchmark | Sweeps |
|---|---|
| `BenchmarkSolanaStoreSlot` | Profiles `average`, `hot`, `hot-dup` at `MaxDocsPerTxn=1000` |
| `BenchmarkSolanaStoreHotSlotBatch` | `MaxDocsPerTxn` in {100, 500, 1000, 2000} on the `hot` profile |
| `BenchmarkSolanaIndexSlots` | `ConcurrentBlockProcessor` workers in {1, 4, 8} end-to-end |

Run all three with the make target (5 iterations each):

```sh
make solana-bench-synthetic
```

Equivalent direct invocations:

```sh
go test -tags bench ./benchmarking/solana -run '^$' \
    -bench 'BenchmarkSolana(StoreSlot|StoreHotSlotBatch|IndexSlots)' \
    -benchtime 5x -timeout 30m
```

Individually, with the documented sampling for store throughput:

```sh
go test -tags bench ./benchmarking/solana -run '^$' \
    -bench 'BenchmarkSolanaStoreSlot$' -benchtime 5x -count 3 -timeout 30m

go test -tags bench ./benchmarking/solana -run '^$' \
    -bench 'BenchmarkSolanaIndexSlots' -benchtime 3x -timeout 30m
```

Metrics reported: `docs/slot` (input volume), `docs/sec` and `slots/sec`
(throughput), plus Go's standard `ns/op` and allocations.

Note: each iteration stores a distinct slot because docIDs are content
hashes — re-storing identical content aborts the store.

## Correctness pins (run with the same tag)

The shape tests and the duplicate-content regression pin live here too.
They run as plain tests (no `-bench`), spin up an embedded DefraDB, and
take minutes:

```sh
go test -tags bench ./benchmarking/solana \
    -run 'TestSynthetic|TestStoreSlotWithDuplicateContentInstructions' -v -timeout 30m
```

## Files

| File | Purpose |
|---|---|
| `helpers_test.go` | Logger TestMain, `benchConfig`, fake block builders, `fakeSlotClient`, `newBenchStore` |
| `synthetic_data_test.go` | Synthetic profiles, shape tests, duplicate-content pin, store/index benchmarks |
| `replay_bench_test.go` | Replay mock server + `TestSolanaReplayProcessingBenchmark` |
| `testdata/` | Replay fixture (`bench_replay_*.json`, gitignored) |

The capture CLI is `cmd/bench_fetch` (repository root, not part of this
package).
