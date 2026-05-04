#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DISK_IMG="$SCRIPT_DIR/exasol-vm.img"
PID_FILE="$SCRIPT_DIR/qemu.pid"
SHARED_DIR="${3:-}"
VIRTIOFS_SOCKET="$SCRIPT_DIR/virtiofs.sock"
VIRTIOFSD_PID_FILE="$SCRIPT_DIR/virtiofsd.pid"
VM_LOG_FILE="$SCRIPT_DIR/vm.log"
VM_CONFIG="$SCRIPT_DIR/vm-config.json"
ARCH_FILE="$SCRIPT_DIR/arch.txt"

VM_CPUS="${1:-}"
VM_MEMORY="${2:-}"

if [ ! -f "$DISK_IMG" ]; then
    echo "Error: $DISK_IMG not found"
    exit 1
fi

if [ -z "$VM_CPUS" ] || [ -z "$VM_MEMORY" ]; then
    if [ ! -f "$VM_CONFIG" ]; then
        echo "Error: vm-config.json not found and no CPU/memory overrides were provided"
        exit 1
    fi
    VM_CPUS="${VM_CPUS:-$(jq -r '.cpus // 2' "$VM_CONFIG")}"
    VM_MEMORY="${VM_MEMORY:-$(jq -r '.memoryMB // 2048' "$VM_CONFIG")}"
fi

if [ -f "$PID_FILE" ]; then
    PID="$(cat "$PID_FILE")"
    if ps -p "$PID" >/dev/null 2>&1; then
        echo "Error: VM is already running (PID: $PID)"
        exit 1
    fi
    rm -f "$PID_FILE"
fi

PORTFWD_RULES=()
if [ -f "$VM_CONFIG" ] && [ "$(jq -r 'has("ports")' "$VM_CONFIG" 2>/dev/null || echo false)" = "true" ]; then
    PORT_COUNT="$(jq -r '.ports | length' "$VM_CONFIG" 2>/dev/null || echo 0)"
    if [ "$PORT_COUNT" -gt 0 ]; then
        for ((i=0; i<PORT_COUNT; i++)); do
            PROTOCOL="$(jq -r ".ports[$i].protocol" "$VM_CONFIG")"
            HOST_PORT="$(jq -r ".ports[$i].host" "$VM_CONFIG")"
            VM_PORT="$(jq -r ".ports[$i].vm" "$VM_CONFIG")"
            PORTFWD_RULES+=("hostfwd=${PROTOCOL}::${HOST_PORT}-:${VM_PORT}")
        done
    fi
fi

NETDEV_ARGS=(-netdev user,id=net0)
if [ "${#PORTFWD_RULES[@]}" -gt 0 ]; then
    NETDEV_ARGS=(-netdev "user,id=net0,$(IFS=,; echo "${PORTFWD_RULES[*]}")")
fi

VIRTIOFS_ARGS=()
if [ -n "$SHARED_DIR" ]; then
    if [ ! -d "$SHARED_DIR" ]; then
        echo "Error: shared directory not found: $SHARED_DIR"
        exit 1
    fi
    rm -f "$VIRTIOFSD_PID_FILE" "$VIRTIOFS_SOCKET"
    /usr/libexec/virtiofsd \
        --socket-path="$VIRTIOFS_SOCKET" \
        --shared-dir="$SHARED_DIR" \
        --sandbox none \
        --cache=auto \
        --thread-pool-size=4 &
    VIRTIOFSD_PID=$!
    echo "$VIRTIOFSD_PID" > "$VIRTIOFSD_PID_FILE"
    sleep 1
    VIRTIOFS_ARGS=(
        -chardev "socket,id=char0,path=$VIRTIOFS_SOCKET"
        -device vhost-user-fs-pci,chardev=char0,tag=hostshare
        -object "memory-backend-file,id=mem,size=${VM_MEMORY}M,mem-path=/dev/shm,share=on"
        -numa node,memdev=mem
    )
fi

if [ ! -f "$ARCH_FILE" ]; then
    echo "Error: arch.txt not found in package directory"
    exit 1
fi

ARCH="$(tr -d '\n' < "$ARCH_FILE")"
case "$ARCH" in
    x86_64)
        QEMU_BIN="qemu-system-x86_64"
        QEMU_MACHINE="q35"
        QEMU_CPU="qemu64"
        for path in /usr/share/ovmf/OVMF.fd /usr/share/OVMF/OVMF_CODE.fd /usr/share/edk2/ovmf/OVMF_CODE.fd /usr/share/qemu/ovmf-x86_64.bin; do
            if [ -f "$path" ]; then
                QEMU_BIOS="$path"
                break
            fi
        done
        ;;
    aarch64)
        QEMU_BIN="qemu-system-aarch64"
        QEMU_MACHINE="virt"
        QEMU_CPU="cortex-a72"
        QEMU_BIOS="/usr/share/qemu-efi-aarch64/QEMU_EFI.fd"
        ;;
    *)
        echo "Error: unsupported architecture: $ARCH"
        exit 1
        ;;
esac

if [ -z "${QEMU_BIOS:-}" ] || [ ! -f "$QEMU_BIOS" ]; then
    echo "Error: suitable UEFI firmware not found for $ARCH"
    exit 1
fi

: > "$VM_LOG_FILE"
QEMU_ARGS=(
    -machine "$QEMU_MACHINE"
    -cpu "$QEMU_CPU"
    -m "$VM_MEMORY"
    -smp "$VM_CPUS"
    -bios "$QEMU_BIOS"
    -drive "file=$DISK_IMG,format=raw,if=virtio"
    "${NETDEV_ARGS[@]}"
    -device virtio-net-pci,netdev=net0
    "${VIRTIOFS_ARGS[@]}"
    -serial "file:$VM_LOG_FILE"
    -daemonize
    -pidfile "$PID_FILE"
    -display none
)

"$QEMU_BIN" "${QEMU_ARGS[@]}"

echo "==> Linux package VM started (PID: $(cat "$PID_FILE"))"
echo "==> Console output: tail -f $VM_LOG_FILE"
