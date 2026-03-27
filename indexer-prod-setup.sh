sudo tee ~/docker-compose.yml <<'EOF'
networks:
  shinzo-net:
    driver: bridge

services:
  shinzo-indexer:
    image: ghcr.io/shinzonetwork/shinzo-indexer-client:standard
    container_name: shinzo-indexer
    restart: unless-stopped
    networks:
      - shinzo-net
    mem_limit: 16g
    mem_reservation: 13g
    user: "1003:1006"
    ports:
      - "9171:9171"
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
      - "443:443"
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
  # Only allow this origin for CORS
  map $http_origin $cors_origin {
    default "";
    "~^https://[^/]+\.shinzo\.network$" $http_origin;
  }

  # HTTP server - redirect to HTTPS
  server {
    listen 8080;
    server_name _;
    return 301 https://$host:443$request_uri;
  }

  # HTTPS server with self-signed SSL
  server {
    listen 443 ssl;
    server_name _;

    # Self-signed SSL certificate paths
    ssl_certificate /etc/nginx/ssl/nginx.crt;
    ssl_certificate_key /etc/nginx/ssl/nginx.key;

    # SSL settings
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    # CORS headers for ALL responses from this server
    add_header 'Access-Control-Allow-Origin' $cors_origin always;
    add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, OPTIONS' always;
    add_header 'Access-Control-Allow-Headers' 'Authorization, Content-Type, Accept, Origin' always;
    add_header 'Access-Control-Max-Age' 3600 always;
    add_header 'Vary' 'Origin' always;

    # Health endpoint only
    location = /health {
      if ($request_method = OPTIONS) {
        return 204;
      }

      proxy_pass http://shinzo-indexer:8080/health;
    }

    # Return 404 for all other paths
    location / {
      return 404;
    }
  }
}
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
    return 301 https://$host:443$request_uri;
  }

  # HTTPS server with self-signed SSL
  server {
    listen 443 ssl;
    server_name _;

    ssl_certificate /etc/nginx/ssl/nginx.crt;
    ssl_certificate_key /etc/nginx/ssl/nginx.key;

    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_prefer_server_ciphers on;

    add_header 'Access-Control-Allow-Origin' $cors_origin always;
    add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, OPTIONS' always;
    add_header 'Access-Control-Allow-Headers' 'Authorization, Content-Type, Accept, Origin' always;
    add_header 'Access-Control-Max-Age' 3600 always;
    add_header 'Vary' 'Origin' always;

    # Health probe
    location = /health {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/health;
    }

    # Registration information [OPTIONAL] -- You may want to keep this private.
    location = /registration {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/registration;
    }

    # Basic metrics
    location = /metrics {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/metrics;
    }

    # List available snapshots
    location = /snapshots {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/snapshots;
    }

    # Download a snapshot file - handles /snapshots/:id
    location ~ ^/snapshots/(.+)$ {
      if ($request_method = OPTIONS) { return 204; }
      proxy_pass http://shinzo-indexer:8080/snapshots/$1;
      proxy_buffering off;
      proxy_read_timeout 300s;
      proxy_send_timeout 300s;
      client_max_body_size 0;
    }

    # Block everything else
    location / {
      return 404;
    }
  }
}
EOF


