#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNNER_IMAGE="${VM_RUNNER_IMAGE:-exasol-nano-vm-runner:latest}"
CONTAINER_NAME="${VM_CONTAINER_NAME:-exasol-nano-vm}"
OUTPUT_DIR="${VM_OUTPUT_DIR:-$ROOT_DIR/output}"
SHARED_DIR="${VM_SHARED_DIR:-$ROOT_DIR/shared}"
VM_CONFIG="${VM_CONFIG:-$ROOT_DIR/config/vm-config.json}"

ATTACHED=false
if [ "${1:-}" = "--attached" ]; then
    ATTACHED=true
fi

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
        "$OUTPUT_DIR/initramfs.img.zst" \
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

check_runner_image() {
    if ! podman image exists "$RUNNER_IMAGE"; then
        echo "Error: runner image is missing: $RUNNER_IMAGE" >&2
        echo "Run: task build-qemu-container" >&2
        exit 1
    fi
}

port_args_from_config() {
    if [ ! -f "$VM_CONFIG" ]; then
        return 0
    fi

    jq -r '.ports[]? | [.protocol, .host] | @tsv' "$VM_CONFIG" \
        | while IFS=$'\t' read -r protocol host_port; do
            if [ -n "$protocol" ] && [ -n "$host_port" ]; then
                printf '%s\n' "-p"
                printf '%s\n' "${host_port}:${host_port}/${protocol}"
            fi
        done
}

require_command jq "task install-deps"
require_command podman "task install-deps"

check_vm_artifacts
check_runner_image

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

RUN_ARGS=(
    --privileged
    --rm
    --name="$CONTAINER_NAME"
    --mount="type=bind,src=$OUTPUT_DIR,dst=/vm-image,relabel=shared"
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

while IFS= read -r port_arg; do
    RUN_ARGS+=("$port_arg")
done < <(port_args_from_config)

if [ "$ATTACHED" = "true" ]; then
    echo "==> Starting attached VM container: $CONTAINER_NAME"
    exec podman run -it "${RUN_ARGS[@]}" "$RUNNER_IMAGE"
fi

echo "==> Starting detached VM container: $CONTAINER_NAME"
podman run -d -t "${RUN_ARGS[@]}" "$RUNNER_IMAGE"
echo "==> Attach with: podman attach --detach-keys ctrl-p,ctrl-q $CONTAINER_NAME"
echo "==> Stop with: task stop-vm"
