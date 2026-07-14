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

echo "==> Staging windows init assets for $IMG_ARCH..."

# Read the tarball name from the windows-specific config.json
CONFIG_FILE="$ROOT_DIR/launcher/assets/windows/init/config.json"
if [ ! -f "$CONFIG_FILE" ]; then
    echo "Error: config.json not found at $CONFIG_FILE" >&2
    exit 1
fi

TARBALL_NAME=$(jq -r '.db.tarball_name' "$CONFIG_FILE")
if [ -z "$TARBALL_NAME" ] || [ "$TARBALL_NAME" = "null" ]; then
    echo "Error: db.tarball_name not found in $CONFIG_FILE" >&2
    exit 1
fi

SOURCE_TARBALL="$ROOT_DIR/release/exasol-nano-db-${IMG_ARCH}.tar.gz"
SOURCE_METADATA="$SOURCE_TARBALL.metadata"
DEST_TARBALL="$ROOT_DIR/launcher/assets/windows/init/$TARBALL_NAME"
DEST_METADATA="$DEST_TARBALL.metadata"

if [ ! -f "$SOURCE_TARBALL" ]; then
    echo "Error: Container tarball not found at $SOURCE_TARBALL" >&2
    echo "Run: task download-db-container IMG_ARCH=$IMG_ARCH" >&2
    exit 1
fi

echo "    Source: $SOURCE_TARBALL"
echo "    Destination: $DEST_TARBALL"

# Copy the container tarball and its metadata sidecar into the windows init assets directory
echo "==> Copying container tarball..."
cp "$SOURCE_TARBALL" "$DEST_TARBALL"
if [ -f "$SOURCE_METADATA" ]; then
    cp "$SOURCE_METADATA" "$DEST_METADATA"
fi

echo "==> Windows init assets staged successfully"
echo "    Tarball size: $(du -h "$DEST_TARBALL" | cut -f1)"
