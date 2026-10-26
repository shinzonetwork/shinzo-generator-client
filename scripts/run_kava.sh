#!/usr/bin/env bash
# Run the generator against Kava mainnet using config/config_kava.yaml.
# Kava providers serve EVM JSON-RPC, CometBFT RPC and REST on separate hosts,
# so each endpoint is pinned explicitly: an Ethereum-oriented .env can neither
# redirect the Kava generator nor leak its API key. Override endpoints with
# KAVA_EVM_URL, KAVA_RPC_URL and KAVA_LCD_URL.
set -euo pipefail

cd "$(dirname "$0")/.."

export GETH_RPC_URL="${KAVA_EVM_URL:-https://kava-evm-rpc.publicnode.com}"
export COSMOS_RPC_URL="${KAVA_RPC_URL:-https://kava-rpc.publicnode.com}"
export COSMOS_LCD_URL="${KAVA_LCD_URL:-https://kava-rest.publicnode.com}"
export COSMOS_HOST="" GETH_WS_URL=""
export GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster ] || [ "${REBUILD:-1}" = "1" ]; then
  make build
fi
exec ./bin/block_poster -config config/config_kava.yaml "$@"
