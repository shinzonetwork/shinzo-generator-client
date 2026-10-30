#!/usr/bin/env bash
# Run the generator against Kaia mainnet using
# config/config_kaia.yaml. The endpoint is pinned explicitly so an
# Ethereum-oriented .env can neither redirect the Kaia generator nor leak
# its API key. Override it with KAIA_RPC_URL; it must serve
# eth_getBlockReceipts (the public endpoint does).
set -euo pipefail

cd "$(dirname "$0")/.."

export GETH_RPC_URL="${KAIA_RPC_URL:-https://public-en.node.kaia.io}"
export GETH_WS_URL="" GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster ] || [ "${REBUILD:-1}" = "1" ]; then
  make build
fi
exec ./bin/block_poster -config config/config_kaia.yaml "$@"
