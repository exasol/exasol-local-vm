#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-$ROOT_DIR/output/${IMG_ARCH}}"

RAW_DISK="$OUTPUT_DIR/disk_thin.img"
KERNEL_FILE="$OUTPUT_DIR/vmlinuz-virt"
INITRD_FILE="$OUTPUT_DIR/initramfs.img.zst"
KERNEL_CMDLINE_FILE="$OUTPUT_DIR/kernel-cmdline.txt"

ARCH_FILE="$OUTPUT_DIR/arch.txt"
VM_CONFIG="$ROOT_DIR/host/run/vm-config.json"
GVPROXY_VERSION="v0.8.8"
GVPROXY_URL="https://github.com/containers/gvisor-tap-vsock/releases/download/${GVPROXY_VERSION}/gvproxy-darwin"

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
    x86_64) PACKAGE_NAME="mac-x86_64" ;;
    aarch64) PACKAGE_NAME="mac-arm64" ;;
    *) echo "Error: unknown architecture: $ARCH" >&2; exit 1 ;;
esac

PACKAGE_DIR="$ROOT_DIR/package/$PACKAGE_NAME"
RELEASE_FILE="$ROOT_DIR/release/$PACKAGE_NAME.tar.xz"

mkdir -p "$PACKAGE_DIR" "$ROOT_DIR/release"
cp "$RAW_DISK" "$PACKAGE_DIR/exasol-vm.img"
cp "$VM_CONFIG" "$PACKAGE_DIR/vm-config.json"
cp "$KERNEL_FILE" "$PACKAGE_DIR/vmlinuz-virt"
cp "$INITRD_FILE" "$PACKAGE_DIR/initramfs.img.zst"
cp "$KERNEL_CMDLINE_FILE" "$PACKAGE_DIR/kernel-cmdline.txt"

curl -fSL -o "$PACKAGE_DIR/gvproxy" "$GVPROXY_URL"
chmod +x "$PACKAGE_DIR/gvproxy"

cat > "$PACKAGE_DIR/README.md" <<'EOF'
# Exasol VM for macOS

This package contains a raw UEFI disk image and a `vfkit` launcher.

## Prerequisites

- macOS 13+
- `vfkit`
- `jq`

Install dependencies with Homebrew:

```bash
brew install vfkit jq
```

## Usage

Start with defaults from `vm-config.json`:

```bash
./start.sh
```

Override CPUs and memory:

```bash
./start.sh 4 4096
```

Share a host directory through virtio-fs:

```bash
./start.sh 2 2048 /path/to/shared
```

The launcher uses the bundled `gvproxy` binary to expose the TCP ports declared in `vm-config.json`.

The built-in disk already contains:

- an EFI System Partition for boot
- an ext4 data partition labeled `exasol-data`
EOF

tar -C "$ROOT_DIR/package" -cf - "$PACKAGE_NAME" | xz -6 -v > "$RELEASE_FILE"

echo "==> macOS package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
