# syntax=docker/dockerfile:1

# ==============================================================================
# Stage 1: Builder
# ==============================================================================
ARG GO_VERSION=alpine
FROM golang:${GO_VERSION} AS builder

# Set working directory inside build container
WORKDIR /build

# Cache dependency downloads by copying go.mod and go.sum first
COPY go.mod go.sum ./
RUN go mod download

# Copy application source code
COPY . .

# Compile statically linked binary without CGO, stripping debug info and symbol tables
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -trimpath \
    -o /build/bin/shopflow \
    ./cmd/shopflow

# ==============================================================================
# Stage 2: Minimal Production Runner (<50MB)
# ==============================================================================
FROM alpine:3.20 AS runner

# Install essential certificates and timezone data
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 -S shopflow && \
    adduser -u 10001 -S shopflow -G shopflow

# Set runtime working directory
WORKDIR /app

# Copy compiled Go binary from builder with non-root ownership
COPY --from=builder --chown=10001:10001 /build/bin/shopflow /app/shopflow

# Copy pre-built frontend distribution assets for SPA fallback serving
COPY --chown=10001:10001 web/dist /app/web/dist

# Run as non-root user (UID/GID 10001)
USER 10001:10001

# Document container ingress port
EXPOSE 8080

# Configure execution entrypoint
ENTRYPOINT ["/app/shopflow"]
