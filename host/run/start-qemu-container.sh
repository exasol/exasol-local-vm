#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

ATTACHED=false
if [ "${1:-}" = "-a" ]; then
    ATTACHED=true
    shift
fi
if [ "$#" -ne 1 ]; then
    echo "Usage: $0 [-a] <x86_64|aarch64>" >&2
    exit 2
fi

IMG_ARCH="$1"
case "$IMG_ARCH" in
    x86_64|aarch64) ;;
    *) echo "Unsupported architecture: $IMG_ARCH" >&2; exit 2 ;;
esac

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNNER_IMAGE="${VM_RUNNER_IMAGE:-exasol-local-vm-runner:latest}"
CONTAINER_NAME="${VM_CONTAINER_NAME:-exasol-local-vm}"
OUTPUT_DIR="${VM_OUTPUT_DIR:-$ROOT_DIR/output/$IMG_ARCH}"
SHARED_DIR="${VM_SHARED_DIR:-$ROOT_DIR/shared}"
VM_CONFIG="$ROOT_DIR/host/run/vm-config.json"

command -v podman >/dev/null 2>&1 || {
    echo "Error: podman is required (run task install-deps)" >&2
    exit 1
}

for artifact in disk.img arch.txt vmlinuz-virt initramfs.img kernel-cmdline.txt; do
    if [ ! -f "$OUTPUT_DIR/$artifact" ]; then
        echo "Error: required VM artifact is missing: $OUTPUT_DIR/$artifact" >&2
        exit 1
    fi
done
if ! podman image exists "$RUNNER_IMAGE"; then
    echo "Error: runner image is missing: $RUNNER_IMAGE" >&2
    exit 1
fi

if podman container exists "$CONTAINER_NAME"; then
    if [ "$(podman inspect --format '{{.State.Running}}' "$CONTAINER_NAME")" = "true" ]; then
        if [ "$ATTACHED" = "true" ]; then
            exec podman attach --detach-keys ctrl-p,ctrl-q "$CONTAINER_NAME"
        fi
        echo "VM container is already running: $CONTAINER_NAME"
        exit 0
    fi
    podman rm "$CONTAINER_NAME" >/dev/null
fi

mkdir -p "$SHARED_DIR"
RUN_ARGS=(
    --privileged
    --rm
    --name="$CONTAINER_NAME"
    --network=host
    --mount="type=bind,src=$OUTPUT_DIR,dst=/vm-image,relabel=shared,ro"
    --mount="type=bind,src=$SHARED_DIR,dst=/shared,relabel=shared"
)
if [ -f "$VM_CONFIG" ]; then
    RUN_ARGS+=(
        -e VM_CONFIG=/vm-config.json
        --mount="type=bind,src=$VM_CONFIG,dst=/vm-config.json,relabel=shared,ro"
    )
fi

if [ "$ATTACHED" = "true" ]; then
    exec podman run -it "${RUN_ARGS[@]}" "$RUNNER_IMAGE"
fi

podman run -d -t "${RUN_ARGS[@]}" "$RUNNER_IMAGE"
echo "Attach with: podman attach --detach-keys ctrl-p,ctrl-q $CONTAINER_NAME"
echo "Stop with: task stop-vm"
