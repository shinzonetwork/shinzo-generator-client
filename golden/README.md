# Golden Tests

End-to-end freeze of the typed EVM pipeline. The suite replays captured chain
fixtures through a local mock RPC endpoint, runs the real fetcher, converter,
and block handler against an embedded headless DefraDB node, reads every stored
document back, and compares the result with the committed golden files. Any
change to what the indexer stores shows up as a golden diff that has to be
reviewed before it lands.

The suite is behind the `integration` build tag and runs as part of
`make integration-test`. No network access and no running node are needed:
the only endpoint is an in-process mock server that answers from the fixture
files, and it serves exactly the three methods the pipeline calls
(`eth_getBlockByNumber`, `eth_getBlockReceipts`, `eth_getTransactionReceipt`).
Any other method fails loudly, so unexpected RPC traffic cannot pass
silently.

## Running

```bash
go test -tags=integration ./golden/... -count=1
```

Or together with the rest of the integration suite:

```bash
make integration-test
```

## Regenerating goldens

```bash
go test -tags=integration ./golden/... -run Golden -golden-update
```

Regenerate only when the stored output is meant to change, review the
diff, and commit the goldens together with the change that caused it.

## Layout

```
golden/
  golden_test.go      suite entry: node startup, chain cases, per-chain flow
  helpers_test.go     mock RPC server, fixture loading, read-back, goldens
  ethereum/
    testdata/
      blocks/26293563.json     eth_getBlockByNumber response
      receipts/26293563.json   eth_getBlockReceipts response
      golden/
        block.json
        transaction.json
        log.json
        accessListEntry.json
```

A block and its receipts are paired by their decimal-height file stem. Both a
full JSON-RPC response body (with the `result` envelope) and a bare result
payload work: the loader unwraps the envelope when one is present.

## Capturing a block

The fixtures are verbatim responses from a real node. Save them under the
decimal height stem:

```bash
# full block by height, transactions included
curl -s $RPC -X POST -H 'Content-Type: application/json' -d \
  '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0xHEIGHT",true]}' \
  > golden/ethereum/testdata/blocks/HEIGHT.json

# receipts for that block, addressed by its hash
curl -s $RPC -X POST -H 'Content-Type: application/json' -d \
  '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockReceipts","params":["0xBLOCKHASH"]}' \
  > golden/ethereum/testdata/receipts/HEIGHT.json
```

### Block requirements

The suite asserts the captured block exercises the full pipeline:

- at least one successful and one failed transaction, so both receipt outcomes
  are frozen
- a complete receipts array: every transaction in the block has its receipt
- every transaction carries `gasUsed`, `cumulativeGasUsed`, and
  `effectiveGasPrice` after the receipt merge, so use a post-London
  (EIP-1559) block

## What the goldens compare

Each golden file freezes every stored row of one collection, limited to
chain-data fields. DefraDB artifacts are excluded: no `_docID` and no relation
link fields, so the freeze tracks conversion output rather than docID scheme
details. Rows are sorted by their compact JSON and rendered with two-space
indent, which keeps diffs stable and reviewable.

## Adding a chain

A chain is a fixture folder under `golden/` plus one entry in the case table
in `golden_test.go` (see the comment on `goldenCases`). Drop the captured
fixtures into `golden/<chain>/testdata/`, then regenerate the goldens.
