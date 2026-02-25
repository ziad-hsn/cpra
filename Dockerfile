# CPRA (Concurrent Pulse-Remediation-Alerting) - Production Docker Image
# Simplified, optimized multi-stage build for Kubernetes deployment
#
# Build: docker build -t cpra:latest .
# Run: docker run --rm -p 8080:8080 --name cpra cpra:latest
#
# Created: 2025-02-15
# Version: v2025.0.0
# Phase: Phase 2 Complete - K8s Native
#

# ============================================================================
# Stage 1: Builder - Compile CPRA binary
# ============================================================================
FROM golang:1.25-alpine AS builder

# Set build environment (Go modules enabled, CGO disabled for static binary)
ENV GO111MODULE=on
ENV CGO_ENABLED=0
ENV GOOS=linux

# Set working directory
WORKDIR /build

# Copy go module files
COPY go.mod go.sum ./

# Download dependencies and verify
RUN go mod download
RUN go mod verify

# Copy source code
COPY . .

# Build CPRA binary with optimization flags
# -trimpath: Remove filesystem paths
# -ldflags="-w -s" - Strip debug info and DWARF
# -tags=ark_tiny,nodocker,noprofile (smallest binary)
# -buildmode=pie (security hardening)

RUN GOARCH=amd64 go build -trimpath -ldflags="-w -s" -tags=ark_tiny -buildmode=pie -o ./bin/cpra

# Verify binary was created successfully
RUN test -f ./bin/cpra || (echo "Binary build failed" && exit 1)

# ============================================================================
# Stage 2: Final - Runtime Image
# ============================================================================
FROM alpine:3.20

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    wget \
    tini

# Create non-root user for security (Alpine uses adduser, not useradd)
RUN addgroup -g 65535 cpra && \
    adduser -u 65535 -D -G cpra cpra && \
    mkdir -p /var/log/cpra && \
    chown -R cpra:cpra /var/log/cpra && \
    chmod 755 /var/log/cpra

# Copy CPRA binary from builder
COPY --from=builder --chown=cpra:cpra /build/bin/cpra /bin/cpra

# Set minimal PATH
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Expose ports
# Health check API
EXPOSE 8080
# Pprof server
EXPOSE 6060

# Environment variables
ENV CPRA_ENV=production
ENV CPRA_DEBUG=false
ENV CPRA_LOG_LEVEL=info

# Health check for Kubernetes readiness
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --retries=3 --timeout=2s -O- http://localhost:8080/health || exit 1

# Run CPRA as non-root user
USER cpra

# Security: run as non-root, no unnecessary privileges
ENTRYPOINT ["/sbin/tini", "--", "/bin/cpra"]

# Metadata for observability and scanning
LABEL org.opencontainers.image.title="CPRA Monitoring" \
      org.opencontainers.image.description="Concurrent Pulse-Remediation-Alerting for 1M+ monitors" \
      org.opencontainers.image.version="v2025.0.0" \
      org.opencontainers.image.vendor="CPRA Project" \
      org.opencontainers.image.created="2025-02-15" \
      org.opencontainers.image.revision="3" \
      org.opencontainers.image.source="https://github.com/project/cpra" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.authors="ziad" \
      org.opencontainers.image.documentation="https://github.com/project/cpra/blob/main/README.md"

# Security scanning labels
LABEL security.scan.disabled=false \
      security.scan.timeout=300
