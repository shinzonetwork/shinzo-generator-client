#!/usr/bin/env bash
# Run the generator against ShinzoHub using config/config_shinzohub.yaml.
# The config loader reads .env and lets non-empty env vars override the YAML,
# so every endpoint/height variable is pinned here: an Ethereum-oriented .env
# can neither redirect the hub generator nor leak its API key to the hub.
# Override any of them by exporting it before running this script.
set -euo pipefail

cd "$(dirname "$0")/.."

export GETH_RPC_URL="${SHINZOHUB_EVM_RPC_URL:-http://testnet.shinzo.network:8545}"
export GETH_WS_URL="${SHINZOHUB_EVM_WS_URL:-ws://testnet.shinzo.network:8546}"
export COSMOS_RPC_URL="${SHINZOHUB_COSMOS_RPC_URL:-http://testnet.shinzo.network:26657}"
export COSMOS_LCD_URL="${SHINZOHUB_COSMOS_LCD_URL:-http://testnet.shinzo.network:1317}"
export GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster ] || [ "${REBUILD:-1}" = "1" ]; then
  make build
fi
exec ./bin/block_poster -config config/config_shinzohub.yaml "$@"
