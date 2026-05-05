#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUILD_CONFIG_FILE="${BUILD_CONFIG_FILE:-$ROOT_DIR/config/build-config.json}"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/output}"

if ! command -v jq >/dev/null 2>&1; then
    echo "Error: jq is required to read ${BUILD_CONFIG_FILE}" >&2
    exit 1
fi

if ! command -v podman >/dev/null 2>&1; then
    echo "Error: podman is required to build the VM artifacts" >&2
    exit 1
fi

if [ ! -f "$BUILD_CONFIG_FILE" ]; then
    echo "Error: build config not found: ${BUILD_CONFIG_FILE}" >&2
    exit 1
fi

if [ -z "$IMG_ARCH" ]; then
    echo "Error: set IMG_ARCH to x86_64 or aarch64" >&2
    exit 1
fi

KERNEL_CMDLINE="${KERNEL_CMDLINE:-$(jq -r '.kernelCmdline' "$BUILD_CONFIG_FILE")}"

mkdir -p "$OUTPUT_DIR"

BUILD_ARGS=(
    --jobs=0
    --pull=newer
    --output
    "type=local,dest=${OUTPUT_DIR}"
    --build-arg
    "KERNEL_CMDLINE=${KERNEL_CMDLINE}"
    --arch
    "${IMG_ARCH}"
    -f
    "${ROOT_DIR}/Containerfile"
)

echo "==> Building VM artifacts with podman..."
podman build "${BUILD_ARGS[@]}" "$ROOT_DIR/container"

ARCH_FILE="$OUTPUT_DIR/arch.txt"
if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: podman build completed without ${ARCH_FILE}" >&2
    exit 1
fi

ARCH="$(tr -d '\n' < "$ARCH_FILE")"

echo "==> Build completed"
echo "==> Architecture: $ARCH"
echo "==> Raw disk: $OUTPUT_DIR/disk.img"
echo "==> VHDX disk: $OUTPUT_DIR/disk.vhdx"
