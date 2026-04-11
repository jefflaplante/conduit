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

# Build the binary with production flags
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -buildvcs=false \
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

# Create directories for data and config
RUN mkdir -p /data /etc/conduit /workspace && \
    chown -R conduit:conduit /data /etc/conduit /workspace

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/conduit .

# Copy default minimal config
COPY --chown=conduit:conduit configs/examples/config-minimal.json /etc/conduit/config.json

# Set ownership
RUN chown conduit:conduit /app/conduit

# Switch to non-root user
USER conduit

# Expose default port
EXPOSE 18789

# Volume mount points
VOLUME ["/data", "/etc/conduit", "/workspace"]

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:18789/health || exit 1

# Default command
ENTRYPOINT ["./conduit"]
CMD ["server", "--config", "/etc/conduit/config.json", "--verbose"]
