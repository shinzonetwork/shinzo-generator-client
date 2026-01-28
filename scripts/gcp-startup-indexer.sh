#!/bin/bash
set -e

apt-get update
apt-get install -y docker.io

mkdir -p ~/data/defradb ~/data/lens
chown -R 1003:1006 ~/data/defradb ~/data/lens

docker pull ghcr.io/shinzonetwork/shinzo-indexer-client:v0.4.6
docker rm -f shinzo-indexer || true
docker run -d \
  --name shinzo-indexer \
  --restart unless-stopped \
  --network host \
  -u 1003:1006 \
  -v ~/data/defradb:/app/.defra \
  -v ~/data/lens:/app/.defra/lens \
  -e GETH_RPC_URL="https://json-rpc.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com" \
  -e GETH_WS_URL="ws://ws.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com" \
  -e GETH_API_KEY="AIzaSyChwEoj24VGkyItUPd9vQV5mC8w9Vi0mg8" \
  -e INDEXER_START_HEIGHT=23700000 \
  -e DEFRADB_KEYRING_SECRET="pingpong" \
  -e DEFRADB_BLOCK_CACHE_MB=2048 \
  -e DEFRADB_MEMTABLE_MB=1024 \
  -e DEFRADB_INDEX_CACHE_MB=2048 \
  -e DEFRADB_NUM_COMPACTORS=8 \
  -e DEFRADB_NUM_LEVEL_ZERO_TABLES=20 \
  -e DEFRADB_NUM_LEVEL_ZERO_TABLES_STALL=40 \
  -e LOG_LEVEL=error \
  -e LOG_SOURCE=false \
  -e LOG_STACKTRACE=false \
  ghcr.io/shinzonetwork/shinzo-indexer-client:v0.4.6