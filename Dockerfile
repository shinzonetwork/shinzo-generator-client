# Multi-stage build for the Shinzo Network generator client.
#
# CGO must stay enabled: the binary pulls in lens/host-go's wasmtime runtime,
# which is cgo-only. wasmtime-go vendors its own prebuilt static libraries, so
# nothing WASM-related needs installing here — no wasmer, no wasmtime CLI, no
# headers. Verified with `go list -deps ./cmd/block_poster`: wasmtime-go and
# wazero are in the graph, wasmer is not in go.mod at all.

# Stage 1: build
FROM golang:1.26-bookworm AS builder

WORKDIR /app

# Cache module downloads separately from the source tree.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# gcc and git already ship in the golang image; wasmtime links statically.
RUN CGO_ENABLED=1 go build -ldflags="-w -s" -o /out/block_poster ./cmd/block_poster

# Stage 2: runtime
# bookworm-slim matches the builder's glibc exactly, which a cgo binary needs.
FROM debian:bookworm-slim

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
COPY config/config.yaml /app/config/config.yaml

RUN mkdir -p /app/.defra && chown -R shinzo:shinzo /app

USER shinzo

# health, p2p, graphql
EXPOSE 8080 9171 9181

CMD ["./block_poster", "-config", "config/config.yaml"]
