#!/usr/bin/env bash
# Run the generator against ShinzoHub using config/config_shinzohub.yaml.
# All endpoints derive from one node host (cosmos.host): EVM :8545, CometBFT
# :26657, REST :1317. The config loader reads .env and lets non-empty env vars
# override the YAML, so the per-endpoint variables are pinned empty here: an
# Ethereum-oriented .env can neither redirect the hub generator nor leak its
# API key to the hub. Point at another hub with SHINZOHUB_HOST.
set -euo pipefail

cd "$(dirname "$0")/.."

export COSMOS_HOST="${SHINZOHUB_HOST:-testnet.shinzo.network}"
export GETH_RPC_URL="" GETH_WS_URL="" COSMOS_RPC_URL="" COSMOS_LCD_URL=""
export GETH_API_KEY="" GETH_API_KEY_TYPE="" START_HEIGHT=""

if [ ! -x bin/block_poster ] || [ "${REBUILD:-1}" = "1" ]; then
  make build
fi
exec ./bin/block_poster -config config/config_shinzohub.yaml "$@"
