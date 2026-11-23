# Multi-stage build for the Shinzo Network generator client.
#
# CGO must stay enabled: the binary pulls in lens/host-go's wasmtime runtime,
# which is cgo-only. wasmtime-go vendors its own prebuilt static libraries, so
# nothing WASM-related needs installing here — no wasmer, no wasmtime CLI, no
# headers. Verified with `go list -deps ./cmd/block_poster/evm`: wasmtime-go and
# wazero are in the graph, wasmer is not in go.mod at all.

# Stage 1: build
FROM golang:1.27.3-bookworm AS builder

# Release builds pass --build-arg VERSION=<tag>; local builds report "dev".
ARG VERSION=dev
ARG BUILD_TAGS
# release.yml passes the chain and network parsed from the tag
# (v<ver>-<chain>-<network>); they select the config baked into the image.
ARG CHAIN=ethereum
ARG NETWORK=mainnet

WORKDIR /app

# Cache module downloads separately from the source tree.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# gcc and git already ship in the golang image; wasmtime links statically.
# The image keeps the internal binary name /app/block_poster so docker-compose,
# healthchecks, and prod scripts are untouched; only the build path changes.
RUN CGO_ENABLED=1 go build \
        -ldflags="-w -s -X github.com/shinzonetwork/shinzo-generator-client/config.Version=${VERSION}" \
        ${BUILD_TAGS:+-tags="${BUILD_TAGS}"} \
        -o /out/block_poster ./cmd/block_poster/evm

# Each generator runs one chain: bake that chain's config as config.yaml so
# CMD, compose and k8s stay unchanged. ethereum/mainnet keeps config.yaml;
# other mainnets use config_<chain>.yaml, other networks
# config_<chain>_<network>.yaml. A chain without a config fails the build
# rather than shipping an Ethereum image under another chain's tag.
RUN set -e; \
    c=$(echo "$CHAIN" | tr 'A-Z' 'a-z'); n=$(echo "$NETWORK" | tr 'A-Z' 'a-z'); \
    if [ "$c" = ethereum ] && [ "$n" = mainnet ]; then f=config/config.yaml; \
    elif [ "$n" = mainnet ]; then f=config/config_${c}.yaml; \
    else f=config/config_${c}_${n}.yaml; fi; \
    test -f "$f" || { echo "no config for chain=$c network=$n (expected $f)"; exit 1; }; \
    echo "baking $f"; cp "$f" /out/config.yaml

# Stage 2: runtime
# bookworm-slim matches the builder's glibc exactly, which a cgo binary needs.
FROM debian:bookworm-slim

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION=dev

LABEL maintainer="Shinzo Network <team@shinzo.network>" \
      org.opencontainers.image.title="Shinzo Network Generator" \
      org.opencontainers.image.description="Ethereum blockchain generator for Shinzo Network" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.source="https://github.com/shinzonetwork/shinzo-generator-client"

# ca-certificates for TLS to the RPC node; curl only for the compose healthcheck.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/*

# uid/gid 1001 is what docker-compose-prod.yml runs as and what the
# ~/shinzo-data volumes are chowned to.
RUN groupadd -g 1001 shinzo && useradd -u 1001 -g shinzo -m shinzo

WORKDIR /app

COPY --from=builder /out/block_poster /app/block_poster
# GraphQL collections are go:embed-ed into the binary; only the YAML is needed.
COPY --from=builder /out/config.yaml /app/config/config.yaml

# Per-chain configs name their own local store (./.defra-<chain>). In the
# container every chain stores under /app/.defra, the path the compose files
# mount as the data volume (keys live beside it in /app/keys), so data
# survives restarts whichever chain the image was built for.
ENV DEFRADB_STORE_PATH=/app/.defra \
    SNAPSHOT_DIR=/app/.defra/snapshots

RUN mkdir -p /app/.defra /app/keys /app/logs && \
    touch /app/logs/logfile && \
    chown -R shinzo:shinzo /app

USER shinzo

# health, p2p, graphql
EXPOSE 8080 9171 9181

CMD ["./block_poster", "-config", "config/config.yaml"]
