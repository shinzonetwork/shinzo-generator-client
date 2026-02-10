# Multi-stage build for Shinzo Network Ethereum Indexer
# Stage 1: Builder stage
FROM golang:1.25 AS builder

# Build arguments
ARG BUILD_DATE
ARG VCS_REF
ARG VERSION=dev
ARG BUILD_TAGS

# Install build dependencies including WASM runtimes
RUN apt-get update && apt-get install -y \
    git \
    ca-certificates \
    tzdata \
    make \
    build-essential \
    pkg-config \
    wget \
    tar \
    xz-utils \
    bash \
    coreutils \
    libgcc-s1 \
    libstdc++6 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Download dependencies (this should be cached if go.mod/go.sum don't change)
RUN go mod download && go mod verify

# Install WASM runtimes in builder stage (where commands work properly)
RUN set -ex && \
    echo "Installing WASM runtimes in builder stage" && \
    # Create directories
    mkdir -p /usr/local/include /usr/local/lib /usr/local/bin && \
    ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then \
        WASMTIME_ARCH="aarch64"; \
    else \
        WASMTIME_ARCH="x86_64"; \
    fi && \
    # Install Wasmtime
    wget -O wasmtime.tar.xz "https://github.com/bytecodealliance/wasmtime/releases/download/v15.0.1/wasmtime-v15.0.1-${WASMTIME_ARCH}-linux.tar.xz" && \
    tar -xf wasmtime.tar.xz && \
    mv "wasmtime-v15.0.1-${WASMTIME_ARCH}-linux/wasmtime" /usr/local/bin/ && \
    chmod +x /usr/local/bin/wasmtime && \
    rm -rf wasmtime* && \
    # Install Wasmer (use correct URL format)
    if [ "$WASMTIME_ARCH" = "x86_64" ]; then \
        WASMER_URL="https://github.com/wasmerio/wasmer/releases/download/v4.2.5/wasmer-linux-amd64.tar.gz"; \
    else \
        WASMER_URL="https://github.com/wasmerio/wasmer/releases/download/v4.2.5/wasmer-linux-aarch64.tar.gz"; \
    fi && \
    wget -O wasmer.tar.gz "$WASMER_URL" && \
    tar -xf wasmer.tar.gz && \
    mv bin/wasmer /usr/local/bin/ && \
    mv lib/* /usr/local/lib/ && \
    mv include/* /usr/local/include/ && \
    chmod +x /usr/local/bin/wasmer && \
    rm -rf wasmer.tar.gz bin lib include && \
    echo "WASM runtimes installed in builder stage"

# Set CGO flags for WASM support
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/usr/local/include"
ENV CGO_LDFLAGS="-L/usr/local/lib"

# Copy source code
COPY . .

# Build the application (exclude Wasmer runtime, use only Wazero)
RUN set -ex && \
    BUILD_DATE=$(date -u -Iseconds | sed 's/+00:00/Z/') && \
    VCS_REF=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") && \
    echo "Building for VERSION=${VERSION}, BUILD_DATE=${BUILD_DATE}, VCS_REF=${VCS_REF}, BUILD_TAGS=${BUILD_TAGS}" && \
    mkdir -p bin && \
    CGO_ENABLED=1 go build -v \
    -ldflags="-w -s -X main.version=${VERSION} -X main.buildDate=${BUILD_DATE} -X main.gitCommit=${VCS_REF}" \
    ${BUILD_TAGS:+-tags="${BUILD_TAGS}"} \
    -o bin/block_poster \
    cmd/block_poster/main.go && \
    echo "Build completed, checking binary:" && \
    ls -la bin/ && \
    echo "Binary created successfully"

# Stage 2: Runtime stage
FROM ubuntu:24.04

# Re-declare build arguments for this stage
ARG BUILD_DATE
ARG VCS_REF
ARG VERSION=dev

# Labels for metadata
LABEL maintainer="Shinzo Network <team@shinzo.network>" \
      org.opencontainers.image.title="Shinzo Network Indexer" \
      org.opencontainers.image.description="Ethereum blockchain indexer for Shinzo Network" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.source="https://github.com/shinzonetwork/shinzo-indexer-client"

# Install runtime dependencies
RUN apt-get update && apt-get install -y \
    ca-certificates \
    tzdata \
    curl \
    jq \
    dumb-init \
    libc6 \
    libgcc-s1 \
    libstdc++6 \
    && apt-get upgrade -y \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Copy WASM runtimes from builder stage (avoids command issues in runtime)
COPY --from=builder /usr/local/bin/wasmtime /usr/local/bin/wasmtime
COPY --from=builder /usr/local/bin/wasmer /usr/local/bin/wasmer
COPY --from=builder /usr/local/lib/ /usr/local/lib/
COPY --from=builder /usr/local/include/ /usr/local/include/

# Set library path for WASM runtimes
ENV LD_LIBRARY_PATH="/usr/local/lib"

# Create non-root user for security
RUN groupadd -g 1001 indexer && \
    useradd -u 1001 -g indexer -m -s /bin/bash indexer

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/bin/block_poster /app/block_poster

# Copy configuration files
COPY --from=builder /app/config/ /app/config/
COPY --from=builder /app/pkg/schema/ /app/pkg/schema/

# Create necessary directories with proper permissions
RUN mkdir -p /app/.defra /app/logs /tmp && \
    touch /app/logs/logfile && \
    chown -R indexer:indexer /app && \
    chmod -R 755 /app && \
    chmod +x /app/block_poster

# Switch to non-root user
USER indexer

# Health check with better error handling
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Expose ports health, p2p, graphql
EXPOSE 8080 9171 9181

# Use dumb-init for proper signal handling
ENTRYPOINT ["/usr/bin/dumb-init", "--"]

# Default command
CMD ["./block_poster", "-config", "config/config.yaml"]
