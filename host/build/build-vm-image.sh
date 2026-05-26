#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
IMAGE_DIR="${2:-/image}"
ARTIFACT_DIR="${3:-/output}"

# Inputs
IMAGE_KERNEL="${IMAGE_DIR}/boot/vmlinuz-virt"
IMAGE_VAR_DIR="${IMAGE_DIR}/var"

DISK_PADDING_SIZE="${DISK_PADDING_SIZE:-3G}"
KERNEL_CMDLINE="${KERNEL_CMDLINE:-console=hvc0}"

# Outputs
KERNEL_FILE="${ARTIFACT_DIR}/vmlinuz-virt"
INITRAMFS_FILE="${ARTIFACT_DIR}/initramfs.img"
RAW_DISK_FILE="${ARTIFACT_DIR}/disk.img"
RAW_DISK_THIN_FILE="${ARTIFACT_DIR}/disk_thin.img"
VHDX_FILE="${ARTIFACT_DIR}/disk.vhdx"
ARCH_FILE="${ARTIFACT_DIR}/arch.txt"
CMDLINE_FILE="${ARTIFACT_DIR}/kernel-cmdline.txt"

mkdir -p "${ARTIFACT_DIR}"

### write output text files
printf "%s\n" "${IMG_ARCH}" > "${ARCH_FILE}"
printf "%s\n" "${KERNEL_CMDLINE}" > "${CMDLINE_FILE}"


### copy kernel to output
cp "${IMAGE_KERNEL}" "${KERNEL_FILE}"


### write rootfs to initramfs
#
# The initramfs becomes our rootfs for the whole vm runtime, we never pivot to
# another root on a persistent disk. Data and logs go to a directory mounted
# from the host. So we pack the whole container image that we built as an
# initramfs, with only a few exceptions.
#
# We exclude /boot, which contains the kernel and default alpine initramfs
# because we don't need them and it takes up space. The default initramfs is
# never needed because we pack our whole root fs into an initramfs. The kernel
# isn't needed because both of our boot modes start it from a different place
# than /boot.
#
# Our two boot modes are:
#
#   - Thin image: kernel/initrd/cmdline are passed to the vm manager and started
#     directly, the vm doesn't need /boot at all.
#   - Fat image: We pack the kernel and initrd together into a UKI below and put
#     it in the EFI system partition. Then the firmware starts that UKI directly
#     and we don't need the separate kernel binary.
#
# We exclude /var because we pack it into the actual disk image so it can be
# grown during vm startup. We need a place where the VM can put bigger files and
# e.g. container images at runtime

pushd "${IMAGE_DIR}"
find . -xdev -not -path './boot/*' -not -path './var/*' |
    cpio --quiet -H newc -o \
        >"${INITRAMFS_FILE}"
popd



# Prepare contents for vm image partitions, as well as the definitions from
# which systemd-repart will create the partitions and final image.
COPY_SOURCE_DIR="$(mktemp -d)"
DEFINITIONS_DIR="$(mktemp -d)"
trap 'rm -rf "${COPY_SOURCE_DIR}" "${DEFINITIONS_DIR}"' EXIT

# Pack the kernel, initrd and cmdline into a single EFI binary that can be
# started directly by UEFI. This avoids the need for an actual bootloader and
# configuration, which simplifies our setup.
#
# The UKI is the only file that needs to be in the ESP and it just needs to be
# placed in the correct location to be started automatically.
#
# IMPORTANT: Use copies of kernel and initramfs as input to ukify to avoid
# any potential in-place modification of the output files
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

mkdir -p "${COPY_SOURCE_DIR}/EFI/BOOT"

# Create temporary copies for ukify to avoid any potential modification of output files
TEMP_KERNEL="$(mktemp)"
TEMP_INITRAMFS="$(mktemp)"
trap 'rm -rf "${COPY_SOURCE_DIR}" "${DEFINITIONS_DIR}" "${TEMP_KERNEL}" "${TEMP_INITRAMFS}"' EXIT
cp "${KERNEL_FILE}" "${TEMP_KERNEL}"
cp "${INITRAMFS_FILE}" "${TEMP_INITRAMFS}"

ukify build \
    --linux="${TEMP_KERNEL}" \
    --initrd="${TEMP_INITRAMFS}" \
    --cmdline="${KERNEL_CMDLINE}" \
    --stub="${EFI_STUB}" \
    --output="${COPY_SOURCE_DIR}/EFI/BOOT/${EFI_BOOT_FILE}"

# We need to copy /var to COPY_SOURCE_DIR, so systemd-repart can find it
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

# One downside of using systemd-repart is that some defaults can't be overriden
# via config. For example the minimum vfat filesystem size that it will create
# is 512MiB because of bugs in some other software.
#
# - https://github.com/systemd/systemd/issues/40774
# - https://github.com/systemd/systemd/issues/36370
#
# This means that the fat image is too big because our kernel + initrd is
# only ~75MiB. However, because the rest of the ESP is empty this compresses
# extremely well and currently we mostly care about the thin image anyway.

# Remove target files because systemd-repart refuses to overwrite them
# This is technically racy for concurrent builds but we probably don't care and
# littering `output` with temporary files is just a different failure mode.
rm -f "${RAW_DISK_THIN_FILE}" "${RAW_DISK_FILE}" "${VHDX_FILE}"

SYSTEMD_REPART_ARGS=(
    --dry-run=no
    --offline=yes
    --size=auto
    --empty=create
    # generating a correct partition table seems to require 512byte sectors?
    --sector-size=512
    --definitions="${DEFINITIONS_DIR}"
    --copy-source="${COPY_SOURCE_DIR}"
)

# Exclude esp from the thin image to save space because with the thin image
# you're passing the kernel/initrd/cmdline directly to your vm manager and don't
# need it duplicated in the esp/boot
systemd-repart "${SYSTEMD_REPART_ARGS[@]}" --exclude-partitions=esp "${RAW_DISK_THIN_FILE}"

systemd-repart "${SYSTEMD_REPART_ARGS[@]}"  "${RAW_DISK_FILE}"

qemu-img convert -f raw -O vhdx -o subformat=dynamic "${RAW_DISK_FILE}" "${VHDX_FILE}"
