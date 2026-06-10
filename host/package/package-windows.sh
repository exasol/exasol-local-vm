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
VHDX_DISK="$ROOT_DIR/output/${IMG_ARCH}/disk.vhdx"
ARCH_FILE="$ROOT_DIR/output/${IMG_ARCH}/arch.txt"
HYPERV_SCRIPT="$ROOT_DIR/host/run/start-hyperv.ps1"
VM_CONFIG="$ROOT_DIR/host/run/vm-config.json"

if [ ! -f "$VHDX_DISK" ]; then
    echo "Error: $VHDX_DISK not found. Run 'task build' first."
    exit 1
fi

if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: $ARCH_FILE not found."
    exit 1
fi

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
case "$ARCH" in
    x86_64) PACKAGE_NAME="windows-x86_64" ;;
    aarch64) PACKAGE_NAME="windows-arm64" ;;
    *) echo "Error: unknown architecture: $ARCH" >&2; exit 1 ;;
esac

PACKAGE_DIR="$ROOT_DIR/package/$PACKAGE_NAME"
RELEASE_FILE="$ROOT_DIR/release/$PACKAGE_NAME.tar.xz"

mkdir -p "$PACKAGE_DIR" "$ROOT_DIR/release"
cp "$VHDX_DISK" "$PACKAGE_DIR/exasol-vm.vhdx"
cp "$ARCH_FILE" "$PACKAGE_DIR/arch.txt"
cp "$VM_CONFIG" "$PACKAGE_DIR/vm-config.json"
cp "$HYPERV_SCRIPT" "$PACKAGE_DIR/start.ps1"

cat > "$PACKAGE_DIR/README.md" <<'EOF'
# Exasol VM for Windows

This package contains a Hyper-V-ready VHDX image and a PowerShell launcher.

## Prerequisites

- Windows 10/11 Pro or Enterprise
- Hyper-V enabled

## Usage

Run PowerShell as Administrator:

```powershell
.\start.ps1
```

Override CPUs and memory:

```powershell
.\start.ps1 4 4096
```

After startup the script writes the guest IP address to `vm-ip.txt` when it becomes available.
Use the ports declared in `vm-config.json` to connect to services inside the guest.

The built-in disk already contains:

- an EFI System Partition for boot
- an ext4 data partition labeled `exasol-data`
EOF

tar -C "$ROOT_DIR/package" -cf - "$PACKAGE_NAME" | xz -6 -v > "$RELEASE_FILE"

echo "==> Windows package created: $PACKAGE_DIR"
echo "==> Release archive: $RELEASE_FILE"
