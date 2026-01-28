#!/bin/bash
set -euxo pipefail

apt-get update
apt-get install -y docker.io mdadm

# GCP Local SSDs have model "nvme_card" and can appear as multiple namespaces on one controller (nvme0n1, nvme0n2)
# or as separate controllers (nvme0n1, nvme1n1)
DEVICES=()
for dev in /dev/nvme*n*; do
  [[ "$dev" =~ p[0-9]+$ ]] && continue
  CTRL=$(echo "$dev" | sed 's|/dev/\(nvme[0-9]*\)n.*|\1|')
  MODEL=$(cat /sys/class/nvme/$CTRL/model 2>/dev/null | xargs)
  echo "Checking $dev - controller: $CTRL, model: '$MODEL'"
  if [[ "$MODEL" == "nvme_card" ]]; then
    DEVICES+=("$dev")
  fi
done

echo "Found ${#DEVICES[@]} Local SSD(s): ${DEVICES[*]}"

if [ "${#DEVICES[@]}" -eq 0 ]; then
  echo "ERROR: No Local SSDs detected! Check that Local SSDs are attached to this VM."
  exit 1
fi

if [ "${#DEVICES[@]}" -ge 2 ]; then
  echo "Creating RAID-0 over ${#DEVICES[@]} Local SSDs"
  RAID_DEV=/dev/md0
  if [ ! -e "$RAID_DEV" ]; then
    mdadm --create $RAID_DEV \
      --level=0 \
      --raid-devices=${#DEVICES[@]} \
      "${DEVICES[@]}"
  fi
  TARGET_DEV=$RAID_DEV
else
  echo "Only one Local SSD found, using it directly"
  TARGET_DEV=${DEVICES[0]}
fi

if ! blkid $TARGET_DEV; then
  mkfs.ext4 -F $TARGET_DEV
fi

MNT=/mnt/localssd
mkdir -p $MNT
mountpoint -q $MNT || mount -o noatime,discard $TARGET_DEV $MNT
chmod 777 $MNT
mkdir -p \
  $MNT/defradb \
  $MNT/lens \
  $MNT/logs
chown -R 1003:1006 \
  $MNT/defradb \
  $MNT/lens

docker pull ghcr.io/shinzonetwork/shinzo-indexer-client:v0.4.6
docker rm -f shinzo-indexer || true
docker run -d \
  --name shinzo-indexer \
  --restart unless-stopped \
  --network host \
  -u 1003:1006 \
  -v $MNT/defradb:/app/.defra \
  -v $MNT/lens:/app/.defra/lens \
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
