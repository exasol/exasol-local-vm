#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

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

# TODO Potentially add explicit cache-busting instead of --pull=newer?
# Having e.g. a motd file with a version number for the vm image would be enough
# as we can bump it before a release.
#
# These builds use idfiles so we don't have to tag these temporary containers
# and clutter the host.
BASE_BUILD_ARGS=(
    --jobs=0
    --pull=newer
    --arch="${IMG_ARCH}"
    --iidfile="${OUTPUT_DIR}/base_image_id"
)
# The converter needs to be built for the same architecture as the target image
# to have access to the correct efi stub for that architecture to boot the
# kernel without a bootloader
IMG_CONVERTER_BUILD_ARGS=(
    --jobs=0
    --pull=newer
    --arch="${IMG_ARCH}"
    --iidfile="${OUTPUT_DIR}/converter_image_id"
)

echo "==> Building VM contents image with podman..."
podman build "${BASE_BUILD_ARGS[@]}" "$ROOT_DIR/container"

echo "==> Building podman image -> VM disk image converter with podman..."
podman build "${IMG_CONVERTER_BUILD_ARGS[@]}" "$ROOT_DIR/host/build"

BASE_IMG_ID="$(cat "${OUTPUT_DIR}/base_image_id")"
CONVERTER_IMG_ID="$(cat "${OUTPUT_DIR}/converter_image_id")"

echo "==> Converting VM podman image -> VM disk image..."
echo "    ==> podman->VM converter image:      ${CONVERTER_IMG_ID}"
echo "    ==> VM guest contents podman image:  ${BASE_IMG_ID}"

"${ROOT_DIR}/host/build/convert-podman-vm.sh" \
    "${IMG_ARCH}" \
    "${CONVERTER_IMG_ID}" \
    "${BASE_IMG_ID}" \
    "${OUTPUT_DIR}"

OUTPUT_DIR_RELATIVE="${OUTPUT_DIR##"${PWD}/"}"
echo "==> Build completed"
echo "==> Architecture:        $IMG_ARCH"
echo "==> Raw (fat) disk:      ${OUTPUT_DIR_RELATIVE}/disk.img"
echo "==> Raw (thin) disk:     ${OUTPUT_DIR_RELATIVE}/disk_thin.img"
echo "==> VHDX disk:           ${OUTPUT_DIR_RELATIVE}/disk.vhdx"
echo "==> Kernel binary:       ${OUTPUT_DIR_RELATIVE}/vmlinuz-virt"
echo "==> Initramfs image:     ${OUTPUT_DIR_RELATIVE}/initramfs.img"
echo "==> Kernel commandline:  ${OUTPUT_DIR_RELATIVE}/kernel-cmdline.txt"
