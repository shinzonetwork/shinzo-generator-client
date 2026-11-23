#!/usr/bin/env bash
# Compare what an EVM generator stored in DefraDB against the chain's RPC.
# Called by the per-chain wrappers (scripts/<chain>_parity.sh), which set
# PREFIX and EVM_RPC_URL. For each height in [FROM, TO] it checks:
#   - block hash, tx count, log count;
#   - every stored log carries its block's number;
#   - every stored tx's status matches its receipt and its gasPrice matches
#     the raw pipeline's fee rule (wire gasPrice, else maxFeePerGas for 0x2/0x3).
# Exits non-zero on any mismatch. Use finalized heights only: tip blocks may
# still reorg.
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql PREFIX=Polygon__Mainnet \
#          EVM_RPC_URL=https://... scripts/evm_parity.sh FROM TO
#   GQL is printed at startup ("Providing GraphQL endpoint at ...").
set -euo pipefail

FROM=${1:?usage: $0 FROM TO}
TO=${2:?usage: $0 FROM TO}
GQL=${GQL:?set GQL to the embedded DefraDB GraphQL endpoint}
P=${PREFIX:?set PREFIX to the collection prefix, e.g. Polygon__Mainnet}
EVM=${EVM_RPC_URL:?set EVM_RPC_URL to the chain JSON-RPC endpoint}

gql() { curl -sf -X POST -H 'Content-Type: application/json' --data "$(jq -n --arg q "$1" '{query: $q}')" "$GQL"; }
# Public RPCs return transient 500s and the occasional null result; retry
# until the result is present.
evm() {
  local out
  for _ in 1 2 3 4 5; do
    out=$(curl -s -X POST -H 'Content-Type: application/json' --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}" "$EVM" || true)
    if jq -e '.result != null' >/dev/null 2>&1 <<<"$out"; then echo "$out"; return; fi
    sleep 1
  done
  echo "RPC $1 $2 failed: $out" >&2; return 1
}

# hex quantity -> decimal string. jq numbers are doubles, which is exact for
# every gas price below 2^53 wei (~9M gwei).
JQ_HEX='def hex: ltrimstr("0x") | ascii_downcase | explode
  | reduce .[] as $c (0; . * 16 + (if $c >= 97 then $c - 87 else $c - 48 end));'

rfile=$(mktemp)
trap 'rm -f "$rfile"' EXIT

fail=0
for ((n = FROM; n <= TO; n++)); do
  h=$(printf '0x%x' "$n")
  stored=$(gql "{ b: ${P}__Block(filter: {number: {_eq: $n}}) { hash }
    t: ${P}__Transaction(filter: {blockNumber: {_eq: $n}}) { hash status gasPrice }
    l: ${P}__Log(filter: {blockNumber: {_eq: $n}}) { blockNumber } }")
  got=$(jq -c --arg n "$n" '.data | {hash: (.b[0].hash // ""), txs: (.t|length), logs: (.l|length),
    logsOnBlock: ([.l[] | select((.blockNumber|tostring) == $n)] | length),
    fees: ([.t[] | {hash: (.hash|ascii_downcase), status, gasPrice: (.gasPrice // "")}] | sort_by(.hash))}' <<<"$stored")

  block=$(evm eth_getBlockByNumber "[\"$h\", true]")
  receipts=$(evm eth_getBlockReceipts "[\"$h\"]")
  # Large blocks run to ~1MB of receipts: feed them to jq on stdin or via a
  # file, never as arguments.
  printf '%s' "$receipts" >"$rfile"
  want=$(jq -c --slurpfile r "$rfile" "$JQ_HEX"'
    ([$r[0].result[].logs[]] | length) as $logs
    | ($r[0].result | map({key: (.transactionHash|ascii_downcase),
        value: ((.root // "") != "" or .status == "0x1")}) | from_entries) as $status
    | {hash: .result.hash, txs: (.result.transactions|length), logs: $logs, logsOnBlock: $logs,
       fees: ([.result.transactions[] | {hash: (.hash|ascii_downcase), status: $status[.hash|ascii_downcase],
         gasPrice: (if .gasPrice then (.gasPrice|hex|tostring)
                    elif ((.type // "0x0"|hex) as $t | $t == 2 or $t == 3) and .maxFeePerGas then (.maxFeePerGas|hex|tostring)
                    else "" end)}] | sort_by(.hash))}' <<<"$block")

  if [ "$got" != "$want" ]; then
    echo "MISMATCH $n:"; diff <(jq . <<<"$got") <(jq . <<<"$want") | head -20 || true; fail=1
  fi
done
[ "$fail" = 0 ] && echo "OK: $P blocks $FROM-$TO match the chain"
exit "$fail"
