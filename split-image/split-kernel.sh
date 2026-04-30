#!/usr/bin/env bash
# Extract the ARM64 kernel and initramfs from disk.img for vfkit direct boot.
#
# Alpine vmlinuz-virt is an EFI zboot image (PE32+ wrapper around a compressed ARM64 Image).
# vfkit requires a raw, uncompressed ARM64 kernel with the correct magic at offset 56.
# This script uses the bundled unzboot binary to perform the extraction.
#
# Output files written to split-image/package/:
#   kernel          - raw ARM64 kernel (extracted from vmlinuz-virt)
#   initramfs       - initrd
#   cmdline         - kernel command line (console=hvc0 appended)
#   vm-config.json  - port forwarding config (copied from package/mac-arm64/)
#   start-direct.sh - vfkit start script using linux direct boot + gvproxy

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PACKAGE_DIR="$SCRIPT_DIR/package"
UNZBOOT="$SCRIPT_DIR/unzboot"
DISK_IMG="$PROJECT_ROOT/disk.img"

mkdir -p "$PACKAGE_DIR"

if [ ! -f "$DISK_IMG" ]; then
    echo "Error: $DISK_IMG not found"
    exit 1
fi

if [ ! -x "$UNZBOOT" ]; then
    echo "Error: unzboot not found at $UNZBOOT"
    exit 1
fi

echo "==> Setting up loop device..."
LOOP=$(sudo losetup -f --show -P "$DISK_IMG")
echo "    Loop device: $LOOP"

ROOTFS_MOUNT="/tmp/rootfs-patch-$$"

cleanup() {
    echo "==> Cleaning up..."
    sudo umount "$ROOTFS_MOUNT" 2>/dev/null || true
    rmdir "$ROOTFS_MOUNT" 2>/dev/null || true
    sudo losetup -d "$LOOP" 2>/dev/null || true
    sudo umount /tmp/proto-mount 2>/dev/null || true
    rm -rf /tmp/proto-mount
}
trap cleanup EXIT

# Find the root partition (the ext4 partition)
ROOT_PART=""
for part in "${LOOP}p1" "${LOOP}p2" "${LOOP}p3"; do
    if [ -b "$part" ]; then
        FSTYPE=$(sudo blkid -p -o value -s TYPE "$part" 2>/dev/null || echo "")
        if [ "$FSTYPE" = "ext4" ]; then
            ROOT_PART="$part"
            echo "    Root partition: $ROOT_PART (ext4)"
            break
        fi
    fi
done

if [ -z "$ROOT_PART" ]; then
    echo "Error: could not find ext4 root partition in $DISK_IMG"
    sudo lsblk "$LOOP"
    exit 1
fi

echo "==> Mounting root partition..."
mkdir -p /tmp/proto-mount
sudo mount -o ro "$ROOT_PART" /tmp/proto-mount

echo "==> Locating kernel files..."
VMLINUZ=$(sudo find /tmp/proto-mount/boot -name "vmlinuz-virt" 2>/dev/null | head -1)
INITRAMFS=$(sudo find /tmp/proto-mount/boot -name "initramfs-virt" 2>/dev/null | head -1)
GRUB_CFG=$(sudo find /tmp/proto-mount -name "grub.cfg" 2>/dev/null | head -1)

if [ -z "$VMLINUZ" ]; then
    echo "Error: vmlinuz-virt not found in /boot"
    exit 1
fi
if [ -z "$INITRAMFS" ]; then
    echo "Error: initramfs-virt not found in /boot"
    exit 1
fi

echo "    vmlinuz: $VMLINUZ"
echo "    initramfs: $INITRAMFS"

echo "==> Extracting kernel cmdline from grub.cfg..."
KERNEL_CMDLINE=""
if [ -n "$GRUB_CFG" ]; then
    KERNEL_CMDLINE=$(sudo grep -oP '(?<=linux\s/boot/vmlinuz-virt\s).*' "$GRUB_CFG" | head -1 || true)
    echo "    grub.cfg: $GRUB_CFG"
fi
if [ -z "$KERNEL_CMDLINE" ]; then
    echo "    Warning: grub.cfg not found or no cmdline; using fallback"
    ROOT_UUID=$(sudo blkid -p -o value -s UUID "$ROOT_PART" 2>/dev/null || echo "")
    if [ -n "$ROOT_UUID" ]; then
        KERNEL_CMDLINE="root=UUID=$ROOT_UUID ro modules=sd-mod,usb-storage,ext4"
    else
        KERNEL_CMDLINE="root=/dev/vda ro"
    fi
fi
echo "    cmdline: $KERNEL_CMDLINE"

echo "==> Extracting root partition..."
sudo dd if="$ROOT_PART" of="$PACKAGE_DIR/rootfs.img" bs=4M conv=sparse status=progress
sudo chown "$(id -u):$(id -g)" "$PACKAGE_DIR/rootfs.img"
sudo chmod 644 "$PACKAGE_DIR/rootfs.img"
echo "    rootfs size: $(du -sh "$PACKAGE_DIR/rootfs.img" | cut -f1) (apparent: $(du -sh --apparent-size "$PACKAGE_DIR/rootfs.img" | cut -f1))"

echo "==> Patching rootfs.img..."
mkdir -p "$ROOTFS_MOUNT"
sudo mount "$PACKAGE_DIR/rootfs.img" "$ROOTFS_MOUNT"

# Add nofail to /boot/efi fstab entry — no EFI partition exists in rootfs.img
if sudo grep -q "/boot/efi" "$ROOTFS_MOUNT/etc/fstab" 2>/dev/null; then
    sudo sed -i '/\/boot\/efi/{/nofail/!s/defaults/defaults,nofail/}' "$ROOTFS_MOUNT/etc/fstab"
    echo "    Added nofail to /boot/efi fstab entry"
fi

# Change virtiofs fstab entry to noauto — busybox mount ignores nofail, so mount -a
# would fail localmount when no device is attached. setup-logging.initd mounts it instead.
if sudo grep -q "virtiofs" "$ROOTFS_MOUNT/etc/fstab" 2>/dev/null; then
    sudo sed -i '/virtiofs/s/defaults,nofail/noauto/' "$ROOTFS_MOUNT/etc/fstab"
    echo "    Changed virtiofs fstab entry to noauto"
fi

# Update setup-logging.initd from source — includes virtiofs mount attempt and
# dangling /var/log symlink fix
sudo cp "$PROJECT_ROOT/cloud-init/guest-scripts/setup-logging.initd" \
    "$ROOTFS_MOUNT/etc/init.d/setup-logging"
echo "    Updated setup-logging.initd"

# Restore /var/log to a real directory if it was replaced by a symlink pointing at
# /mnt/host/.system-logs on a previous boot with the shared folder mounted
if sudo test -L "$ROOTFS_MOUNT/var/log" && ! sudo test -e "$ROOTFS_MOUNT/var/log"; then
    sudo rm -f "$ROOTFS_MOUNT/var/log"
    sudo mkdir -p "$ROOTFS_MOUNT/var/log"
    echo "    Restored /var/log as directory (was dangling symlink)"
fi

sudo umount "$ROOTFS_MOUNT"
rmdir "$ROOTFS_MOUNT"
echo "    Patching complete"

echo "==> Copying vmlinuz-virt from disk image..."
sudo cp "$VMLINUZ" "$PACKAGE_DIR/vmlinuz-virt.tmp"
sudo chmod 644 "$PACKAGE_DIR/vmlinuz-virt.tmp"

echo "==> Copying initramfs-virt..."
sudo cp "$INITRAMFS" "$PACKAGE_DIR/initramfs"
sudo chown "$(id -u):$(id -g)" "$PACKAGE_DIR/initramfs"
sudo chmod 644 "$PACKAGE_DIR/initramfs"

echo "==> Running unzboot to extract raw ARM64 kernel..."
"$UNZBOOT" "$PACKAGE_DIR/vmlinuz-virt.tmp" "$PACKAGE_DIR/kernel"
rm -f "$PACKAGE_DIR/vmlinuz-virt.tmp"

echo "==> Verifying ARM64 magic at offset 56..."
MAGIC=$(xxd -s 56 -l 4 -p "$PACKAGE_DIR/kernel" 2>/dev/null || od -An -tx1 -j56 -N4 "$PACKAGE_DIR/kernel" | tr -d ' \n')
echo "    Magic bytes: $MAGIC"
if [[ "$MAGIC" == "41524d64" ]]; then
    echo "    ARM64 magic verified (41524d64 = 'ARM\x64')"
else
    echo "    Warning: ARM64 magic not found (got: $MAGIC) — vfkit may reject this kernel"
fi

echo "==> Writing cmdline file..."
echo "$KERNEL_CMDLINE console=hvc0" > "$PACKAGE_DIR/cmdline"
echo "    cmdline: $(cat "$PACKAGE_DIR/cmdline")"

echo "==> Copying vm-config.json..."
cp "$PROJECT_ROOT/package/mac-arm64/vm-config.json" "$PACKAGE_DIR/vm-config.json"

echo ""
echo "==> Done! Files written to $PACKAGE_DIR:"
ls -lh "$PACKAGE_DIR/rootfs.img" "$PACKAGE_DIR/kernel" "$PACKAGE_DIR/initramfs" \
        "$PACKAGE_DIR/cmdline" "$PACKAGE_DIR/vm-config.json"
echo ""
echo "To start the VM on Linux:  task start-direct"
echo "To start the VM on macOS:  ./split-image/start-vfkit.sh [cpus] [memory_mb] [shared_dir]"
