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
INITRD_FILE="$OUTPUT_DIR/initramfs.img"
KERNEL_CMDLINE_FILE="$OUTPUT_DIR/kernel-cmdline.txt"

ARCH_FILE="$OUTPUT_DIR/arch.txt"
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
cp "$RAW_DISK" "$PACKAGE_DIR/disk_thin.img"
cp "$KERNEL_FILE" "$PACKAGE_DIR/vmlinuz-virt"
cp "$INITRD_FILE" "$PACKAGE_DIR/initramfs.img"
cp "$KERNEL_CMDLINE_FILE" "$PACKAGE_DIR/kernel-cmdline.txt"

curl -fSL -o "$PACKAGE_DIR/gvproxy" "$GVPROXY_URL"
chmod +x "$PACKAGE_DIR/gvproxy"

# Create the release archive first (without launcher)
tar -C "$ROOT_DIR/package" -cf - "$PACKAGE_NAME" | xz -6 -v > "$RELEASE_FILE"

echo "==> macOS package archive created: $RELEASE_FILE"

# Build the Go launcher with embedded release archive
echo "==> Building macOS launcher..."
LAUNCHER_DIR="$ROOT_DIR/launcher/mac"
pushd "$LAUNCHER_DIR" > /dev/null

# Copy the release archive to be embedded
cp "$RELEASE_FILE" vm-package.tar.xz

echo "==> macOS package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
