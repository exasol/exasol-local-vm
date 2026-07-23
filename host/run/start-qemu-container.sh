#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

DEFAULT_VERSION_CHECK_INTERVAL_SECONDS="86400"
DEFAULT_VERSION_CHECK_IDENTITY="NONE"
DEFAULT_VERSION_CHECK_URL="${VERSION_CHECK_DEFAULT_URL:-https://metrics-test.exasol.com/v1/version-check}"

ATTACHED=false
VERSION_CHECK_ENABLED="true"
VERSION_CHECK_INTERVAL_SECONDS="$DEFAULT_VERSION_CHECK_INTERVAL_SECONDS"
VERSION_CHECK_IDENTITY="$DEFAULT_VERSION_CHECK_IDENTITY"
VERSION_CHECK_URL="$DEFAULT_VERSION_CHECK_URL"

usage() {
    cat >&2 <<EOF
Usage: start-qemu-container.sh [-a] [version-check options] <x86_64|aarch64>

Version-check options:
  --version-check-enabled <true|false>
  --version-check-interval-seconds <seconds>
  --version-check-identity <identity>
  --version-check-url <url>
EOF
}

require_option_value() {
    if [ "$#" -lt 2 ] || [ -z "${2:-}" ]; then
        usage
        exit 1
    fi
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        -a)
            ATTACHED=true
            shift
            ;;
        --version-check-enabled=*)
            VERSION_CHECK_ENABLED="${1#*=}"
            shift
            ;;
        --version-check-enabled)
            require_option_value "$@"
            VERSION_CHECK_ENABLED="${2:-}"
            shift 2
            ;;
        --version-check-interval-seconds=*)
            VERSION_CHECK_INTERVAL_SECONDS="${1#*=}"
            shift
            ;;
        --version-check-interval-seconds)
            require_option_value "$@"
            VERSION_CHECK_INTERVAL_SECONDS="${2:-}"
            shift 2
            ;;
        --version-check-identity=*)
            VERSION_CHECK_IDENTITY="${1#*=}"
            shift
            ;;
        --version-check-identity)
            require_option_value "$@"
            VERSION_CHECK_IDENTITY="${2:-}"
            shift 2
            ;;
        --version-check-url=*)
            VERSION_CHECK_URL="${1#*=}"
            shift
            ;;
        --version-check-url)
            require_option_value "$@"
            VERSION_CHECK_URL="${2:-}"
            shift 2
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
VERSION_CHECK_CONFIG="$SHARED_DIR/version-check.json"

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

normalize_bool() {
    local value
    value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
    case "$value" in
        1|true|yes|on)
            printf 'true'
            ;;
        *)
            printf 'false'
            ;;
    esac
}

positive_integer_or_default() {
    local value="$1"
    local default_value="$2"

    if [[ "$value" =~ ^[0-9]+$ ]] && [ "$value" -gt 0 ]; then
        printf '%s' "$value"
    else
        printf '%s' "$default_value"
    fi
}

detect_version_check_operating_system() {
    local kernel_name
    kernel_name="$(uname -s 2>/dev/null || printf 'unknown')"
    case "$kernel_name" in
        Darwin*) printf 'MacOS' ;;
        Linux*) printf 'Linux' ;;
        MINGW*|MSYS*|CYGWIN*|Windows_NT*) printf 'Windows' ;;
        *) printf '%s' "$kernel_name" ;;
    esac
}

json_escape() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    value="${value//$'\n'/\\n}"
    value="${value//$'\r'/\\r}"
    value="${value//$'\t'/\\t}"
    printf '%s' "$value"
}

write_version_check_config() {
    local enabled interval_seconds identity url operating_system
    local escaped_identity escaped_url escaped_operating_system

    enabled="$(normalize_bool "$VERSION_CHECK_ENABLED")"
    interval_seconds="$(positive_integer_or_default "$VERSION_CHECK_INTERVAL_SECONDS" "$DEFAULT_VERSION_CHECK_INTERVAL_SECONDS")"
    identity="$VERSION_CHECK_IDENTITY"
    if [ -z "$identity" ]; then
        identity="$DEFAULT_VERSION_CHECK_IDENTITY"
    fi

    url="$VERSION_CHECK_URL"
    if [ -z "$url" ]; then
        url="$DEFAULT_VERSION_CHECK_URL"
    fi

    operating_system="$(detect_version_check_operating_system)"

    escaped_identity="$(json_escape "$identity")"
    escaped_url="$(json_escape "$url")"
    escaped_operating_system="$(json_escape "$operating_system")"

    mkdir -p "$SHARED_DIR"
    cat > "$VERSION_CHECK_CONFIG" <<EOF
{
  "enabled": $enabled,
  "interval_seconds": $interval_seconds,
  "identity": "$escaped_identity",
  "url": "$escaped_url",
  "operating_system": "$escaped_operating_system"
}
EOF
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

write_version_check_config

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
