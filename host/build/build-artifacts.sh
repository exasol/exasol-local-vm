#!/usr/bin/env bash
set -euo pipefail

if ! command -v podman >/dev/null 2>&1; then
    echo "Error: podman is required to build the VM artifacts" >&2
    echo "Run: task install-deps" >&2
    exit 1
fi

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/output/$IMG_ARCH}"

mkdir -p "$OUTPUT_DIR"

BASE_BUILD_ARGS=(
    --jobs=0
    --pull=newer
    --arch="${IMG_ARCH}"
    --iidfile="${OUTPUT_DIR}/base_image_id"
)
IMG_CONVERTER_BUILD_ARGS=(
    --jobs=0
    --pull=newer
    --arch="${IMG_ARCH}"
    --iidfile="${OUTPUT_DIR}/converter_image_id"
)

echo "==> Building VM artifacts with podman..."
podman build "${BASE_BUILD_ARGS[@]}" "$ROOT_DIR/container"
podman build "${IMG_CONVERTER_BUILD_ARGS[@]}" "$ROOT_DIR/host/build"

IMG_CONVERTER_RUN_ARGS=(
    --rm
    --arch="${IMG_ARCH}"
    --mount="type=image,src=$(cat "${OUTPUT_DIR}/base_image_id"),dst=/image"
    --mount="type=bind,src=${OUTPUT_DIR},dst=/output,relabel=shared"
)
podman run "${IMG_CONVERTER_RUN_ARGS[@]}" "$(cat "${OUTPUT_DIR}/converter_image_id")" "${IMG_ARCH}"

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
