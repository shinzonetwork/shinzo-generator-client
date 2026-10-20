#!/usr/bin/env bash
# Compare what the ShinzoHub generator stored in DefraDB against the chain.
# For each height in [FROM, TO]: block hash, EVM tx and log counts, Cosmos tx,
# message and event counts, and that every stored log carries its block's
# number. Exits non-zero on the first mismatch.
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql scripts/shinzohub_parity.sh FROM TO
#   GQL is printed at startup ("Providing GraphQL endpoint at ...").
set -euo pipefail

FROM=${1:?usage: $0 FROM TO}
TO=${2:?usage: $0 FROM TO}
GQL=${GQL:?set GQL to the embedded DefraDB GraphQL endpoint}
HOST=${SHINZOHUB_HOST:-testnet.shinzo.network}
EVM="http://$HOST:8545"
RPC="http://$HOST:26657"
LCD="http://$HOST:1317"
P=${PREFIX:-Shinzohub__Testnet}

gql() { curl -sf -X POST -H 'Content-Type: application/json' --data "$(jq -n --arg q "$1" '{query: $q}')" "$GQL"; }
evm() { curl -sf -X POST -H 'Content-Type: application/json' --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" "$EVM"; }

fail=0
for ((n = FROM; n <= TO; n++)); do
  h=$(printf '0x%x' "$n")
  stored=$(gql "{ b: ${P}__Block(filter: {number: {_eq: $n}}) { hash }
    t: ${P}__Transaction(filter: {blockNumber: {_eq: $n}}) { hash }
    l: ${P}__Log(filter: {blockNumber: {_eq: $n}}) { blockNumber }
    ct: ${P}__CosmosTransaction(filter: {blockNumber: {_eq: $n}}) { hash }
    cm: ${P}__CosmosMessage(filter: {blockNumber: {_eq: $n}}) { typeUrl }
    ce: ${P}__CosmosEvent(filter: {blockNumber: {_eq: $n}}) { type } }")
  got=$(jq -c '.data | {hash: (.b[0].hash // ""), txs: (.t|length), logs: (.l|length), ctxs: (.ct|length), msgs: (.cm|length), events: (.ce|length)}' <<<"$stored")

  block=$(evm eth_getBlockByNumber "[\"$h\", false]")
  receipts=$(evm eth_getBlockReceipts "[\"$h\"]")
  results=$(curl -sf "$RPC/block_results?height=$n")
  msgs=$(curl -sf "$LCD/cosmos/tx/v1beta1/txs/block/$n?pagination.limit=10000" | jq '[.txs[].body.messages[]] | length')
  want=$(jq -cn --argjson b "$block" --argjson r "$receipts" --argjson br "$results" --argjson m "$msgs" \
    '{hash: $b.result.hash, txs: ($b.result.transactions|length), logs: ([$r.result[].logs[]]|length),
      ctxs: (($br.result.txs_results // [])|length), msgs: $m,
      events: ((([($br.result.txs_results // [])[].events[]])|length) + (($br.result.finalize_block_events // [])|length))}')

  if [ "$got" != "$want" ]; then
    echo "MISMATCH $n: stored=$got chain=$want"; fail=1
  fi
done
[ "$fail" = 0 ] && echo "OK: blocks $FROM-$TO match the chain"
exit "$fail"
