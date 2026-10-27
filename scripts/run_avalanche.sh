#!/usr/bin/env bash
# Run the generator against Avalanche C-Chain mainnet using
# config/config_avalanche.yaml. The endpoint is pinned explicitly so an
# Ethereum-oriented .env can neither redirect the Avalanche generator nor leak
# its API key. Override it with AVAX_RPC_URL; it must serve
# eth_getBlockReceipts (api.avax.network does not).
set -euo pipefail

cd "$(dirname "$0")/.."

export GETH_RPC_URL="${AVAX_RPC_URL:-https://avalanche-c-chain-rpc.publicnode.com}"
export GETH_WS_URL="" GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster ] || [ "${REBUILD:-1}" = "1" ]; then
  make build
fi
exec ./bin/block_poster -config config/config_avalanche.yaml "$@"
