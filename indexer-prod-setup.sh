sudo tee ~/docker-compose.yml <<'EOF'
networks:
  shinzo-net:
    driver: bridge

services:
  shinzo-generator:
    image: ghcr.io/shinzonetwork/shinzo-generator-client:standard
    user: "1001:1001"
    container_name: shinzo-generator
    restart: unless-stopped
    networks:
      - shinzo-net
    mem_limit: 16g
    mem_reservation: 13g
    ports:
      - "9171:9171"
    volumes:
      - ~/shinzo-data/defradb:/app/.defra
      - ~/shinzo-data/lens:/app/.defra/lens
    environment:
      - GETH_RPC_URL=https://json-rpc.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_WS_URL=ws://ws.che8qim8flet1lfjpapfmtl42.blockchainnodeengine.com
      - GETH_API_KEY=<YOUR_API_KEY>
      - GETH_API_KEY_TYPE=x-goog-api-key      
      - INDEXER_START_HEIGHT=0
      - DEFRADB_KEYRING_SECRET=pingpong
      - GOMEMLIMIT=14GiB
      - SNAPSHOT_ENABLED=false
      - LOG_LEVEL=error
      - LOG_SOURCE=false
      - LOG_STACKTRACE=false
      - SCHEMA_AUTH_MODE=none
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
      - ~/ssl/nginx.crt:/etc/nginx/ssl/nginx.crt:ro
      - ~/ssl/nginx.key:/etc/nginx/ssl/nginx.key:ro
    depends_on:
      - shinzo-generator
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
      proxy_pass http://shinzo-generator:8080/health;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }
    # Optional - registration endpoint
    location = /registration {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/registration;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }

    location = /registration-app {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/registration-app;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Metrics endpoint
    location = /metrics {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/metrics;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Snapshots endpoint
    location = /snapshots {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/snapshots;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }

    location ~ ^/snapshots/(.+)$ {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/snapshots/$1;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
      proxy_buffering off;
      proxy_read_timeout 300s;
      proxy_send_timeout 300s;
      client_max_body_size 0;
    }

    # Schema endpoint
    location = /api/v1/schema {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/api/v1/schema;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }

    location ~ ^/api/v1/schema/(.+)$ {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-generator:8080/api/v1/schema/$1;
      proxy_set_header Host $host;
      proxy_set_header X-Forwarded-Host $host;
      proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Default 404 for unmatched routes
    location / {
      return 404;
    }
  }
}

EOF

