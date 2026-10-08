# Benchmarking — BSC Tip-Indexing Acceptance

Answers the question **"can we index BSC blocks at the tip"** with a hard,
repeatable verdict: capture real blocks once, replay them through the
production pipeline and orchestration on any machine, and assert the
effective per-block interval stays within the chain's block time (450 ms on
BSC mainnet since the Fermi hardfork, Jan 2026).

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
                                                   the indexer's ConcurrentBlockProcessor
                                                   (workers from config_bsc.yaml)
                                                                  │
                                                                  ▼
                                                   Fetcher → Converter → BlockHandler.Store
                                                                  │
                                                                  ▼
                                                   effective per-block interval ≤ target?
                                                   (hard assert)
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
- `BSC_REPLAY_PRUNER=off` / `BSC_REPLAY_SNAPSHOT=off` disable the background
  services (both are on by default, see below).
- `BSC_REPLAY_PRUNER_INTERVAL_SECONDS` (10), `BSC_REPLAY_PRUNER_MAX_BLOCKS`
  (50), `BSC_REPLAY_SNAPSHOT_INTERVAL_SECONDS` (10),
  `BSC_REPLAY_SNAPSHOT_BLOCKS_PER_FILE` (50) tune the services' forced-fast
  cadence and retention.

The loop runs through the indexer's **`ConcurrentBlockProcessor`** — the real
production orchestration (worker pool, retry classification, in-order commit,
per-block `Committed block N` info logs). The worker count comes from the
shipped yaml's `concurrent_blocks` like production reads it; there is no bench
override knob, and the env scrub keeps stray `INDEXER_*` values out, so
raising concurrency is a yaml change exercised identically in production.

The run deliberately bypasses `blocks_per_minute` pacing (`0` disables the
processor's rate limiter) because it measures raw pipeline capacity against
the chain's BPS.

The config boots from the shipped `config/config_bsc.yaml` — the same file
production runs (`block_poster -config config/config_bsc.yaml`) — so chain
identity, batch sizes, badger caches, `prune_history: true` and the per-cycle
prune cap are production values, not bench approximations. Only what the
bench cannot take from it is overridden: the mock endpoint, the temp store and
snapshot dir, P2P off, no health server, open schema auth, a throwaway keyring,
and the forced-fast cadence below. Env overrides (`CHAIN_*`, `GETH_*`,
`DEFRADB_*`, `INDEXER_*`, `PRUNER_*`, `SNAPSHOT_*`, `SCHEMA_*`, `CONVERTER_*`,
`LOGGER_*`, `SHINZOHUB_*` — what a developer `.env` carries) are scrubbed for
the load, so the shipped yaml is the only input and the measurement reproduces.

The embedded DefraDB node (real badger on disk, loopback bind) runs with the
same node options the production bootstrap applies: the node identity comes
from a real file keyring under the store dir (throwaway secret, temp
directory) and is set via `SetNodeIdentity`, and badger gets its value-log
file size straight from the config — whatever the shipped yaml sets, the
bench and production both run with it.

The pruner and snapshotter run beside the replay loop with **forced-fast
defaults** — 10 s cycles, 50-block retention, 50-block snapshot files — so
delete and snapshot IO lands in the measurements the way production
background load does. The per-cycle prune size is the one pruner knob the
harness does not override: whatever the shipped yaml sets, the bench runs
with it, so raising it there concentrates the same deletes into fewer,
heavier cycles and moves this benchmark's average and tail. Their outcome
prints below the report box, one line per service, and the block-count
assertion is pruning-aware (expected rows = processed − pruned).

### What this measures vs. live indexing

The loopback mock isolates the client-side pipeline, so the dominant
live-indexing cost is absent from the measurement: real RPC round-trips plus
the node's own work serving full blocks and receipts, and endpoint rate
limiting (a single 429 backoff can add seconds). A few smaller deltas remain
on the local side:

- **P2P off** — the production node runs the libp2p host and pubsub; the bench
  node does not.
- **Services forced-fast** — the pruner (10 s cycles, 50-block retention →
  real deletions mid-run) and snapshotter (10 s scans, 50-block files → real
  gzip writes) run beside the loop with their IO in the timings; only the
  health server stays off.
- **Fresh store** — the bench writes into a new badger; compaction and LSM
  shape change over months of production data.
- **Deployment** — production typically runs containerized with cgroup memory
  limits; the bench runs on the host.

**Write-collision concentration**: the loopback mock answers in near-zero
time and pacing is off, so concurrent workers reach `Store` within the same
milliseconds, back to back — write pressure that paced production (real
fetch latency plus the `blocks_per_minute` dispatch floor) spreads over
minutes is compressed here into seconds. Under that pressure a block's
batch writes can exhaust their conflict retries and commit partially; the
block then lands unsigned and the signature-count verdict fails naming it.
Paced production stays below that pressure today — a red signature count
at several concurrent workers is the deliberate stress finding, not a
harness bug.

So live per-block time ≈ acceptance average + (real RPC time − loopback time)
+ those residuals. This suite answers the
*"is the pipeline fast enough"* question, the live suite (`make bsc-live-test`)
answers *"does it hold with the network in the loop"*.

## 3. The report

```
====================================================================================================
Blocks Replayed: 126050700 - 126050850   (151 blocks)
Concurrent Blocks: 1
Total Time:  44.184471515s
Target Throughput:  450ms  / block
Effective Throughput:  292.612394ms / block
Headroom:    35.0%
Network Latency Budget: 157.387606ms / block
____________________________________________________________________________________________________
Sequential Block Processing Stats:
 -  Average Block Time:  292.231173ms
 -  Min: 125.270292ms || p50: 257.144167ms || p95: 517.100084ms || Max: 1.356533708s
 - Outliers:
    - 126050782 - 1.356533708s - +367.4% vs avg
====================================================================================================
Pruner: 51 blocks / 53882 docs pruned
Snapshotter: 3 snapshots (last block 126050850)
```

The report is bracketed by two `=` banners and splits inside: the throughput
section backs the verdict, the underscore divider opens the sequential block
processing stats. Every content line is space-padded to the 100-character
banner width (long lines are never truncated). The values above are
illustrative, not a real run; the `Pruner:`/`Snapshotter:` lines print below
the report, one per enabled service, and are skipped when a service is
disabled.

Two verdicts, both asserted:

1. **Throughput**: the effective per-block interval — wall clock from first
   dispatch to the last in-order commit, divided by the block count — must
   stay ≤ the target block interval. Missing it fails the test — the chain
   outruns the indexer and the backlog grows forever. This — not per-block
   latency — is the gate, because contention inflates individual latencies
   even when the concurrent pipeline comfortably keeps up.
2. **Correctness**: a range query on the block collection must count
   exactly the blocks processed minus what the pruner removed (stores are
   duplicate-rejecting, so a mismatch means a block silently failed), and
   every stored block (processed − pruned) must carry a `BlockSignature` —
   a signed block is a complete block, so a shortfall names a block that
   was committed with a partial store.

The remaining statistics — `Average Block Time`, `Min`/`p50`/`p95`/`Max`,
`Outliers` — are per-block fetch→store latencies under concurrency: reported
as evidence, not gated. At more than one worker they include the shared
badger/`/seq/doc` write contention and every retry attempt a block needed —
a block that reaches store only after processor retries carries its full
retry window.
Beyond-tip fetch dispatches that are still in flight when the run's last
block commits are cancelled and contribute no samples (they are also
excluded from the wall clock).
