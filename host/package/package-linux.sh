#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/output}"
PACKAGE_ROOT="${PACKAGE_ROOT:-$ROOT_DIR/package}"
RELEASE_ROOT="${RELEASE_ROOT:-$ROOT_DIR/release}"
RAW_DISK="$OUTPUT_DIR/disk.img"
ARCH_FILE="$OUTPUT_DIR/arch.txt"
KERNEL_FILE="$OUTPUT_DIR/vmlinuz-virt"
INITRD_FILE="$OUTPUT_DIR/initramfs.img.zst"
KERNEL_CMDLINE_FILE="$OUTPUT_DIR/kernel-cmdline.txt"
VM_CONFIG="$ROOT_DIR/config/vm-config.json"
RUN_CONTAINERFILE="$ROOT_DIR/host/run/Containerfile"

for artifact in \
    "$RAW_DISK" \
    "$ARCH_FILE" \
    "$KERNEL_FILE" \
    "$INITRD_FILE" \
    "$KERNEL_CMDLINE_FILE" \
    "$VM_CONFIG" \
    "$RUN_CONTAINERFILE"; do
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
PACKAGE_OUTPUT_DIR="$PACKAGE_DIR/output"
PACKAGE_CONFIG_DIR="$PACKAGE_DIR/config"

rm -rf "$PACKAGE_DIR"
mkdir -p "$PACKAGE_OUTPUT_DIR" "$PACKAGE_CONFIG_DIR" "$PACKAGE_DIR/shared" "$RELEASE_ROOT"

cp "$RAW_DISK" "$PACKAGE_OUTPUT_DIR/disk.img"
cp "$ARCH_FILE" "$PACKAGE_OUTPUT_DIR/arch.txt"
cp "$KERNEL_FILE" "$PACKAGE_OUTPUT_DIR/vmlinuz-virt"
cp "$INITRD_FILE" "$PACKAGE_OUTPUT_DIR/initramfs.img.zst"
cp "$KERNEL_CMDLINE_FILE" "$PACKAGE_OUTPUT_DIR/kernel-cmdline.txt"
cp "$VM_CONFIG" "$PACKAGE_CONFIG_DIR/vm-config.json"
cp "$RUN_CONTAINERFILE" "$PACKAGE_DIR/Containerfile"

cat > "$PACKAGE_DIR/start.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER_IMAGE="${VM_RUNNER_IMAGE:-exasol-nano-vm-runner:latest}"
CONTAINER_NAME="${VM_CONTAINER_NAME:-exasol-nano-vm}"
OUTPUT_DIR="${VM_OUTPUT_DIR:-$SCRIPT_DIR/output}"
CONFIG_FILE="${VM_CONFIG:-$SCRIPT_DIR/config/vm-config.json}"
SHARED_DIR="${VM_SHARED_DIR:-$SCRIPT_DIR/shared}"

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "Error: $1 is required" >&2
        echo "Install podman and jq on the Linux host." >&2
        exit 1
    fi
}

port_args_from_config() {
    jq -r '.ports[]? | [.protocol, .host] | @tsv' "$CONFIG_FILE" \
        | while IFS=$'\t' read -r protocol host_port; do
            if [ -n "$protocol" ] && [ -n "$host_port" ]; then
                printf '%s\n' "-p"
                printf '%s\n' "${host_port}:${host_port}/${protocol}"
            fi
        done
}

require_command podman
require_command jq

if [ ! -d "$OUTPUT_DIR" ]; then
    echo "Error: VM artifact directory is missing: $OUTPUT_DIR" >&2
    exit 1
fi

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Error: VM config is missing: $CONFIG_FILE" >&2
    exit 1
fi

echo "==> Building QEMU runner container: $RUNNER_IMAGE"
podman build -f "$SCRIPT_DIR/Containerfile" -t "$RUNNER_IMAGE" "$SCRIPT_DIR"

RUN_ARGS=(
    --privileged
    --rm
    -it
    --replace
    --name "$CONTAINER_NAME"
    -v "$OUTPUT_DIR:/vm-image:Z"
    -e VM_CONFIG=/vm-config.json
    -v "$CONFIG_FILE:/vm-config.json:ro,Z"
)

if [ -d "$SHARED_DIR" ]; then
    RUN_ARGS+=(-v "$SHARED_DIR:/shared:Z")
fi

while IFS= read -r port_arg; do
    RUN_ARGS+=("$port_arg")
done < <(port_args_from_config)

echo "==> Starting attached VM container: $CONTAINER_NAME"
exec podman run "${RUN_ARGS[@]}" "$RUNNER_IMAGE"
EOF

chmod +x "$PACKAGE_DIR/start.sh"

cat > "$PACKAGE_DIR/README.md" <<'EOF'
# Exasol VM for Linux

This package contains VM artifacts and a Podman-based QEMU runner.

## Prerequisites

- `podman`
- `jq`

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
podman stop exasol-nano-vm
```

The `shared/` directory is mounted into the guest at `/mnt/host`.

The built disk contains:

- an EFI System Partition for boot
- an ext4 data partition labeled `exasol-data`
EOF

tar -C "$PACKAGE_ROOT" -cf - "$PACKAGE_NAME" | xz -6 -v > "$RELEASE_FILE"

echo "==> Linux package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
