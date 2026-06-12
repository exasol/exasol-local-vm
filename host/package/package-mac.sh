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
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/output/${IMG_ARCH}}"

RAW_DISK="$OUTPUT_DIR/disk.img"  # Use fat image with ESP for UEFI boot

ARCH_FILE="$OUTPUT_DIR/arch.txt"

if [ ! -f "$RAW_DISK" ]; then
    echo "Error: $RAW_DISK not found. Run 'task build' first."
    exit 1
fi

if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: $ARCH_FILE not found."
    exit 1
fi

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
case "$ARCH" in
    x86_64) PACKAGE_NAME="mac-x86_64" ;;
    aarch64) PACKAGE_NAME="mac-arm64" ;;
    *) echo "Error: unknown architecture: $ARCH" >&2; exit 1 ;;
esac

PACKAGE_DIR="$ROOT_DIR/package/$PACKAGE_NAME"
RELEASE_FILE="$ROOT_DIR/release/$PACKAGE_NAME.tar.xz"

mkdir -p "$PACKAGE_DIR" "$ROOT_DIR/release"
cp "$RAW_DISK" "$PACKAGE_DIR/disk.img"

# Note: Using UEFI boot with fat disk image
# Kernel, initramfs, and cmdline are bundled in the ESP partition as a UKI
# Modern ARM64 kernels have EFI stub and require UEFI boot

# Create the release archive first (without launcher).
# This archive is embedded into the macOS launcher binary, so it is packed at the
# maximum xz level. -9 --extreme costs build-time CPU/memory only; launch-time
# decompression is unaffected.
tar -C "$ROOT_DIR/package" -cf - "$PACKAGE_NAME" | xz -9 --extreme -v > "$RELEASE_FILE"

echo "==> macOS package archive created: $RELEASE_FILE"

# Build the Go launcher with embedded release archive
echo "==> Building macOS launcher..."
LAUNCHER_DIR="$ROOT_DIR/launcher/mac"
pushd "$LAUNCHER_DIR" > /dev/null

# Copy the release archive to be embedded
cp "$RELEASE_FILE" vm-package.tar.xz

echo "==> macOS package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
