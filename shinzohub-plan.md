# Development Guide: Run the Generator on ShinzoHub

**Outcome.** `block_poster` runs against ShinzoHub. For every block it writes
both the **EVM view** (blocks, transactions, logs, including precompile calls)
and the **native Cosmos view** (transactions, decoded messages, events) into
its embedded DefraDB under `Shinzohub__<Network>__*`. Both views share one
Block doc and one block signature.

**Branch:** `dvncan/shinzohub-client`, based on `aleks/ethereum-client-interface`
(PR #454). Open it as a draft PR against that branch and retarget to `main`
once #454 merges.

**Status (2026-10-20): done.** The generator is running against testnet. It
started 1,000 blocks behind the tip and has a 1,000-block retention window.
Parity with the chain is verified on live blocks and on a historical
precompile-transaction range.

---

## Quick start

```sh
./scripts/run_shinzohub.sh            # builds, then runs with config/config_shinzohub.yaml
```

- The startup log prints the GraphQL endpoint:
  `Providing GraphQL endpoint at http://<lan-ip>:9181/api/graphql`.
  The health page is at `http://localhost:8080`.
- The keyring secret comes from `DEFRADB_KEYRING_SECRET` (`.env` is fine).
- The script pins every endpoint and height variable, because `LoadConfig`
  loads `.env` and non-empty env vars override the YAML. An Ethereum-oriented
  `.env` therefore can't redirect the hub generator or send its API key to the
  hub. To point at another hub, set `SHINZOHUB_EVM_RPC_URL`,
  `SHINZOHUB_EVM_WS_URL`, `SHINZOHUB_COSMOS_RPC_URL` and
  `SHINZOHUB_COSMOS_LCD_URL`.
- Stop it with Ctrl-C (SIGINT). It shuts down cleanly and resumes from the
  highest stored block.

Verify against the chain:

```sh
GQL=http://<lan-ip>:9181/api/graphql scripts/shinzohub_parity.sh <from> <to>
```

---

## Definition of done

- [x] Starts against ShinzoHub testnet and logs
      `Indexing chain: Shinzohub__Testnet`.
- [x] Starts at tip − 1,000 on an empty store and catches up (120 blocks/min
      against the hub's ~44).
- [x] Follows the tip: about 50 blocks behind at 11:10, then 45 behind right
      after the restart.
- [x] Restart resumes from the highest stored block. After SIGINT at 6,351,915
      it logged `Resuming from block 6351916` and restored the prune queue
      (1,083 entries). Parity passes across the restart (6,351,910–6,351,925).
- [x] `Block.hash` equals the CometBFT/EVM RPC block hash.
- [x] Per block, the Transaction, Log, CosmosTransaction, CosmosMessage and
      CosmosEvent doc counts match the chain (`scripts/shinzohub_parity.sh`).
      Checked on live blocks 6,350,414–6,350,443 and on historical blocks
      5,708,555–5,708,566.
- [x] Precompile logs carry the right `blockNumber`/`blockHash` and link to
      their EVM transaction (precompile `0x…0212`, block 5,708,560).
- [x] Cosmos messages are stored as decoded JSON, linked to their
      CosmosTransaction and the shared Block. Multi-message IBC transactions
      are covered.
- [x] The block signature covers the Cosmos docs (e.g. `cidCount` 21 = 1
      Block + 20 finalize events).
- [x] Snapshots are written (e.g. `snapshot_6351200_6351299`).
- [x] Pruning to `max_blocks: 1000`. The first cycle removed 19 blocks / 418
      docs (Block, BlockSignature, 380 CosmosEvent). The lowest stored block
      advanced from 6,350,414 to 6,350,783, with no docs left below it.
- [x] `go test ./...` passes. Ethereum and BSC behaviour is unchanged:
      existing `pkg/chains/evm` tests pass, and the default adapter has
      regression guards.
- [ ] `golangci-lint`: not run locally. The installed binary and the local Go
      toolchain are 1.26, but the module needs 1.27. CI (golangci-lint v2.14)
      will lint the PR.

**Decoding is the host's job.** The generator stores precompile calldata and
logs raw (`input`, `topics`, `data`), and Cosmos messages as the REST API
renders them. Host views decode precompile data with each precompile's ABI
(`shinzohub/app/precompiles/<name>/abi.json`), the same way as contract
transactions.

---

## Architecture

```
config: chain.adapter = "cosmos-evm"
                │
pkg/chains/cosmos (chain family "cosmos-evm")
 ├─ Fetcher ── evm.Fetcher (eth JSON-RPC: block, receipts)      ┐ concurrently
 │          └─ cosmos.Client (CometBFT /block_results          ┘ per height
 │                            + REST /cosmos/tx/v1beta1/txs/block/{h})
 │             → *cosmos.Bundle{EVM, Cosmos}
 ├─ Converter ─ evm.Converter → Block, Transaction, Log, AccessListEntry
 │            + CosmosTransaction, CosmosMessage, CosmosEvent
 │            (rejects joins of different blocks: height and hash must match)
 ├─ LinkStamper ─ delegates EVM collections; links Cosmos docs to the
 │               shared Block and to their CosmosTransaction
 └─ CollectionNames ─ EVM set + 3 Cosmos collections (SDL embedded)

pkg/chains/evm/client_adapter_shinzohub.go (ClientAdapter "shinzohub", from chain.name)
 ├─ NeedsRawBlock + DecodeTransactions: keep the RPC block hash
 │   (go-ethereum's recomputed header hash is wrong on cosmos-evm)
 └─ StampReceiptLogs: copy blockNumber/blockHash from the receipt into each log
     (hub precompiles emit logs with blockNumber 0x0)
```

The EVM quirk adapter stays in `pkg/chains/evm`. #454's `chainAdapters`
registry is package-private, and these fixes act inside the EVM client.

### Collections (`Shinzohub__Testnet__…`)

| Collection | Source | Key fields | Links |
|---|---|---|---|
| `Block` | EVM RPC (hash = CometBFT hash) | number, hash, … | — |
| `Transaction`, `Log`, `AccessListEntry` | EVM RPC + receipts | as on Ethereum; raw `input`/`topics`/`data` | Block, Transaction |
| `CosmosTransaction` | CometBFT + REST | hash (CometBFT sha256), transactionIndex, code, codespace, log, gasWanted, gasUsed, memo, fee (JSON), messageCount | Block |
| `CosmosMessage` | REST | messageIndex, `typeUrl` (@index), `body` (JSON incl. `@type`) | Block, CosmosTransaction |
| `CosmosEvent` | CometBFT | phase (`tx` / `finalize_block`), eventIndex, `type` (@index), `attributes` (JSON) | Block, CosmosTransaction (tx phase only) |
| `BlockSignature`, `SnapshotSignature` | generator | as on Ethereum | — |

Volume: every block carries about 20 finalize-block events (mint, rewards,
fee market, …), so it's about 21 docs per empty block.

### Config (`config/config_shinzohub.yaml`)

| Key | Value |
|---|---|
| `chain` | `name: Shinzohub`, `network: Testnet`, `adapter: cosmos-evm`, `chain_id: 91273001` |
| `geth` | `http://testnet.shinzo.network:8545`, `ws://…:8546`, `api_key: ""` |
| `cosmos` (new) | `rpc_url: http://testnet.shinzo.network:26657`, `lcd_url: http://testnet.shinzo.network:1317`, `request_timeout_seconds: 30` |
| `indexer` | `start_height: 0`, `start_buffer: 1000`, `blocks_per_minute: 120` |
| `pruner` / `snapshot` | on, `max_blocks: 1000` (~23 min of chain) |
| `defradb.store.path` | `./.defra-shinzohub` (gitignored) |

New env overrides: `COSMOS_RPC_URL`, `COSMOS_LCD_URL`, `CHAIN_ID`.
`config.validateConfig` accepts `evm` and `cosmos-evm`; `cosmos-evm` requires
both cosmos URLs.

---

## What was built

| Change | Files |
|---|---|
| `cosmos-evm` chain family | `pkg/chains/cosmos/{factory,fetcher,client,converter,link_stamper,collections,types}.go`, `collections/*.graphql` |
| ShinzoHub EVM quirks | `pkg/chains/evm/client_adapter_shinzohub.go`; optional `receiptLogStamper` hook in `client_adapter.go`, called from `convertGethReceipt` (`ethereum_client.go`) |
| Config | `config/config.go` (`CosmosConfig`, `CosmosEVMChainAdapter`, `ChainID`, env overrides, validation), `config/config_shinzohub.yaml` |
| Source chain reporting | `GetSourceChainInfo` returns `(shinzohub, 91273001)` from config |
| Binaries | `cmd/block_poster`, `cmd/build_schema` blank-import `pkg/chains/cosmos` |
| Tooling | `scripts/run_shinzohub.sh`, `scripts/shinzohub_parity.sh` |
| Tests | `pkg/chains/cosmos/cosmos_test.go` (client, converter, registry, and an end-to-end write of testnet blocks into DefraDB through `defra.BlockHandler`); `pkg/chains/evm/client_adapter_shinzohub_test.go`; config and indexer tests |
| Fixtures (testnet) | `pkg/chains/cosmos/testdata/` (blocks 5708560 MsgEthereumTx, 5888399 MsgIndexerAssertion, 6032603 IBC, 5708559 empty); `pkg/chains/evm/testdata/shinzohub/` (5708560, 5648812 precompile calls; 5406552 legacy contract call; 5708559 empty) |

---

## Testnet facts (2026-10-20)

| Item | Value |
|---|---|
| Endpoints | EVM `:8545` / WS `:8546`, CometBFT `:26657`, REST `:1317` on `testnet.shinzo.network` |
| Chain ID | `91273001` (`eth_chainId` = `0x570b729`) |
| Height / block time | ~6.35M, ~1.37s |
| EVM txs, all time | 142, latest at 5,708,560. Mostly precompiles `0x…0211` (56) and `0x…0212` (17). None recently, as expected. |
| Recent activity | No transactions since about height 6.3M. Before that: `MsgEthereumTx`, IBC (`MsgUpdateClient`, `MsgAcknowledgement`), `bank.MsgSend`, `MsgIndexerAssertion`, `MsgRegisterSourcehubICA` |

### For the hub team

The hub precompiles build logs by hand in `stateDB.AddLog(&gethtypes.Log{…})`
(`app/precompiles/*/methods.go`) and don't set `BlockNumber`. Every precompile
log is therefore served with `blockNumber: 0x0`, while logs from the EVM's own
`LOG` opcodes are correct. The generator works around this
(`StampReceiptLogs`). Fixing it in the hub would help every other consumer of
the RPC too.

---

## Absent from this base (PR #431, `aleks/bsc-integration`)

Both PRs branched from `main` at `8f7e81e`. Apart from `.gitignore`, they
touch different files.

| Missing piece | Instead, for now | After #431 merges |
|---|---|---|
| `cmd/fetch_blocks` | Fixtures captured with `curl` | Optionally regenerate fixtures with it |
| `config_bsc.yaml` pattern | `config_shinzohub.yaml` built from `config.yaml` | Align the layout |
| Unified live-test setup | `scripts/shinzohub_parity.sh` against testnet | Add `integration/live/shinzohub` + a `shinzohub-live-test` target |
| Make targets | `scripts/run_shinzohub.sh` | Add `shinzohub-*` targets |
| `GetDefraDBURL` | URL from the startup log | — |

## Follow-ups

1. **Host views** decode precompile `input`/`topics`/`data` with the ABIs, and
   read `CosmosMessage.body` by `typeUrl`.
2. **Hub fix** for precompile log `BlockNumber` (see above). Once it ships,
   refresh the fixtures; `StampReceiptLogs` then becomes a no-op.
4. **Deployment:** a docker-compose service or systemd unit wrapping
   `scripts/run_shinzohub.sh`, with its own `DEFRADB_KEYRING_SECRET`.
