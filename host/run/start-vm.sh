#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DISK_IMG="$ROOT_DIR/output/disk.img"
PID_FILE="$ROOT_DIR/qemu.pid"
SHARED_DIR="$ROOT_DIR/shared"
VIRTIOFS_SOCKET="$ROOT_DIR/virtiofs.sock"
VIRTIOFSD_PID_FILE="$ROOT_DIR/virtiofsd.pid"
VM_LOG_FILE="$ROOT_DIR/vm.log"
VM_CONFIG="$ROOT_DIR/config/vm-config.json"

ATTACHED=false
if [ "${1:-}" = "--attached" ]; then
    ATTACHED=true
fi

if [ ! -f "$DISK_IMG" ]; then
    echo "Error: $DISK_IMG not found. Run 'task build' first."
    exit 1
fi

if [ -f "$PID_FILE" ]; then
    PID="$(cat "$PID_FILE")"
    if ps -p "$PID" >/dev/null 2>&1; then
        echo "Error: VM is already running (PID: $PID)"
        exit 1
    fi
    rm -f "$PID_FILE"
fi

mkdir -p "$SHARED_DIR"
rm -f "$VIRTIOFSD_PID_FILE" "$VIRTIOFS_SOCKET"
: > "$VM_LOG_FILE"

if [ -f "$VM_CONFIG" ]; then
    VM_CPUS="$(jq -r '.cpus // 2' "$VM_CONFIG")"
    VM_MEMORY="$(jq -r '.memoryMB // 2048' "$VM_CONFIG")"
else
    VM_CPUS=2
    VM_MEMORY=2048
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
if [ -x /usr/libexec/virtiofsd ]; then
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

echo "==> Starting VM from $DISK_IMG"
for rule in "${PORTFWD_RULES[@]}"; do
    if [[ "$rule" =~ hostfwd=([^:]+)::([0-9]+)-:([0-9]+) ]]; then
        echo "==> Port forwarding: localhost:${BASH_REMATCH[2]} -> VM:${BASH_REMATCH[3]} (${BASH_REMATCH[1]})"
    fi
done
if [ "${#VIRTIOFS_ARGS[@]}" -gt 0 ]; then
    echo "==> Shared folder: $SHARED_DIR -> /mnt/host"
fi

source "$ROOT_DIR/host/run/get-qemu-args.sh"

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
)

if [ "$ATTACHED" = "true" ]; then
    "$QEMU_BIN" "${QEMU_ARGS[@]}" -nographic
    rm -f "$PID_FILE"
else
    "$QEMU_BIN" "${QEMU_ARGS[@]}" -daemonize -pidfile "$PID_FILE" -display none
    echo "==> VM started successfully (PID: $(cat "$PID_FILE"))"
    echo "==> Console output: tail -f $VM_LOG_FILE"
fi
