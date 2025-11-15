# Stage 1: Build Go binary using lightweight Alpine
FROM golang:1.24-alpine AS builder

# Install git for module fetching
RUN apk add --no-cache git

# Set workdir
WORKDIR /app

# Copy go.mod/go.sum and download dependencies
COPY go.mod ./
RUN go mod download

# Copy source code
COPY . .

# Build Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -o importer .

# Stage 2: Runtime on Ubuntu 24.04
FROM ubuntu:24.04

# Avoid interactive prompts during apt installs
ENV DEBIAN_FRONTEND=noninteractive

# Install runtime dependencies: python3-pip, ffmpeg, git, curl, rsgain
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        python3-pip \
        ffmpeg \
        git \
        curl \
        rsgain \
    && rm -rf /var/lib/apt/lists/*

# Install beets via pip
RUN pip3 install --break-system-packages --no-cache-dir beets


# Set up import/library directories (can be mounted)
# ENV IMPORT_DIR=/import
# ENV LIBRARY_DIR=/library
# RUN mkdir -p $IMPORT_DIR $LIBRARY_DIR

# Copy Go binary from builder stage
COPY --from=builder /app/importer /usr/local/bin/importer

# Entrypoint
ENTRYPOINT ["importer"]
