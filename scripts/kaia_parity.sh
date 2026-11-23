#!/usr/bin/env bash
# Parity check for the Kaia generator: wraps scripts/evm_parity.sh with
# the Kaia endpoint and collection prefix. Kaia-native txs are served as type 0x0 and compared as served.
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql scripts/kaia_parity.sh FROM TO
#   Override the endpoint with KAIA_RPC_URL.
set -euo pipefail
cd "$(dirname "$0")/.."

export EVM_RPC_URL=${KAIA_RPC_URL:-https://public-en.node.kaia.io} PREFIX=${PREFIX:-Kaia__Mainnet}
exec scripts/evm_parity.sh "$@"
