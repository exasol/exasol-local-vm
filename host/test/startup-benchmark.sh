#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

TEST_DIR="$(mktemp -d)"
export VM_CONTAINER_NAME="exasol-nano-test-vm-$$"
# Set this to something that doesn't exist to make sure we can also start
# without a shared dir.  Other tests exercise the shared directory code path.
export VM_SHARED_DIR="${TEST_DIR}/non-existent"

# Ensure VM is stopped on exit (success or failure)
trap './host/run/stop-qemu-container.sh 2>/dev/null || true' EXIT

echo "==> Starting VM startup benchmark..."
echo ""

# Record start time
START_TIME=$(date +%s%3N)  # milliseconds

# Start the VM
./host/run/start-qemu-container.sh "${IMG_ARCH}"

echo ""
echo "==> Waiting for SSH connection..."

# Wait for SSH to be available
MAX_WAIT=180  # 3 minutes for slow emulation
BOOT_TIME=0
while [ $BOOT_TIME -lt $MAX_WAIT ] && podman container exists "${VM_CONTAINER_NAME}"; do
    NOW_TIME=$(date +%s%3N)  # milliseconds
    BOOT_TIME=$(( (NOW_TIME - START_TIME) / 1000 ))  # Convert to seconds
    BOOT_TIME_MS=$(( (NOW_TIME - START_TIME) % 1000 ))  # Remainder in ms
    if podman logs "${VM_CONTAINER_NAME}" |& grep -q 'Starting sshd ... \[ ok \]' ; then
        echo ""
        echo "========================================="
        echo "  VM Startup Benchmark Complete"
        echo "========================================="
        echo "  Time to init done: ${BOOT_TIME}.${BOOT_TIME_MS} seconds"
        echo "========================================="
        echo ""
        exit 0
    fi
    sleep 1
    if [ $((BOOT_TIME % 10)) -eq 0 ]; then
        printf "."
    fi
done

echo ""
echo "==> Error: Timeout waiting for init to finish after ${MAX_WAIT} seconds"
exit 1
