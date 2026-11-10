sudo tee ~/docker-compose.yml <<'EOF'
networks:
  shinzo-net:
    driver: bridge

services:
  shinzo-generator:
    # ethereum-mainnet-latest is a floating tag published automatically on each
    # release, so this always pulls the newest published generator version.
    # To pin a specific release instead, use e.g. :v0.6.5.4-ethereum-mainnet.
    image: ghcr.io/shinzonetwork/shinzo-generator-client:ethereum-mainnet-latest
    user: "1001:1001"
    container_name: shinzo-generator
    restart: unless-stopped
    networks:
      - shinzo-net
    mem_limit: 16g
    mem_reservation: 13g
    ports:
      - "9171:9171"
      - "8080:8080"
    volumes:
      - ./shinzo-data/defradb:/app/.defra
      - ./shinzo-data/keys:/app/keys
      - ./shinzo-data/lens:/app/.defra/lens
    environment:
      - GETH_RPC_URL=https://json-rpc.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_WS_URL=ws://ws.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_API_KEY=<YOUR_API_KEY>
      - GETH_API_KEY_TYPE=x-goog-api-key
      - GETH_DIAL_TIMEOUT_SECONDS=10
      # The chain the RPC serves. Change all three together.
      # - CHAIN_NAME=Ethereum
      # - CHAIN_NETWORK=Mainnet
      # - CHAIN_ID=1
      - INDEXER_START_HEIGHT=0
      - DEFRADB_KEYRING_SECRET=pingpong
      - GOMEMLIMIT=14GiB
      - SNAPSHOT_ENABLED=false
      - LOG_LEVEL=error
      - LOG_SOURCE=false
      - LOG_STACKTRACE=false
      - SCHEMA_AUTH_MODE=none

      # Metrics (off unless OTEL_METRICS_EXPORTER is set)
      - OTEL_METRICS_EXPORTER=${OTEL_METRICS_EXPORTER:-}
      - OTEL_EXPORTER_OTLP_ENDPOINT=${OTEL_EXPORTER_OTLP_ENDPOINT:-}
      - OTEL_EXPORTER_OTLP_PROTOCOL=${OTEL_EXPORTER_OTLP_PROTOCOL:-}
      - OTEL_EXPORTER_OTLP_HEADERS=${OTEL_EXPORTER_OTLP_HEADERS:-}
      - OTEL_SERVICE_NAME=${OTEL_SERVICE_NAME:-}
      - OTEL_RESOURCE_ATTRIBUTES=${OTEL_RESOURCE_ATTRIBUTES:-}
    logging:
      options:
        max-size: "50m"
        max-file: "3"
EOF