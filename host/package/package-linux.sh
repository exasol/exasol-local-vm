#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/output/${IMG_ARCH}}"
PACKAGE_ROOT="${PACKAGE_ROOT:-$ROOT_DIR/package}"
RELEASE_ROOT="${RELEASE_ROOT:-$ROOT_DIR/release}"
RAW_DISK="$OUTPUT_DIR/disk.img"
ARCH_FILE="$OUTPUT_DIR/arch.txt"
KERNEL_FILE="$OUTPUT_DIR/vmlinuz-virt"
INITRD_FILE="$OUTPUT_DIR/initramfs.img"
KERNEL_CMDLINE_FILE="$OUTPUT_DIR/kernel-cmdline.txt"
VM_CONFIG="$ROOT_DIR/host/run/vm-config.json"
RUN_CONTAINERFILE="$ROOT_DIR/host/run/Containerfile"
RUN_QEMU_SCRIPT="$ROOT_DIR/host/run/run-qemu.sh"

for artifact in \
    "$RAW_DISK" \
    "$ARCH_FILE" \
    "$KERNEL_FILE" \
    "$INITRD_FILE" \
    "$KERNEL_CMDLINE_FILE" \
    "$VM_CONFIG" \
    "$RUN_CONTAINERFILE" \
    "$RUN_QEMU_SCRIPT"; do
    if [ ! -f "$artifact" ]; then
        echo "Error: required package input is missing: $artifact" >&2
        exit 1
    fi
done

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
case "$ARCH" in
    x86_64) PACKAGE_NAME="linux-x86_64" ;;
    aarch64) PACKAGE_NAME="linux-arm64" ;;
    *) echo "Error: unknown architecture: $ARCH" >&2; exit 1 ;;
esac

PACKAGE_DIR="$PACKAGE_ROOT/$PACKAGE_NAME"
RELEASE_FILE="$RELEASE_ROOT/$PACKAGE_NAME.tar.xz"

rm -rf "$PACKAGE_DIR"
mkdir -p "$PACKAGE_DIR/shared" "$RELEASE_ROOT"

cp "$RAW_DISK" "$PACKAGE_DIR/disk.img"
cp "$ARCH_FILE" "$PACKAGE_DIR/arch.txt"
cp "$KERNEL_FILE" "$PACKAGE_DIR/vmlinuz-virt"
cp "$INITRD_FILE" "$PACKAGE_DIR/initramfs.img"
cp "$KERNEL_CMDLINE_FILE" "$PACKAGE_DIR/kernel-cmdline.txt"
cp "$VM_CONFIG" "$PACKAGE_DIR/vm-config.json"
cp "$RUN_CONTAINERFILE" "$PACKAGE_DIR/Containerfile"
cp "$RUN_QEMU_SCRIPT" "$PACKAGE_DIR/run-qemu.sh"

cat > "$PACKAGE_DIR/start.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER_IMAGE="${VM_RUNNER_IMAGE:-exasol-local-vm-runner:latest}"
CONTAINER_NAME="${VM_CONTAINER_NAME:-exasol-local-vm}"
CONFIG_FILE="$SCRIPT_DIR/vm-config.json"
SHARED_DIR="${VM_SHARED_DIR:-$SCRIPT_DIR/shared}"
VERSION_CHECK_CONFIG="$SHARED_DIR/version-check.json"

DEFAULT_VERSION_CHECK_INTERVAL_SECONDS="86400"
DEFAULT_VERSION_CHECK_IDENTITY="NONE"
DEFAULT_VERSION_CHECK_URL="https://metrics-test.exasol.com/v1/version-check"
VERSION_CHECK_ENABLED="true"
VERSION_CHECK_INTERVAL_SECONDS="$DEFAULT_VERSION_CHECK_INTERVAL_SECONDS"
VERSION_CHECK_IDENTITY="$DEFAULT_VERSION_CHECK_IDENTITY"
VERSION_CHECK_URL="$DEFAULT_VERSION_CHECK_URL"

usage() {
    cat >&2 <<USAGE
Usage: ./start.sh [version-check options]

Version-check options:
  --version-check-enabled <true|false>
  --version-check-interval-seconds <seconds>
  --version-check-identity <identity>
  --version-check-url <url>
USAGE
}

require_option_value() {
    if [ "$#" -lt 2 ] || [ -z "${2:-}" ]; then
        usage
        exit 1
    fi
}

while [ "$#" -gt 0 ]; do
    case "$1" in
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
            usage
            exit 1
            ;;
    esac
done

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "Error: $1 is required" >&2
        echo "Install podman and jq on the Linux host." >&2
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
    cat > "$VERSION_CHECK_CONFIG" <<CONFIG
{
  "enabled": $enabled,
  "interval_seconds": $interval_seconds,
  "identity": "$escaped_identity",
  "url": "$escaped_url",
  "operating_system": "$escaped_operating_system"
}
CONFIG
}

require_command podman

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Error: VM config is missing: $CONFIG_FILE" >&2
    exit 1
fi

echo "==> Building QEMU runner container: $RUNNER_IMAGE"
podman build -f "$SCRIPT_DIR/Containerfile" -t "$RUNNER_IMAGE" "$SCRIPT_DIR"

write_version_check_config

RUN_ARGS=(
    --privileged
    --rm
    -it
    --replace
    --network=host
    --name "$CONTAINER_NAME"
    -v "$SCRIPT_DIR:/vm-image:ro,Z"
)

if [ -d "$SHARED_DIR" ]; then
    RUN_ARGS+=(-v "$SHARED_DIR:/shared:Z")
fi

echo "==> Starting attached VM container: $CONTAINER_NAME"
exec podman run "${RUN_ARGS[@]}" "$RUNNER_IMAGE"
EOF

chmod +x "$PACKAGE_DIR/start.sh"

cat > "$PACKAGE_DIR/README.md" <<'EOF'
# Exasol VM for Linux

This package contains VM artifacts and a Podman-based QEMU runner.

## Prerequisites

- `podman`

QEMU, UEFI firmware, and virtiofsd are installed inside the runner container.

## Usage

Build the QEMU runner container and start the VM with the console attached:

```bash
./start.sh
```

The runner image is rebuilt by `./start.sh`; Podman will reuse cached layers
when possible.

Exit QEMU with `Ctrl-A X`. If you detach with `Ctrl-P Ctrl-Q`, stop the
container with:

```bash
podman stop exasol-local-vm
```

The `shared/` directory is mounted into the guest at `/mnt/host`.

The built disk contains:

- an EFI System Partition for boot
- an ext4 data partition labeled `exasol-data`
EOF

tar -C "$PACKAGE_ROOT" -cf - "$PACKAGE_NAME" | xz -6 -v > "$RELEASE_FILE"

echo "==> Linux package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
