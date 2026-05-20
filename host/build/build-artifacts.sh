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

### Stage the pre-built nano-container tarball into the guest's build context.
#
# container/Containerfile expects a file named `container.tar.gz` alongside
# it (loaded into the guest's podman storage at build time so the resulting
# disk ships with the image pre-loaded). The tarball is produced by
# `task build-nano-container` (which `task build` depends on).
NANO_CONTAINER_DIR="$ROOT_DIR/output/nano-container"
NANO_CONTAINER_STAGED="$ROOT_DIR/container/container.tar.gz"

shopt -s nullglob
NANO_TARBALL_CANDIDATES=("$NANO_CONTAINER_DIR"/exasol-nano-*-"${IMG_ARCH}".tar.gz)
shopt -u nullglob

if [ "${#NANO_TARBALL_CANDIDATES[@]}" -eq 0 ]; then
    echo "Error: no nano-container tarball found under $NANO_CONTAINER_DIR for $IMG_ARCH" >&2
    echo "Run: task build-nano-container IMG_ARCH=$IMG_ARCH" >&2
    exit 1
fi
if [ "${#NANO_TARBALL_CANDIDATES[@]}" -gt 1 ]; then
    echo "Error: multiple nano-container tarballs match for $IMG_ARCH:" >&2
    printf '  %s\n' "${NANO_TARBALL_CANDIDATES[@]}" >&2
    echo "Remove the stale ones from $NANO_CONTAINER_DIR and retry." >&2
    exit 1
fi
NANO_TARBALL="${NANO_TARBALL_CANDIDATES[0]}"

echo "==> Staging nano-container tarball into guest build context"
echo "    ==> $NANO_TARBALL -> $NANO_CONTAINER_STAGED"
cp -f "$NANO_TARBALL" "$NANO_CONTAINER_STAGED"
trap 'rm -f "$NANO_CONTAINER_STAGED"' EXIT

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
    --security-opt=label=disable
    --security-opt=seccomp=unconfined
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
