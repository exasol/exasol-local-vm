#!/usr/bin/env bash
set -euo pipefail

# Usage: ./start-qemu.sh [cpu_count] [memory_mb] [shared_directory]
# All arguments optional. Defaults: 2 CPUs, 2048 MB RAM, no folder sharing.
# Port forwards are read from split-image/package/vm-config.json.
# Run 'task split-image' first to populate split-image/package/.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_DIR="$SCRIPT_DIR/package"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DISK_IMG="$PACKAGE_DIR/rootfs.img"
KERNEL="$PACKAGE_DIR/kernel"
INITRD="$PACKAGE_DIR/initramfs"
CMDLINE_FILE="$PACKAGE_DIR/cmdline"
VM_CONFIG="$PACKAGE_DIR/vm-config.json"
SSH_KEY="$PROJECT_ROOT/vm-key"
VIRTIOFS_SOCKET="$PACKAGE_DIR/virtiofs.sock"
VIRTIOFSD_PID_FILE="$PACKAGE_DIR/virtiofsd.pid"
PID_FILE="$PACKAGE_DIR/qemu.pid"
LOG_FILE="$PACKAGE_DIR/vm-console.log"
SSH_PORT=2222

CPUS="${1:-2}"
MEMORY_MB="${2:-2048}"
SHARED_DIR="${3:-}"

for f in "$DISK_IMG" "$KERNEL" "$INITRD" "$CMDLINE_FILE"; do
    if [ ! -f "$f" ]; then
        echo "Error: required file missing: $f"
        echo "Run 'task split-image' first."
        exit 1
    fi
done

if ! command -v qemu-system-aarch64 &>/dev/null; then
    echo "Error: qemu-system-aarch64 not found. Install with: sudo apt-get install qemu-system-aarch64"
    exit 1
fi
if ! command -v jq &>/dev/null; then
    echo "Error: jq is required. Install with: sudo apt-get install jq"
    exit 1
fi

# Check for stale PID
if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
        echo "Error: VM already running (PID $PID)"
        exit 1
    fi
    rm -f "$PID_FILE"
fi

CMDLINE=$(cat "$CMDLINE_FILE")

SHARED_DIR_ABS=""
if [ -n "$SHARED_DIR" ]; then
    if [ ! -d "$SHARED_DIR" ]; then
        echo "Error: Shared directory not found: $SHARED_DIR"
        exit 1
    fi
    SHARED_DIR_ABS="$(cd "$SHARED_DIR" && pwd)"
fi

# KVM acceleration when host is also aarch64
QEMU_CPU="cortex-a72"
QEMU_ACCEL="tcg,thread=multi"
if [ "$(uname -m)" = "aarch64" ] && [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    QEMU_CPU="host"
    QEMU_ACCEL="kvm"
    echo "==> KVM acceleration enabled"
fi

# Build port forwarding from vm-config.json
PORTFWD=""
if [ -f "$VM_CONFIG" ]; then
    while IFS= read -r entry; do
        hp=$(printf '%s' "$entry" | jq -r '.host')
        vp=$(printf '%s' "$entry" | jq -r '.vm')
        proto=$(printf '%s' "$entry" | jq -r '.protocol')
        rule="hostfwd=${proto}::${hp}-:${vp}"
        PORTFWD="${PORTFWD:+$PORTFWD,}$rule"
    done < <(jq -c '.ports[]?' "$VM_CONFIG" 2>/dev/null)
fi

echo ""
echo "=========================================="
echo "  Starting Linux VM (QEMU direct boot)"
echo "=========================================="
echo ""
echo "VM Configuration:"
echo "  Kernel: $KERNEL"
echo "  Disk:   $DISK_IMG"
echo "  CPUs:   $CPUS"
echo "  Memory: ${MEMORY_MB}MB"
echo "  SSH:    localhost:$SSH_PORT"
if [ -n "$SHARED_DIR_ABS" ]; then
    echo "  Shared: $SHARED_DIR_ABS -> /mnt/host"
else
    echo "  Shared: None (provide as 3rd argument to enable)"
fi
echo ""

# Start virtiofsd if shared directory provided
VIRTIOFS_ARGS=()
if [ -n "$SHARED_DIR_ABS" ]; then
    VIRTIOFSD_BIN=""
    for p in /usr/libexec/virtiofsd /usr/bin/virtiofsd; do
        [ -x "$p" ] && VIRTIOFSD_BIN="$p" && break
    done
    if [ -z "$VIRTIOFSD_BIN" ]; then
        echo "Error: virtiofsd not found. Install with: sudo apt-get install virtiofsd"
        exit 1
    fi

    rm -f "$VIRTIOFS_SOCKET" "$VIRTIOFSD_PID_FILE"
    "$VIRTIOFSD_BIN" \
        --socket-path="$VIRTIOFS_SOCKET" \
        --shared-dir="$SHARED_DIR_ABS" \
        --sandbox none \
        --cache=auto \
        --thread-pool-size=4 &
    echo $! > "$VIRTIOFSD_PID_FILE"
    sleep 1

    VIRTIOFS_ARGS=(
        -chardev "socket,id=char0,path=$VIRTIOFS_SOCKET"
        -device vhost-user-fs-pci,chardev=char0,tag=hostshare
        -object "memory-backend-file,id=mem,size=${MEMORY_MB}M,mem-path=/dev/shm,share=on"
        -numa node,memdev=mem
    )
fi

> "$LOG_FILE"

qemu-system-aarch64 \
    -machine virt \
    -accel "$QEMU_ACCEL" \
    -cpu "$QEMU_CPU" \
    -m "$MEMORY_MB" \
    -smp "$CPUS" \
    -kernel "$KERNEL" \
    -initrd "$INITRD" \
    -append "$CMDLINE" \
    -drive "file=$DISK_IMG,format=raw,if=virtio" \
    -netdev "user,id=net0${PORTFWD:+,$PORTFWD}" \
    -device virtio-net-pci,netdev=net0 \
    "${VIRTIOFS_ARGS[@]}" \
    -serial "file:$LOG_FILE" \
    -daemonize \
    -pidfile "$PID_FILE" \
    -display none

echo "==> VM started successfully! (PID: $(cat "$PID_FILE"))"
echo ""
echo "Connection Information:"
echo "  SSH:     ssh -i $SSH_KEY -p $SSH_PORT -o StrictHostKeyChecking=no exasol@localhost"
echo "  Console: tail -f $LOG_FILE"
if [ -n "$SHARED_DIR_ABS" ]; then
    echo "  Shared:  $SHARED_DIR_ABS -> /mnt/host (in VM)"
fi
echo ""
echo "  Stop: kill \$(cat $PID_FILE)"
echo ""
echo "Note: Wait 20-30 seconds for the VM to fully boot before connecting"
