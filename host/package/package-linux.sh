#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RAW_DISK="$ROOT_DIR/output/disk.img"
ARCH_FILE="$ROOT_DIR/output/arch.txt"
LINUX_SCRIPT="$ROOT_DIR/host/run/start-linux.sh"
VM_CONFIG="$ROOT_DIR/config/vm-config.json"

if [ ! -f "$RAW_DISK" ]; then
    echo "Error: $RAW_DISK not found. Run 'task build' first."
    exit 1
fi

if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: $ARCH_FILE not found."
    exit 1
fi

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
case "$ARCH" in
    x86_64) PACKAGE_NAME="linux-x86_64" ;;
    aarch64) PACKAGE_NAME="linux-arm64" ;;
    *) echo "Error: unknown architecture: $ARCH" >&2; exit 1 ;;
esac

PACKAGE_DIR="$ROOT_DIR/package/$PACKAGE_NAME"
RELEASE_FILE="$ROOT_DIR/release/$PACKAGE_NAME.tar.xz"

mkdir -p "$PACKAGE_DIR" "$ROOT_DIR/release"
cp "$RAW_DISK" "$PACKAGE_DIR/exasol-vm.img"
cp "$ARCH_FILE" "$PACKAGE_DIR/arch.txt"
cp "$VM_CONFIG" "$PACKAGE_DIR/vm-config.json"
cp "$LINUX_SCRIPT" "$PACKAGE_DIR/start.sh"
chmod +x "$PACKAGE_DIR/start.sh"

cat > "$PACKAGE_DIR/README.md" <<'EOF'
# Exasol VM for Linux

This package contains a raw UEFI disk image and a QEMU launcher.

## Prerequisites

- `qemu-system-x86_64` or `qemu-system-aarch64`
- UEFI firmware (`ovmf` for x86_64, `qemu-efi-aarch64` for arm64)
- `jq`
- Optional for folder sharing: `virtiofsd`

## Usage

Start with defaults from `vm-config.json`:

```bash
./start.sh
```

Override CPUs and memory:

```bash
./start.sh 4 4096
```

Share a host directory through virtiofs:

```bash
./start.sh 2 2048 /path/to/shared
```

The built-in disk already contains:

- an EFI System Partition for boot
- an ext4 data partition labeled `exasol-data`
EOF

tar -C "$ROOT_DIR/package" -cf - "$PACKAGE_NAME" | xz -6 -v > "$RELEASE_FILE"

echo "==> Linux package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
