#!/usr/bin/env bash
# Compare what the Avalanche generator stored in DefraDB against the C-Chain.
# For each height in [FROM, TO]: block hash, tx count, log count, and that
# every stored log carries its block's number. Exits non-zero on any mismatch.
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql scripts/avalanche_parity.sh FROM TO
#   GQL is printed at startup ("Providing GraphQL endpoint at ...").
set -euo pipefail

FROM=${1:?usage: $0 FROM TO}
TO=${2:?usage: $0 FROM TO}
GQL=${GQL:?set GQL to the embedded DefraDB GraphQL endpoint}
EVM=${AVAX_RPC_URL:-https://avalanche-c-chain-rpc.publicnode.com}
P=${PREFIX:-Avalanche__Mainnet}

gql() { curl -sf -X POST -H 'Content-Type: application/json' --data "$(jq -n --arg q "$1" '{query: $q}')" "$GQL"; }
evm() { curl -sf -X POST -H 'Content-Type: application/json' --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" "$EVM"; }

fail=0
for ((n = FROM; n <= TO; n++)); do
  h=$(printf '0x%x' "$n")
  stored=$(gql "{ b: ${P}__Block(filter: {number: {_eq: $n}}) { hash }
    t: ${P}__Transaction(filter: {blockNumber: {_eq: $n}}) { hash }
    l: ${P}__Log(filter: {blockNumber: {_eq: $n}}) { blockNumber } }")
  got=$(jq -c --arg n "$n" '.data | {hash: (.b[0].hash // ""), txs: (.t|length), logs: (.l|length),
    logsOnBlock: ([.l[] | select((.blockNumber|tostring) == $n)] | length)}' <<<"$stored")

  block=$(evm eth_getBlockByNumber "[\"$h\", false]")
  receipts=$(evm eth_getBlockReceipts "[\"$h\"]")
  want=$(jq -cn --argjson b "$block" --argjson r "$receipts" \
    '([$r.result[].logs[]] | length) as $logs
     | {hash: $b.result.hash, txs: ($b.result.transactions|length), logs: $logs, logsOnBlock: $logs}')

  if [ "$got" != "$want" ]; then
    echo "MISMATCH $n: stored=$got chain=$want"; fail=1
  fi
done
[ "$fail" = 0 ] && echo "OK: blocks $FROM-$TO match the chain"
exit "$fail"
