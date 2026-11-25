#!/usr/bin/env bash
# Parity check for the Celo generator: wraps scripts/evm_parity.sh with
# the Celo endpoint and collection prefix. Fee-currency (0x7b) gas prices are in the fee currency, so they are compared as served.
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql scripts/celo_parity.sh FROM TO
#   Override the endpoint with CELO_RPC_URL.
set -euo pipefail
cd "$(dirname "$0")/.."

export EVM_RPC_URL=${CELO_RPC_URL:-https://forno.celo.org} PREFIX=${PREFIX:-Celo__Mainnet}
exec scripts/evm_parity.sh "$@"
