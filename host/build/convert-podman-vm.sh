#!/usr/bin/env bash
set -euo pipefail

IMG_ARCH="${1}"
CONVERTER_IMG_ID="${2}"
BASE_IMG_ID="${3}"
OUTPUT_DIR="${4}"
shift 4

IMG_CONVERTER_RUN_ARGS=(
    --rm
    --arch="${IMG_ARCH}"
    --mount="type=image,src=${BASE_IMG_ID},dst=/image"
    --mount="type=bind,src=${OUTPUT_DIR},dst=/output,relabel=shared"
)

exec podman run "${IMG_CONVERTER_RUN_ARGS[@]}" "${CONVERTER_IMG_ID}" "${IMG_ARCH}"
