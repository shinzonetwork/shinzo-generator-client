#!/usr/bin/env bash
# Run the generator against Polygon PoS mainnet using config/config_polygon.yaml,
# or Amoy with NETWORK=amoy (config/config_polygon_amoy.yaml).
# The endpoint is pinned explicitly so an Ethereum-oriented .env can neither
# redirect this generator nor leak its API key. Override it with POLYGON_RPC_URL / POLYGON_WS_URL;
# the HTTP endpoint must serve eth_getBlockReceipts.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ "${NETWORK:-mainnet}" = "amoy" ]; then
  config=config/config_polygon_amoy.yaml
  export GETH_RPC_URL="${POLYGON_RPC_URL:-https://polygon-amoy.drpc.org}"
  export GETH_WS_URL="${POLYGON_WS_URL:-wss://polygon-amoy.drpc.org}"
else
  config=config/config_polygon.yaml
  export GETH_RPC_URL="${POLYGON_RPC_URL:-https://polygon.drpc.org}"
  export GETH_WS_URL="${POLYGON_WS_URL:-}"
fi
export GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster_evm ] || [ "${REBUILD:-1}" = "1" ]; then
  make evm-build
fi
exec ./bin/block_poster_evm -config "$config" "$@"
