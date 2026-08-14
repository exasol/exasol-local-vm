#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

ATTACHED=false

usage() {
    cat >&2 <<EOF
Usage: start-qemu-container.sh [-a] <x86_64|aarch64>
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        -a)
            ATTACHED=true
            shift
            ;;
        --)
            shift
            break
            ;;
        -*)
            usage
            exit 1
            ;;
        *)
            break
            ;;
    esac
done

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    usage
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LAUNCHER_IMAGE="${VM_LAUNCHER_IMAGE:-exasol-local-vm-launcher:latest}"
CONTAINER_NAME="${VM_CONTAINER_NAME:-exasol-local-vm}"
OUTPUT_DIR="${VM_OUTPUT_DIR:-$ROOT_DIR/output/$IMG_ARCH}"
SHARED_DIR="${VM_SHARED_DIR:-$ROOT_DIR/shared}"
VM_CONFIG="$ROOT_DIR/host/run/vm-config.json"

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "Error: $1 is required" >&2
        echo "Run: $2" >&2
        exit 1
    fi
}

check_vm_artifacts() {
    local artifact
    local missing=false
    for artifact in \
        "$OUTPUT_DIR/disk.img" \
        "$OUTPUT_DIR/arch.txt" \
        "$OUTPUT_DIR/vmlinuz-virt" \
        "$OUTPUT_DIR/initramfs.img" \
        "$OUTPUT_DIR/kernel-cmdline.txt"; do
        if [ ! -f "$artifact" ]; then
            echo "Error: required VM artifact is missing: $artifact" >&2
            missing=true
        fi
    done

    if [ "$missing" = "true" ]; then
        echo "Run: task build" >&2
        exit 1
    fi
}

check_launcher_image() {
    if ! podman image exists "$LAUNCHER_IMAGE"; then
        echo "Error: launcher image is missing: $LAUNCHER_IMAGE" >&2
        echo "Run: task build-qemu-container" >&2
        exit 1
    fi
}

require_command podman "task install-deps"

check_vm_artifacts
check_launcher_image

if podman container exists "$CONTAINER_NAME"; then
    if [ "$(podman inspect --format '{{.State.Running}}' "$CONTAINER_NAME")" = "true" ]; then
        if [ "$ATTACHED" = "true" ]; then
            echo "==> Attaching to running VM container: $CONTAINER_NAME"
            exec podman attach --detach-keys ctrl-p,ctrl-q "$CONTAINER_NAME"
        fi
        echo "==> VM container is already running: $CONTAINER_NAME"
        echo "==> Attach with: podman attach --detach-keys ctrl-p,ctrl-q $CONTAINER_NAME"
        exit 0
    fi

    podman rm "$CONTAINER_NAME" >/dev/null
fi

mkdir -p "$SHARED_DIR"

# This uses --privileged to get access to /dev/kvm and --network=host so we
# don't have to individually export ports from the vm that we want to access.
#
# In theory this could be tightened down but the main reason to run this in a
# container is not because we don't trust it but to avoid host dependencies and
# make cleanup trivial.
RUN_ARGS=(
    --privileged
    --rm
    --name="$CONTAINER_NAME"
    --network=host
    --mount="type=bind,src=$OUTPUT_DIR,dst=/vm-image,relabel=shared,ro"
)

if [ -f "$VM_CONFIG" ]; then
    RUN_ARGS+=(
        -e VM_CONFIG=/vm-config.json
        --mount="type=bind,src=$VM_CONFIG,dst=/vm-config.json,relabel=shared,ro"
    )
fi

if [ -d "$SHARED_DIR" ]; then
    RUN_ARGS+=(--mount="type=bind,src=$SHARED_DIR,dst=/shared,relabel=shared")
fi

if [ "$ATTACHED" = "true" ]; then
    echo "==> Starting attached VM container: $CONTAINER_NAME"
    exec podman run -it "${RUN_ARGS[@]}" "$LAUNCHER_IMAGE"
fi

echo "==> Starting detached VM container: $CONTAINER_NAME"
podman run -d -t "${RUN_ARGS[@]}" "$LAUNCHER_IMAGE"
echo "==> Attach with: podman attach --detach-keys ctrl-p,ctrl-q $CONTAINER_NAME"
echo "==> Stop with: task stop-vm"
