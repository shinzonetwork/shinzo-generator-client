sudo tee ~/docker-compose.yml <<'EOF'
services:
  shinzo-indexer:
    image: ghcr.io/shinzonetwork/shinzo-indexer-client:v0.5.5
    container_name: shinzo-indexer
    restart: unless-stopped
    network_mode: host
    mem_limit: 16g
    mem_reservation: 13g
    user: "1003:1006"
    volumes:
      - ~/data/defradb:/app/.defra
      - ~/data/lens:/app/.defra/lens
    environment:
      - GETH_RPC_URL=https://json-rpc.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_WS_URL=ws://ws.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_API_KEY=YOUR_API_KEY
      - INDEXER_START_HEIGHT=0
      - DEFRADB_KEYRING_SECRET=pingpong
      - GOMEMLIMIT=14GiB
      - SNAPSHOT_ENABLED=false
      - LOG_LEVEL=error
      - LOG_SOURCE=false
      - LOG_STACKTRACE=false
    logging:
      options:
        max-size: "50m"
        max-file: "3"
EOF
