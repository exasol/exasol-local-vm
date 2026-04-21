#!/usr/bin/env bash
set -euo pipefail

CONTAINER_NAME="${VM_CONTAINER_NAME:-exasol-local-vm}"

if ! command -v podman >/dev/null 2>&1; then
    echo "Error: podman is required" >&2
    echo "Run: task install-deps" >&2
    exit 1
fi

if ! podman container exists "$CONTAINER_NAME"; then
    echo "==> VM container is not running: $CONTAINER_NAME"
    exit 0
fi

if [ "$(podman inspect --format '{{.State.Running}}' "$CONTAINER_NAME")" = "true" ]; then
    echo "==> Stopping VM container: $CONTAINER_NAME"
    podman stop "$CONTAINER_NAME" >/dev/null
else
    echo "==> Removing stopped VM container: $CONTAINER_NAME"
    podman rm "$CONTAINER_NAME" >/dev/null
fi

echo "==> VM container stopped"
