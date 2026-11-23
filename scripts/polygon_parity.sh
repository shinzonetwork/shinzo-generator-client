#!/usr/bin/env bash
# Parity check for the Polygon generator: wraps scripts/evm_parity.sh with
# the Polygon endpoint and collection prefix. State-sync blocks (0x7f) are included: their tx and logs must be stored too.
#
# Usage: GQL=http://<lan-ip>:9181/api/graphql [NETWORK=amoy] scripts/polygon_parity.sh FROM TO
#   Override the endpoint with POLYGON_RPC_URL.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ "${NETWORK:-mainnet}" = "amoy" ]; then
  export EVM_RPC_URL=${POLYGON_RPC_URL:-https://polygon-amoy.drpc.org} PREFIX=${PREFIX:-Polygon__Amoy}
else
  export EVM_RPC_URL=${POLYGON_RPC_URL:-https://polygon.drpc.org} PREFIX=${PREFIX:-Polygon__Mainnet}
fi
exec scripts/evm_parity.sh "$@"
