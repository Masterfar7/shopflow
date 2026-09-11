# syntax=docker/dockerfile:1

# ==============================================================================
# Stage 1: Frontend Builder
# ==============================================================================
FROM node:22-alpine AS web-builder
WORKDIR /web
COPY web/package*.json ./
RUN npm ci --prefer-offline --no-audit || npm install --no-audit
COPY web/ ./
RUN npm run build

# ==============================================================================
# Stage 2: Go Backend Builder
# ==============================================================================
FROM golang:alpine AS builder

WORKDIR /build

# Allow Go to automatically use toolchain matching go.mod
ENV GOTOOLCHAIN=auto

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
# Stage 3: Minimal Production Runner (<50MB)
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

# Copy compiled frontend distribution assets from web-builder
COPY --from=web-builder --chown=10001:10001 /web/dist /app/web/dist

# Run as non-root user (UID/GID 10001)
USER 10001:10001

# Document container ingress port
EXPOSE 8080

# Configure execution entrypoint
ENTRYPOINT ["/app/shopflow"]
