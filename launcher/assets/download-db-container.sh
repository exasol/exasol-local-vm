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

if [ -z "${NANO_BASE_TAG:-}" ] || [ "$NANO_BASE_TAG" = "latest" ]; then
    echo "Error: NANO_BASE_TAG must be a concrete exasol/nano tag, not empty or latest" >&2
    exit 1
fi

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

BASE_IMAGE="docker.io/exasol/nano:${NANO_BASE_TAG}"
DEST_DIR="$ROOT_DIR/release"
DEST_TARBALL="$DEST_DIR/exasol-nano-db-${IMG_ARCH}.tar.gz"
DEST_METADATA="$DEST_TARBALL.metadata"

mkdir -p "$DEST_DIR"

echo "    Nano base image: $BASE_IMAGE"
echo "    Product version: $NANO_BASE_TAG"
echo "    Platform: $PLATFORM"
echo "    Destination: $DEST_TARBALL"

# Pull the Docker image for the specified platform
echo "==> Pulling Docker image..."
podman pull --platform "$PLATFORM" "$BASE_IMAGE"

ENTRYPOINT_JSON=$(podman image inspect "$BASE_IMAGE" --format '{{json .Config.Entrypoint}}')
if [ "$ENTRYPOINT_JSON" != '["/controller"]' ]; then
    echo "Error: expected Nano image entrypoint [\"/controller\"], got $ENTRYPOINT_JSON" >&2
    exit 1
fi

# Save the image to a compressed tarball
echo "==> Saving image to tarball..."
podman save "$BASE_IMAGE" | gzip -9 > "$DEST_TARBALL"
{
    echo "nano_base_tag=$NANO_BASE_TAG"
    echo "base_image=$BASE_IMAGE"
    echo "product_version=$NANO_BASE_TAG"
    echo "platform=$PLATFORM"
} > "$DEST_METADATA"

echo "==> Database container built successfully"
echo "    Tarball: $DEST_TARBALL"
echo "    Metadata: $DEST_METADATA"
echo "    Size: $(du -h "$DEST_TARBALL" | cut -f1)"
