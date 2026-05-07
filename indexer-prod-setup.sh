sudo tee ~/docker-compose.yml <<'EOF'
networks:
  shinzo-net:
    driver: bridge

services:
  shinzo-indexer:
    image: ghcr.io/shinzonetwork/shinzo-indexer-client:standard
    user: "0:0"
    container_name: shinzo-indexer
    restart: unless-stopped
    networks:
      - shinzo-net
    mem_limit: 16g
    mem_reservation: 13g
    ports:
      - "9171:9171"
    volumes:
      - ~/data/defradb:/app/.defra
      - ~/data/lens:/app/.defra/lens
    environment:
      - GETH_RPC_URL=https://json-rpc.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_WS_URL=ws://ws.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_API_KEY=YOUR_API_KEY
      - GETH_API_KEY_TYPE=x-goog-api-key      
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
      - "443:8080"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - ~/ssl:/etc/nginx/ssl:ro
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
  map $http_origin $cors_origin {
    default "";
    "~^https://[^/]+\.shinzo\.network$" $http_origin;
  }

  server {
    listen 8080;
    server_name _;

    add_header 'Access-Control-Allow-Origin' $cors_origin always;
    add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, OPTIONS' always;
    add_header 'Access-Control-Allow-Headers' 'Authorization, Content-Type, Accept, Origin' always;
    add_header 'Access-Control-Max-Age' 3600 always;
    add_header 'Vary' 'Origin' always;

    # Health endpoint
    location = /health {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/health;
    }
    # Optional - registration endpoint
    location = /registration {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/registration;
    }

    # Metrics endpoint
    location = /metrics {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/metrics;
    }

    # Snapshots endpoint
    location = /snapshots {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/snapshots;
    }

    location ~ ^/snapshots/(.+)$ {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/snapshots/$1;
      proxy_buffering off;
      proxy_read_timeout 300s;
      proxy_send_timeout 300s;
      client_max_body_size 0;
    }

    # Default 404 for unmatched routes
    location / {
      return 404;
    }
  }
}
EOF


