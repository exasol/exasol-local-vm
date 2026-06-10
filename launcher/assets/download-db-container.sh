#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "==> Downloading database container for $IMG_ARCH..."

# Map architecture to Docker platform
case "$IMG_ARCH" in
    aarch64)
        PLATFORM="linux/arm64"
        ;;
    x86_64)
        PLATFORM="linux/amd64"
        ;;
    *)
        echo "Error: unsupported architecture '$IMG_ARCH' (use aarch64 or x86_64)" >&2
        exit 1
        ;;
esac

DOCKER_IMAGE="docker.io/exasol/nano:latest"
DEST_DIR="$ROOT_DIR/release"
DEST_TARBALL="$DEST_DIR/exasol-nano-db-${IMG_ARCH}.tar.gz"

mkdir -p "$DEST_DIR"

echo "    Docker image: $DOCKER_IMAGE"
echo "    Platform: $PLATFORM"
echo "    Destination: $DEST_TARBALL"

# Pull the Docker image for the specified platform
echo "==> Pulling Docker image..."
podman pull --platform "$PLATFORM" "$DOCKER_IMAGE"

# Save the image to a compressed tarball
echo "==> Saving image to tarball..."
podman save "$DOCKER_IMAGE" | gzip -9 > "$DEST_TARBALL"

echo "==> Database container downloaded successfully"
echo "    Tarball: $DEST_TARBALL"
echo "    Size: $(du -h "$DEST_TARBALL" | cut -f1)"
