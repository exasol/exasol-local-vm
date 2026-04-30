#!/usr/bin/env bash
set -euo pipefail

# Usage: ./start-vfkit.sh [cpu_count] [memory_mb] [shared_directory]
# All arguments optional. Defaults: 2 CPUs, 2048 MB RAM, no folder sharing.
# Port forwards are read from split-image/package/vm-config.json via gvproxy.
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
GVPROXY="$PROJECT_ROOT/package/mac-arm64/gvproxy"
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

if ! command -v vfkit &>/dev/null; then
    echo "Error: vfkit is not installed. Install with: brew install vfkit"
    exit 1
fi
if [ ! -x "$GVPROXY" ]; then
    echo "Error: gvproxy not found at $GVPROXY"
    exit 1
fi
if ! command -v jq &>/dev/null; then
    echo "Error: jq is required. Install with: brew install jq"
    exit 1
fi

CMDLINE=$(cat "$CMDLINE_FILE")
DISK_IMG_ABS="$(cd "$(dirname "$DISK_IMG")" && pwd)/$(basename "$DISK_IMG")"

SHARED_DIR_ABS=""
if [ -n "$SHARED_DIR" ]; then
    if [ ! -d "$SHARED_DIR" ]; then
        echo "Error: Shared directory not found: $SHARED_DIR"
        exit 1
    fi
    SHARED_DIR_ABS="$(cd "$SHARED_DIR" && pwd)"
fi

if pgrep -f "vfkit.*rootfs.img" >/dev/null; then
    echo "Error: VM appears to already be running"
    exit 1
fi

echo ""
echo "=========================================="
echo "  Starting Linux VM (vfkit direct boot)"
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

GVPROXY_VFKIT_SOCK="$PACKAGE_DIR/gvproxy-vfkit.sock"
GVPROXY_API_SOCK="$PACKAGE_DIR/gvproxy-api.sock"

MAC_FILE="$PACKAGE_DIR/vm-mac.txt"
if [ ! -f "$MAC_FILE" ]; then
    printf '52:54:00:%02x:%02x:%02x\n' \
        $((RANDOM % 256)) $((RANDOM % 256)) $((RANDOM % 256)) > "$MAC_FILE"
fi
VM_MAC=$(cat "$MAC_FILE")

VFKIT_ARGS=(
    --cpus "$CPUS"
    --memory "$MEMORY_MB"
    --bootloader "linux,kernel=$KERNEL,initrd=$INITRD,cmdline=$CMDLINE"
    --device "virtio-blk,path=$DISK_IMG_ABS"
    --device "virtio-net,unixSocketPath=$GVPROXY_VFKIT_SOCK,mac=$VM_MAC"
    --device virtio-rng
    --device "virtio-serial,logFilePath=$PACKAGE_DIR/vm-console.log"
)

if [ -n "$SHARED_DIR_ABS" ]; then
    VFKIT_ARGS+=(--device "virtio-fs,sharedDir=$SHARED_DIR_ABS,mountTag=hostshare")
fi

rm -f "$GVPROXY_VFKIT_SOCK" "$GVPROXY_API_SOCK" "$PACKAGE_DIR/gvproxy.pid"
"$GVPROXY" \
    --mtu 1500 \
    --listen "unix://$GVPROXY_API_SOCK" \
    --listen-vfkit "unixgram://$GVPROXY_VFKIT_SOCK" \
    --log-file "$PACKAGE_DIR/gvproxy.log" \
    --pid-file "$PACKAGE_DIR/gvproxy.pid" &

for _ in $(seq 1 30); do
    [ -S "$GVPROXY_VFKIT_SOCK" ] && [ -S "$GVPROXY_API_SOCK" ] && break
    sleep 0.2
done
if [ ! -S "$GVPROXY_VFKIT_SOCK" ] || [ ! -S "$GVPROXY_API_SOCK" ]; then
    echo "Error: gvproxy failed to start. Log:"
    cat "$PACKAGE_DIR/gvproxy.log" 2>/dev/null || true
    exit 1
fi

vfkit "${VFKIT_ARGS[@]}" > "$PACKAGE_DIR/vfkit.log" 2>&1 &
VFKIT_PID=$!
echo "$VFKIT_PID" > "$PACKAGE_DIR/vfkit.pid"

sleep 2
if ! kill -0 "$VFKIT_PID" 2>/dev/null; then
    echo "Error: vfkit failed to start"
    cat "$PACKAGE_DIR/vfkit.log"
    exit 1
fi

GUEST_IP=""
for _ in $(seq 1 30); do
    GUEST_IP=$(curl -s --unix-socket "$GVPROXY_API_SOCK" \
        http:/unix/services/dhcp/leases 2>/dev/null \
        | jq -r --arg mac "$VM_MAC" 'to_entries[] | select(.value == $mac) | .key' \
        | head -1)
    [ -n "$GUEST_IP" ] && break
    sleep 0.5
done
if [ -z "$GUEST_IP" ]; then
    echo "Error: guest did not appear in gvproxy DHCP leases (MAC $VM_MAC)"
    exit 1
fi

jq -c '.ports[]? | select(.protocol == "tcp")' "$VM_CONFIG" 2>/dev/null | while read -r entry; do
    hp=$(printf '%s' "$entry" | jq -r '.host')
    vp=$(printf '%s' "$entry" | jq -r '.vm')
    curl -fsS --unix-socket "$GVPROXY_API_SOCK" \
        http:/unix/services/forwarder/expose \
        -X POST -H 'Content-Type: application/json' \
        -d "{\"local\":\":$hp\",\"remote\":\"$GUEST_IP:$vp\",\"protocol\":\"tcp\"}" \
        > /dev/null
    echo "==> Port forward: localhost:$hp -> $GUEST_IP:$vp"
done

echo "==> VM started successfully! (vfkit PID: $VFKIT_PID)"
echo ""
echo "Connection Information:"
echo "  SSH:     ssh -i $SSH_KEY -p $SSH_PORT -o StrictHostKeyChecking=no exasol@localhost"
echo "  Console: tail -f $PACKAGE_DIR/vm-console.log"
echo "  vfkit:   tail -f $PACKAGE_DIR/vfkit.log"
if [ -n "$SHARED_DIR_ABS" ]; then
    echo "  Shared:  $SHARED_DIR_ABS -> /mnt/host (in VM)"
fi
echo ""
echo "  Stop: kill $VFKIT_PID  (or: killall vfkit)"
echo ""
echo "Note: Wait 20-30 seconds for the VM to fully boot before connecting"
