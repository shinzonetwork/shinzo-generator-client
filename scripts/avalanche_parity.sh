#!/usr/bin/env bash
# Parity check for the Avalanche C-Chain generator: wraps scripts/evm_parity.sh with
# the Avalanche C-Chain endpoint and collection prefix. It must serve eth_getBlockReceipts (api.avax.network does not).
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql scripts/avalanche_parity.sh FROM TO
#   Override the endpoint with AVAX_RPC_URL.
set -euo pipefail
cd "$(dirname "$0")/.."

export EVM_RPC_URL=${AVAX_RPC_URL:-https://avalanche-c-chain-rpc.publicnode.com} PREFIX=${PREFIX:-Avalanche__Mainnet}
exec scripts/evm_parity.sh "$@"
