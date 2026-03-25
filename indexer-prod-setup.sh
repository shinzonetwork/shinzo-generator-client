sudo tee ~/docker-compose.yml <<'EOF'
networks:
  shinzo-net:
    driver: bridge

services:
  shinzo-indexer:
    image: ghcr.io/shinzonetwork/shinzo-indexer-client:v0.5.5
    container_name: shinzo-indexer
    restart: unless-stopped
    networks:
      - shinzo-net
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
  nginx:
    image: nginx:alpine
    ports:
      - "8080:8080"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on:
      - shinzo-indexer
    networks:
      - shinzo-net
    restart: unless-stopped
EOF

&&

sudo tee ~/nginx.conf <<'EOF'
events { worker_connections 1024; }

http {
  # Only allow this origin for CORS
  map $http_origin $cors_origin {
    default "";
    "https://*.shinzo.network" $http_origin;
  }

  server {
    listen 8080;
    server_name _;

    # CORS headers for ALL responses from this server
    add_header 'Access-Control-Allow-Origin' $cors_origin always;
    add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, OPTIONS' always;
    add_header 'Access-Control-Allow-Headers' 'Authorization, Content-Type, Accept, Origin' always;
    add_header 'Access-Control-Max-Age' 3600 always;
    add_header 'Vary' 'Origin' always;

    # Generic preflight handler (OPTIONS to any path)
    location / {
      if ($request_method = OPTIONS) {
        return 204;
      }

      proxy_pass http://shinzo-indexer:9181;
      proxy_set_header Indexer $indexer;
    }

    # Health endpoint
    location = /health {
      if ($request_method = OPTIONS) {
        return 204;
      }

      proxy_pass http://shinzo-indexer:8080/health;
      proxy_set_header Indexer $indexer;
    }
  }
}
EOF
