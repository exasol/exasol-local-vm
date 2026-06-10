#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DISK_IMG="$SCRIPT_DIR/exasol-vm.img"
KERNEL_FILE="$SCRIPT_DIR/vmlinuz-virt"
INITRAMFS_FILE="$SCRIPT_DIR/initramfs.img"
KERNEL_CMDLINE_FILE="$SCRIPT_DIR/kernel-cmdline.txt"
VM_CONFIG="$SCRIPT_DIR/vm-config.json"
GVPROXY_BIN="$SCRIPT_DIR/gvproxy"
VFKIT_PID_FILE="$SCRIPT_DIR/vfkit.pid"
GVPROXY_PID_FILE="$SCRIPT_DIR/gvproxy.pid"
VFKIT_SOCK="$SCRIPT_DIR/vfkit.sock"
GVPROXY_API_SOCK="$SCRIPT_DIR/gvproxy.sock"
VFKIT_LOG="$SCRIPT_DIR/vfkit.log"
CONSOLE_LOG="$SCRIPT_DIR/vm-console.log"
VM_MAC="5a:94:ef:e4:0c:ee"

VM_CPUS="${1:-}"
VM_MEMORY="${2:-}"
SHARED_DIR="${3:-}"

if [ ! -f "$DISK_IMG" ]; then
    echo "Error: disk image not found: $DISK_IMG"
    exit 1
fi

if [ ! -f "$KERNEL_FILE" ]; then
    echo "Error: kernel not found: $KERNEL_FILE"
    exit 1
fi

if [ ! -f "$INITRAMFS_FILE" ]; then
    echo "Error: initramfs not found: $INITRAMFS_FILE"
    exit 1
fi

if [ ! -f "$KERNEL_CMDLINE_FILE" ]; then
    echo "Error: kernel cmdline not found: $KERNEL_CMDLINE_FILE"
    exit 1
fi

if [ ! -f "$VM_CONFIG" ]; then
    echo "Error: vm-config.json not found: $VM_CONFIG"
    exit 1
fi

if [ ! -x "$GVPROXY_BIN" ]; then
    echo "Error: gvproxy not found: $GVPROXY_BIN"
    exit 1
fi

if ! command -v vfkit >/dev/null 2>&1; then
    echo "Error: vfkit is not installed"
    exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "Error: jq is required to read vm-config.json"
    exit 1
fi

VM_CPUS="${VM_CPUS:-$(jq -r '.cpus // 2' "$VM_CONFIG")}"
VM_MEMORY="${VM_MEMORY:-$(jq -r '.memoryMB // 2048' "$VM_CONFIG")}"

if [ -f "$VFKIT_PID_FILE" ] && kill -0 "$(cat "$VFKIT_PID_FILE")" 2>/dev/null; then
    echo "Error: vfkit is already running (PID: $(cat "$VFKIT_PID_FILE"))"
    exit 1
fi

if [ -f "$GVPROXY_PID_FILE" ] && kill -0 "$(cat "$GVPROXY_PID_FILE")" 2>/dev/null; then
    echo "Error: gvproxy is already running (PID: $(cat "$GVPROXY_PID_FILE"))"
    exit 1
fi

rm -f "$VFKIT_PID_FILE" "$GVPROXY_PID_FILE" "$VFKIT_SOCK" "$GVPROXY_API_SOCK"

GVPROXY_LISTEN="unix://$GVPROXY_API_SOCK"
VFKIT_LISTEN="unixgram://$VFKIT_SOCK"

"$GVPROXY_BIN" \
    --mtu 1500 \
    --ssh-port -1 \
    --listen "$GVPROXY_LISTEN" \
    --listen-vfkit "$VFKIT_LISTEN" \
    --log-file "$SCRIPT_DIR/gvproxy.log" \
    --pid-file "$GVPROXY_PID_FILE" &

for _ in {1..20}; do
    if [ -S "$VFKIT_SOCK" ] && [ -S "$GVPROXY_API_SOCK" ]; then
        break
    fi
    sleep 0.25
done

VFKIT_ARGS=(
    --cpus "$VM_CPUS"
    --memory "$VM_MEMORY"
    --bootloader "linux,kernel=$KERNEL_FILE,initrd=$INITRAMFS_FILE,cmdline=$(cat "$KERNEL_CMDLINE_FILE")"
    --device "virtio-blk,path=$DISK_IMG"
    --device "virtio-net,unixSocketPath=$VFKIT_SOCK,mac=$VM_MAC"
    --device virtio-rng
    --device "virtio-serial,logFilePath=$CONSOLE_LOG"
)

if [ -n "$SHARED_DIR" ]; then
    if [ ! -d "$SHARED_DIR" ]; then
        echo "Error: shared directory not found: $SHARED_DIR"
        exit 1
    fi
    SHARED_DIR_ABS="$(cd "$SHARED_DIR" && pwd)"
    VFKIT_ARGS+=(--device "virtio-fs,sharedDir=$SHARED_DIR_ABS,mountTag=hostshare")
fi

vfkit "${VFKIT_ARGS[@]}" >"$VFKIT_LOG" 2>&1 &
VFKIT_PID=$!
echo "$VFKIT_PID" > "$VFKIT_PID_FILE"

sleep 2
if ! kill -0 "$VFKIT_PID" 2>/dev/null; then
    echo "Error: vfkit failed to start"
    cat "$VFKIT_LOG"
    exit 1
fi

PORT_COUNT="$(jq -r '.ports | length // 0' "$VM_CONFIG" 2>/dev/null || echo 0)"
for ((i=0; i<PORT_COUNT; i++)); do
    PROTOCOL="$(jq -r ".ports[$i].protocol" "$VM_CONFIG")"
    HOST_PORT="$(jq -r ".ports[$i].host" "$VM_CONFIG")"
    VM_PORT="$(jq -r ".ports[$i].vm" "$VM_CONFIG")"
    if [ "$PROTOCOL" != "tcp" ]; then
        echo "Warning: gvproxy forwarding for protocol '$PROTOCOL' is not configured automatically"
        continue
    fi
    curl --silent --show-error \
        --unix-socket "$GVPROXY_API_SOCK" \
        http:/unix/services/forwarder/expose \
        -X POST \
        -d "{\"local\":\":${HOST_PORT}\",\"remote\":\"192.168.127.2:${VM_PORT}\"}" >/dev/null
    echo "==> Port forwarding: localhost:${HOST_PORT} -> VM:${VM_PORT} (${PROTOCOL})"
done

echo "==> vfkit started successfully (PID: $VFKIT_PID)"
echo "==> gvproxy started successfully (PID: $(cat "$GVPROXY_PID_FILE"))"
echo "==> Console log: $CONSOLE_LOG"
