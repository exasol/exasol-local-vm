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

DISK_PADDING_SIZE_MB="${DISK_PADDING_SIZE_MB:-$(jq -r '.diskPaddingSizeMB' "$BUILD_CONFIG_FILE")}"
KERNEL_CMDLINE="${KERNEL_CMDLINE:-$(jq -r '.kernelCmdline' "$BUILD_CONFIG_FILE")}"
TARGET_ARCH="${TARGET_ARCH:-}"

mkdir -p "$OUTPUT_DIR"

BUILD_ARGS=(
    --jobs=0
    --pull=newer
    # For nested `podman pull` :-/
    --cap-add=SYS_ADMIN
    --output
    "type=local,dest=${OUTPUT_DIR}"
    --build-arg
    "DISK_PADDING_SIZE_MB=${DISK_PADDING_SIZE_MB}"
    --build-arg
    "KERNEL_CMDLINE=${KERNEL_CMDLINE}"
)

if [ -n "$TARGET_ARCH" ]; then
    BUILD_ARGS+=(--arch "$TARGET_ARCH")
fi

echo "==> Building VM artifacts with podman..."
podman build "${BUILD_ARGS[@]}" "$ROOT_DIR"

ARCH_FILE="$OUTPUT_DIR/arch.txt"
if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: podman build completed without ${ARCH_FILE}" >&2
    exit 1
fi

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
printf "%s\n" "$ARCH" > "$ROOT_DIR/config/disk-arch.txt"

echo "==> Build completed"
echo "==> Architecture: $ARCH"
echo "==> Raw disk: $OUTPUT_DIR/disk.img"
echo "==> VHDX disk: $OUTPUT_DIR/disk.vhdx"
