#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
IMAGE_DIR="${2:-/image}"
ARTIFACT_DIR="${3:-/output}"

IMAGE_KERNEL="${IMAGE_DIR}/boot/vmlinuz-virt"
IMAGE_VAR_DIR="${IMAGE_DIR}/var"

KERNEL_FILE="${ARTIFACT_DIR}/vmlinuz-virt"
INITRAMFS_FILE="${ARTIFACT_DIR}/initramfs.img.zst"
RAW_DISK_FILE="${ARTIFACT_DIR}/disk.img"
RAW_DISK_THIN_FILE="${ARTIFACT_DIR}/disk_thin.img"
VHDX_FILE="${ARTIFACT_DIR}/disk.vhdx"
ARCH_FILE="${ARTIFACT_DIR}/arch.txt"
CMDLINE_FILE="${ARTIFACT_DIR}/kernel-cmdline.txt"

DISK_PADDING_SIZE="${DISK_PADDING_SIZE:-3G}"
KERNEL_CMDLINE="${KERNEL_CMDLINE:-}"

case "${IMG_ARCH}" in
    x86_64)
        EFI_STUB="/usr/lib/systemd/boot/efi/linuxx64.efi.stub"
        EFI_BOOT_FILE="BOOTX64.EFI"
        ;;
    aarch64)
        EFI_STUB="/usr/lib/systemd/boot/efi/linuxaa64.efi.stub"
        EFI_BOOT_FILE="BOOTAA64.EFI"
        ;;
    *)
        echo "unsupported build architecture: ${IMG_ARCH}" >&2
        exit 1
        ;;
esac

mkdir -p "${ARTIFACT_DIR}"


### write metadata

printf "%s\n" "${IMG_ARCH}" > "${ARCH_FILE}"
printf "%s\n" "${KERNEL_CMDLINE}" > "${CMDLINE_FILE}"


### copy kernel

cp "${IMAGE_KERNEL}" "${KERNEL_FILE}"


### write initramfs

pushd "${IMAGE_DIR}"
find . -xdev -not -path './boot/*' -not -path './var/*' |
    cpio --quiet -H newc -o |
    zstdmt -9 \
        >"${INITRAMFS_FILE}"
popd


### prepare image write

COPY_SOURCE_DIR="$(mktemp -d)"
DEFINITIONS_DIR="$(mktemp -d)"
trap 'rm -rf "${COPY_SOURCE_DIR}" "${DEFINITIONS_DIR}"' EXIT


### prepare image contents

mkdir -p "${COPY_SOURCE_DIR}/EFI/BOOT"

ukify build \
    --linux="${KERNEL_FILE}" \
    --initrd="${INITRAMFS_FILE}" \
    --cmdline="${KERNEL_CMDLINE}" \
    --stub="${EFI_STUB}" \
    --output="${COPY_SOURCE_DIR}/EFI/BOOT/${EFI_BOOT_FILE}"

cp -a "${IMAGE_VAR_DIR}" "${COPY_SOURCE_DIR}/var"

### declare images

cat > "${DEFINITIONS_DIR}/10-esp.conf" <<EOF
[Partition]
Type=esp
Label=EFI
Format=vfat
Minimize=guess
CopyFiles=/EFI:/EFI
EOF

cat > "${DEFINITIONS_DIR}/20-data.conf" <<EOF
[Partition]
Type=linux-generic
Label=exasol-data
Format=ext4
Minimize=guess
CopyFiles=/var:/
PaddingMinBytes=${DISK_PADDING_SIZE}
EOF


### write images

# remove because systemd-repart refuses to overwrite them
# racy for concurrent builds but we probably don't care and littering `output`
# with temporary files is just a different failure mode.
rm -f "${RAW_DISK_THIN_FILE}" "${RAW_DISK_FILE}" "${VHDX_FILE}"

systemd-repart \
    --dry-run=no \
    --offline=yes \
    --size=auto \
    --empty=create \
    --sector-size=512 \
    --definitions="${DEFINITIONS_DIR}" \
    --copy-source="${COPY_SOURCE_DIR}" \
    --exclude-partitions=esp \
    "${RAW_DISK_THIN_FILE}"

systemd-repart \
    --dry-run=no \
    --offline=yes \
    --size=auto \
    --empty=create \
    --sector-size=512 \
    --definitions="${DEFINITIONS_DIR}" \
    --copy-source="${COPY_SOURCE_DIR}" \
    "${RAW_DISK_FILE}"

qemu-img convert -f raw -O vhdx -o subformat=dynamic "${RAW_DISK_FILE}" "${VHDX_FILE}"
