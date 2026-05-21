#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "==> Staging init assets for $IMG_ARCH..."

# Read the tarball name from db-config.json
DB_CONFIG_FILE="$ROOT_DIR/launcher/assets/init/db-config.json"
if [ ! -f "$DB_CONFIG_FILE" ]; then
    echo "Error: db-config.json not found at $DB_CONFIG_FILE" >&2
    exit 1
fi

TARBALL_NAME=$(jq -r '."tarball-name"' "$DB_CONFIG_FILE")
if [ -z "$TARBALL_NAME" ] || [ "$TARBALL_NAME" = "null" ]; then
    echo "Error: tarball-name not found in $DB_CONFIG_FILE" >&2
    exit 1
fi

# Find the nano-container tarball for this architecture
NANO_CONTAINER_DIR="$ROOT_DIR/output/nano-container"
shopt -s nullglob
NANO_TARBALL_CANDIDATES=("$NANO_CONTAINER_DIR"/exasol-nano-*-"${IMG_ARCH}".tar.gz)
shopt -u nullglob

if [ "${#NANO_TARBALL_CANDIDATES[@]}" -eq 0 ]; then
    echo "Error: No nano-container tarball found for $IMG_ARCH in $NANO_CONTAINER_DIR" >&2
    echo "Run: task build-nano-container IMG_ARCH=$IMG_ARCH" >&2
    exit 1
fi
if [ "${#NANO_TARBALL_CANDIDATES[@]}" -gt 1 ]; then
    echo "Error: Multiple nano-container tarballs match for $IMG_ARCH:" >&2
    printf '  %s\n' "${NANO_TARBALL_CANDIDATES[@]}" >&2
    echo "Remove the stale ones from $NANO_CONTAINER_DIR and retry." >&2
    exit 1
fi

NANO_TARBALL="${NANO_TARBALL_CANDIDATES[0]}"
DEST_TARBALL="$ROOT_DIR/launcher/assets/init/$TARBALL_NAME"

echo "    Nano-container source: $NANO_TARBALL"
echo "    Destination: $DEST_TARBALL"
cp "$NANO_TARBALL" "$DEST_TARBALL"

echo "==> Init assets staged successfully"
