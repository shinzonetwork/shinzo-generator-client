#!/usr/bin/env bash
# Run the generator against Celo mainnet using
# config/config_celo.yaml. The endpoint is pinned explicitly so an
# Ethereum-oriented .env can neither redirect the Celo generator nor leak
# its API key. Override it with CELO_RPC_URL; it must serve
# eth_getBlockReceipts (forno and publicnode do).
set -euo pipefail

cd "$(dirname "$0")/.."

export GETH_RPC_URL="${CELO_RPC_URL:-https://forno.celo.org}"
export GETH_WS_URL="" GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster ] || [ "${REBUILD:-1}" = "1" ]; then
  make build
fi
exec ./bin/block_poster -config config/config_celo.yaml "$@"
