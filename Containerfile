# Conduit Gateway Container Image
# Multi-stage build for minimal production image

# =============================================================================
# Build Stage
# =============================================================================
FROM docker.io/library/golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make ca-certificates tzdata

WORKDIR /build

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./

# Copy the local vecgo dependency (required by go.mod replace directive)
COPY vecgo/ ./vecgo/

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build arguments for version information
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG GIT_TAG=
ARG BUILD_DATE=unknown
ARG BUILD_TAGS=

# Build the binary with production flags
# BUILD_TAGS: optional comma-separated build tags (e.g., "with_datadog,with_k8s,with_mqtt")
# TARGETARCH: injected by Docker BuildKit for multi-platform builds
ARG TARGETARCH
RUN if [ -n "${BUILD_TAGS}" ]; then \
      TAGS_FLAG="-tags ${BUILD_TAGS}"; \
    fi && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
        -buildvcs=false \
        ${TAGS_FLAG} \
        -ldflags="-s -w \
            -X 'conduit/internal/version.Version=${VERSION}' \
            -X 'conduit/internal/version.GitCommit=${GIT_COMMIT}' \
            -X 'conduit/internal/version.GitTag=${GIT_TAG}' \
            -X 'conduit/internal/version.BuildDate=${BUILD_DATE}'" \
        -o conduit \
        ./cmd/gateway

# =============================================================================
# Runtime Stage
# =============================================================================
FROM docker.io/library/alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user for security
RUN addgroup -g 1000 conduit && \
    adduser -u 1000 -G conduit -s /bin/sh -D conduit

# Writable state lives on two volumes owned by the runtime user (conduit-utxe):
#   /data       data_dir: gateway.db (+ search/brain DBs), auth/token_secret,
#               MCP token, ssh keys, .env, conduit.pid
#   /workspace  agent workspace and tool sandbox root
# Named volumes inherit this ownership on first use; bind mounts must be
# writable by uid 1000 (see DEPLOYMENT.md, "Container").
RUN mkdir -p /data /workspace /etc/conduit && \
    chown conduit:conduit /data /workspace && \
    chmod 0700 /data

# Binary (root-owned, not writable by the runtime user)
COPY --from=builder /build/conduit /usr/local/bin/conduit

# Container default config: port 18789, DB under /data, workspace /workspace,
# secrets via ${ENV} expansion. Read-only; override by mounting a file over
# /etc/conduit/config.json.
COPY configs/container/conduit.json /etc/conduit/config.json

# data_dir for the token secret, MCP token, .env and pidfile. Relative paths
# in a custom config resolve under /data, which is writable.
ENV CONDUIT_DATA_DIR=/data
WORKDIR /data

# Switch to non-root user
USER conduit:conduit

# Default port (matches configs/container/conduit.json)
EXPOSE 18789

# Volume mount points
VOLUME ["/data", "/workspace"]

# Health check (port must match "port" in the config)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:18789/health || exit 1

# Default command
ENTRYPOINT ["/usr/local/bin/conduit"]
CMD ["server", "--config", "/etc/conduit/config.json", "--verbose"]
