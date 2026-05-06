#!/bin/sh
set -eu

ARTIFACT_DIR="${1:-/artifacts}"
KERNEL_FILE="${ARTIFACT_DIR}/vmlinuz-virt"
INITRAMFS_FILE="${ARTIFACT_DIR}/initramfs.img.zst"
UKI_FILE="${ARTIFACT_DIR}/vm.efi"
RAW_DISK_FILE="${ARTIFACT_DIR}/disk.img"
RAW_DISK_THIN_FILE="${ARTIFACT_DIR}/disk_thin.img"
VHDX_FILE="${ARTIFACT_DIR}/disk.vhdx"
ARCH_FILE="${ARTIFACT_DIR}/arch.txt"
CMDLINE_FILE="${ARTIFACT_DIR}/kernel-cmdline.txt"
LAYOUT_FILE="${ARTIFACT_DIR}/disk-layout.json"

DISK_PADDING_SIZE="${DISK_PADDING_SIZE:-3G}"
KERNEL_CMDLINE="${KERNEL_CMDLINE:-console=tty0 console=ttyS0,115200 console=ttyAMA0,115200 console=hvc0}"

mkdir -p "${ARTIFACT_DIR}"

if [ ! -f "${KERNEL_FILE}" ]; then
    echo "missing kernel artifact: ${KERNEL_FILE}" >&2
    exit 1
fi

if [ ! -f "${INITRAMFS_FILE}" ]; then
    echo "missing initramfs artifact: ${INITRAMFS_FILE}" >&2
    exit 1
fi

ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64)
        EFI_STUB="/usr/lib/systemd/boot/efi/linuxx64.efi.stub"
        EFI_BOOT_FILE="BOOTX64.EFI"
        ;;
    aarch64)
        EFI_STUB="/usr/lib/systemd/boot/efi/linuxaa64.efi.stub"
        EFI_BOOT_FILE="BOOTAA64.EFI"
        ;;
    *)
        echo "unsupported build architecture: ${ARCH}" >&2
        exit 1
        ;;
esac

printf "%s\n" "${ARCH}" > "${ARCH_FILE}"
printf "%s\n" "${KERNEL_CMDLINE}" > "${CMDLINE_FILE}"

ukify build \
    --linux="${KERNEL_FILE}" \
    --initrd="${INITRAMFS_FILE}" \
    --cmdline="${KERNEL_CMDLINE}" \
    --stub="${EFI_STUB}" \
    --output="${UKI_FILE}"

COPY_SOURCE_DIR="$(mktemp -d)"
DEFINITIONS_DIR="$(mktemp -d)"
trap 'rm -rf "${COPY_SOURCE_DIR}" "${DEFINITIONS_DIR}"' EXIT

mkdir -p "${COPY_SOURCE_DIR}/EFI/BOOT"
cp "${UKI_FILE}" "${COPY_SOURCE_DIR}/EFI/BOOT/${EFI_BOOT_FILE}"
cp -a /artifacts/var "${COPY_SOURCE_DIR}/var"

cat > "${DEFINITIONS_DIR}/20-data.conf" <<EOF
[Partition]
Type=linux-generic
Label=exasol-data
Format=ext4
Minimize=guess
CopyFiles=/var:/
PaddingMinBytes=${DISK_PADDING_SIZE}
EOF

systemd-repart \
    --dry-run=no \
    --offline=yes \
    --size=auto \
    --empty=create \
    --sector-size=512 \
    --definitions="${DEFINITIONS_DIR}" \
    --copy-source="${COPY_SOURCE_DIR}" \
    "${RAW_DISK_THIN_FILE}"

cat > "${DEFINITIONS_DIR}/10-esp.conf" <<EOF
[Partition]
Type=esp
Label=EFI
Format=vfat
Minimize=guess
CopyFiles=/EFI:/EFI
EOF

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

cat > "${LAYOUT_FILE}" <<EOF
{
  "arch": "${ARCH}",
  "efiBootFile": "${EFI_BOOT_FILE}",
  "kernelCmdline": "${KERNEL_CMDLINE}"
}
EOF
