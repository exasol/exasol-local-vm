#!/usr/bin/env bash
set -euo pipefail

# Start qemu and (optionally) virtiofsd and clean up again
# Uses snapshot mode to avoid modifying the disk image
#
# Easiest way to run this is via the provided Containerfile
#
#   podman build -t exasol-nano-vm-runner:latest host/run
#   podman run --rm -ti \
#     --privileged \
#     --network=host \
#     --mount="type=bind,src=${PWD}/output/aarch64,dst=/vm-image,relabel=shared,ro" \
#     exasol-nano-vm-runner:latest
#
# Or just:
#
#   IMG_ARCH=aarch64 task start-vm-attached

RUNTIME_DIR="${RUNTIME_DIR:-/run/exasol-nano-vm}"
DISK_IMG="${DISK_IMG:-/vm-image/disk.img}"
ARCH_FILE="${ARCH_FILE:-/vm-image/arch.txt}"
KERNEL_FILE="${KERNEL_FILE:-/vm-image/vmlinuz-virt}"
INITRD_FILE="${INITRD_FILE:-/vm-image/initramfs.img.zst}"
KERNEL_CMDLINE_FILE="${KERNEL_CMDLINE_FILE:-/vm-image/kernel-cmdline.txt}"
DEFAULT_VM_CONFIG="/etc/exasol-nano-vm/vm-config.json"
SHARED_DIR="${SHARED_DIR:-/shared}"
CONTAINER_MANIFEST="$SHARED_DIR/container-manifest.json"

QEMU_PID=""
VIRTIOFSD_PID=""
ORIGINAL_STTY=""

cleanup() {
    local status=$?
    trap - EXIT

    restore_terminal

    if [ -n "$QEMU_PID" ] && kill -0 "$QEMU_PID" >/dev/null 2>&1; then
        kill "$QEMU_PID" >/dev/null 2>&1 || true
        wait "$QEMU_PID" >/dev/null 2>&1 || true
    fi

    if [ -n "$VIRTIOFSD_PID" ] && kill -0 "$VIRTIOFSD_PID" >/dev/null 2>&1; then
        kill "$VIRTIOFSD_PID" >/dev/null 2>&1 || true
        wait "$VIRTIOFSD_PID" >/dev/null 2>&1 || true
    fi

    exit "$status"
}

terminate() {
    if [ -n "$QEMU_PID" ] && kill -0 "$QEMU_PID" >/dev/null 2>&1; then
        kill "$QEMU_PID" >/dev/null 2>&1 || true
    fi
}

# This allows interacting with the vm console directly so e.g. Ctrl-C behaves
# as expected and doesn't kill the vm.  It does meant that to shut down the vm
# one needs to either `doas poweroff` or use the key combinations printed below
configure_terminal() {
    if [ -t 0 ]; then
        ORIGINAL_STTY="$(stty -g 2>/dev/null || true)"
        if [ -n "$ORIGINAL_STTY" ]; then
            stty raw -echo -echoctl 2>/dev/null || true
        fi
    fi
}

restore_terminal() {
    if [ -n "$ORIGINAL_STTY" ]; then
        stty "$ORIGINAL_STTY" 2>/dev/null || true
        ORIGINAL_STTY=""
    fi
}

find_first_file() {
    local path
    for path in "$@"; do
        if [ -f "$path" ]; then
            printf '%s\n' "$path"
            return 0
        fi
    done
    return 1
}

find_first_executable() {
    local path
    for path in "$@"; do
        if [ -x "$path" ]; then
            printf '%s\n' "$path"
            return 0
        fi
    done
    return 1
}

if [ ! -f "$DISK_IMG" ]; then
    echo "Error: VM disk not found: $DISK_IMG" >&2
    echo "Mount the build output directory with: -v \"\$PWD/output/\$IMG_ARCH:/vm-image:Z\"" >&2
    exit 1
fi

if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: architecture file not found: $ARCH_FILE" >&2
    echo "Mount the build output directory with: -v \"\$PWD/output/\$IMG_ARCH:/vm-image:Z\"" >&2
    exit 1
fi

if [ ! -f "$KERNEL_FILE" ]; then
    echo "Error: kernel file not found: $KERNEL_FILE" >&2
    echo "Mount the build output directory with: -v \"\$PWD/output/\$IMG_ARCH:/vm-image:Z\"" >&2
    exit 1
fi

if [ ! -f "$INITRD_FILE" ]; then
    echo "Error: initrd file not found: $INITRD_FILE" >&2
    echo "Mount the build output directory with: -v \"\$PWD/output/\$IMG_ARCH:/vm-image:Z\"" >&2
    exit 1
fi

if [ ! -f "$KERNEL_CMDLINE_FILE" ]; then
    echo "Error: kernel command line file not found: $KERNEL_CMDLINE_FILE" >&2
    echo "Mount the build output directory with: -v \"\$PWD/output/\$IMG_ARCH:/vm-image:Z\"" >&2
    exit 1
fi

if [ -n "${VM_CONFIG:-}" ]; then
    CONFIG_FILE="$VM_CONFIG"
elif [ -f /vm-image/vm-config.json ]; then
    CONFIG_FILE="/vm-image/vm-config.json"
else
    CONFIG_FILE="$DEFAULT_VM_CONFIG"
fi

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Error: VM config not found: $CONFIG_FILE" >&2
    exit 1
fi

mkdir -p "$RUNTIME_DIR"

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
KERNEL_CMDLINE="$(tr -d '\n' < "$KERNEL_CMDLINE_FILE")"
if [ -z "$KERNEL_CMDLINE" ]; then
    echo "Error: kernel command line file is empty: $KERNEL_CMDLINE_FILE" >&2
    exit 1
fi

case "$ARCH" in
    x86_64)
        QEMU_BIN="qemu-system-x86_64"
        QEMU_MACHINE="q35"
        QEMU_CPU="qemu64"
        QEMU_CONSOLE="ttyS0,115200"
        QEMU_BIOS="$(find_first_file \
            /usr/share/ovmf/OVMF.fd \
            /usr/share/OVMF/OVMF_CODE.fd \
            /usr/share/OVMF/OVMF_CODE_4M.fd \
            /usr/share/edk2/ovmf/OVMF_CODE.fd \
            /usr/share/qemu/ovmf-x86_64.bin)" || {
            echo "Error: suitable OVMF firmware not found for x86_64" >&2
            exit 1
        }
        ;;
    aarch64)
        QEMU_BIN="qemu-system-aarch64"
        QEMU_MACHINE="virt"
        QEMU_CPU="cortex-a72"
        QEMU_CONSOLE="ttyAMA0,115200"
        QEMU_BIOS="$(find_first_file \
            /usr/share/qemu-efi-aarch64/QEMU_EFI.fd \
            /usr/share/AAVMF/AAVMF_CODE.fd \
            /usr/share/AAVMF/AAVMF_CODE_4M.fd \
            /usr/share/edk2/aarch64/QEMU_EFI.fd)" || {
            echo "Error: suitable UEFI firmware not found for aarch64" >&2
            exit 1
        }
        ;;
    *)
        echo "Error: unsupported architecture: $ARCH" >&2
        exit 1
        ;;
esac

KERNEL_CMDLINE="$KERNEL_CMDLINE console=$QEMU_CONSOLE"

HOST_ARCH="$(uname -m)"
case "$HOST_ARCH" in
    amd64) HOST_ARCH="x86_64" ;;
    arm64) HOST_ARCH="aarch64" ;;
esac

KVM_ARGS=()
KVM_STATUS="disabled"
if [ "${DISABLE_KVM:-0}" = "1" ]; then
    KVM_STATUS="disabled by DISABLE_KVM=1"
elif [ "$HOST_ARCH" != "$ARCH" ]; then
    KVM_STATUS="disabled because host architecture is $HOST_ARCH"
elif [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
    KVM_STATUS="disabled because /dev/kvm is not available"
else
    KVM_ARGS=(-enable-kvm)
    QEMU_CPU="host"
    KVM_STATUS="enabled"
fi

VM_CPUS="${VM_CPUS:-$(jq -r '.cpus // 2' "$CONFIG_FILE")}"
VM_MEMORY="${VM_MEMORY_MB:-${VM_MEMORY:-$(jq -r '.memoryMB // 2048' "$CONFIG_FILE")}}"

PORTFWD_RULES=()
if jq -e 'has("ports") and (.ports | type == "array")' "$CONFIG_FILE" >/dev/null 2>&1; then
    PORT_COUNT="$(jq -r '.ports | length' "$CONFIG_FILE")"
    for ((i = 0; i < PORT_COUNT; i++)); do
        PROTOCOL="$(jq -r ".ports[$i].protocol" "$CONFIG_FILE")"
        HOST_PORT="$(jq -r ".ports[$i].host" "$CONFIG_FILE")"
        VM_PORT="$(jq -r ".ports[$i].vm" "$CONFIG_FILE")"
        PORTFWD_RULES+=("hostfwd=${PROTOCOL}::${HOST_PORT}-:${VM_PORT}")
    done
fi

VIRTIOFS_ARGS=()
if [ -d "$SHARED_DIR" ]; then
    VIRTIOFSD="$(find_first_executable \
        /usr/libexec/virtiofsd \
        /usr/lib/qemu/virtiofsd \
        "$(command -v virtiofsd 2>/dev/null || true)")" || {
        echo "Error: virtiofsd not found" >&2
        exit 1
    }

    VIRTIOFS_SOCKET="$RUNTIME_DIR/virtiofs.sock"
    rm -f "$VIRTIOFS_SOCKET"
    "$VIRTIOFSD" \
        --socket-path="$VIRTIOFS_SOCKET" \
        --shared-dir="$SHARED_DIR" \
        --sandbox none \
        --cache=auto \
        --thread-pool-size=4 &
    VIRTIOFSD_PID=$!
    sleep 1

    if ! kill -0 "$VIRTIOFSD_PID" >/dev/null 2>&1; then
        echo "Error: virtiofsd failed to start" >&2
        exit 1
    fi

    VIRTIOFS_ARGS=(
        -chardev "socket,id=char0,path=$VIRTIOFS_SOCKET"
        -device 'vhost-user-fs-pci,chardev=char0,tag=hostshare'
        -object "memory-backend-memfd,id=mem,size=${VM_MEMORY}M,share=on"
        -numa 'node,memdev=mem'
    )

    if [ -f "${CONTAINER_MANIFEST}" ] && \
            jq -e 'has("ports") and (.ports | type == "array")' "${CONTAINER_MANIFEST}" >/dev/null 2>&1; then
        PORT_COUNT="$(jq -r '.ports | length' "${CONTAINER_MANIFEST}")"
        for ((i = 0; i < PORT_COUNT; i++)); do
            PORT="$(jq -r ".ports[$i]" "${CONTAINER_MANIFEST}")"
            PORTFWD_RULES+=("hostfwd=::${PORT}-:${PORT}")
        done
    fi

fi

NETDEV_ARGS=(-netdev 'user,id=net0')
if [ "${#PORTFWD_RULES[@]}" -gt 0 ]; then
    NETDEV_ARGS=(-netdev "user,id=net0,$(IFS=,; echo "${PORTFWD_RULES[*]}")")
fi

QEMU_ARGS=(
    -machine "$QEMU_MACHINE"
    -cpu "$QEMU_CPU"
    "${KVM_ARGS[@]}"
    -m "$VM_MEMORY"
    -smp "$VM_CPUS"
    -bios "$QEMU_BIOS"
    -kernel "$KERNEL_FILE"
    -initrd "$INITRD_FILE"
    -append "$KERNEL_CMDLINE"
    -drive "file=$DISK_IMG,format=raw,if=virtio,discard=unmap"
    -snapshot
    "${NETDEV_ARGS[@]}"
    -device 'virtio-net-pci,netdev=net0'
    "${VIRTIOFS_ARGS[@]}"
    -display 'none'
    -chardev 'stdio,id=stdio,mux=on,signal=off'
    -serial 'chardev:stdio'
    -mon 'chardev=stdio,mode=readline'
)

echo "==> Starting VM from $DISK_IMG"
echo "==> Architecture: $ARCH"
echo "==> Host architecture: $HOST_ARCH"
echo "==> QEMU binary: $QEMU_BIN"
echo "==> KVM: $KVM_STATUS"
echo "==> Firmware: $QEMU_BIOS"
echo "==> Kernel: $KERNEL_FILE"
echo "==> Initrd: $INITRD_FILE"
echo "==> Kernel command line: $KERNEL_CMDLINE"
for rule in "${PORTFWD_RULES[@]}"; do
    if [[ "$rule" =~ hostfwd=([^:]+)::([0-9]+)-:([0-9]+) ]]; then
        echo "==> Port forwarding inside container: localhost:${BASH_REMATCH[2]} -> VM:${BASH_REMATCH[3]} (${BASH_REMATCH[1]})"
    fi
done
if [ "${#VIRTIOFS_ARGS[@]}" -gt 0 ]; then
    echo "==> Shared folder: $SHARED_DIR -> /mnt/host"
fi
echo "==> Ctrl-C is sent to the VM console"
echo "==> Exit QEMU/container with Ctrl-A X, or detach with Ctrl-P Ctrl-Q and stop the container"

trap cleanup EXIT
trap terminate TERM HUP
trap '' INT
configure_terminal

trap - EXIT
exec "$QEMU_BIN" "${QEMU_ARGS[@]}"

