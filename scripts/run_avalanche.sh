#!/usr/bin/env bash
# Run the generator against Avalanche C-Chain mainnet using config/config_avalanche.yaml.
# The endpoint is pinned explicitly so an Ethereum-oriented .env can neither
# redirect this generator nor leak its API key. Override it with AVAX_RPC_URL;
# the HTTP endpoint must serve eth_getBlockReceipts.
set -euo pipefail

cd "$(dirname "$0")/.."

export GETH_RPC_URL="${AVAX_RPC_URL:-https://avalanche-c-chain-rpc.publicnode.com}"
export GETH_WS_URL=""
export GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster_evm ] || [ "${REBUILD:-1}" = "1" ]; then
  make evm-build
fi
exec ./bin/block_poster_evm -config config/config_avalanche.yaml "$@"
