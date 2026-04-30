#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGE_DIR="$SCRIPT_DIR/package"

PID_FILE="$PACKAGE_DIR/qemu.pid"
VIRTIOFSD_PID_FILE="$PACKAGE_DIR/virtiofsd.pid"
VIRTIOFS_SOCKET="$PACKAGE_DIR/virtiofs.sock"

stop_virtiofsd() {
    if [ -f "$VIRTIOFSD_PID_FILE" ]; then
        VIRTIOFSD_PID=$(cat "$VIRTIOFSD_PID_FILE")
        if ps -p "$VIRTIOFSD_PID" > /dev/null 2>&1; then
            echo "==> Stopping virtiofsd (PID: $VIRTIOFSD_PID)..."
            kill "$VIRTIOFSD_PID" 2>/dev/null || true
            sleep 0.5
            if ps -p "$VIRTIOFSD_PID" > /dev/null 2>&1; then
                kill -9 "$VIRTIOFSD_PID" 2>/dev/null || true
            fi
        fi
        rm -f "$VIRTIOFSD_PID_FILE"
    fi
    rm -f "$VIRTIOFS_SOCKET"
}

if [ ! -f "$PID_FILE" ]; then
    echo "==> VM is not running (no PID file found)"
    stop_virtiofsd
    exit 0
fi

PID=$(cat "$PID_FILE")

if ! ps -p "$PID" > /dev/null 2>&1; then
    echo "==> VM process not found (PID: $PID) — removing stale PID file"
    rm -f "$PID_FILE"
    stop_virtiofsd
    exit 0
fi

echo "==> Stopping VM (PID: $PID)..."
kill "$PID"

for _ in $(seq 1 60); do
    ps -p "$PID" > /dev/null 2>&1 || break
    sleep 0.5
done

if ps -p "$PID" > /dev/null 2>&1; then
    echo "==> Force stopping VM..."
    kill -9 "$PID" 2>/dev/null || true
fi

rm -f "$PID_FILE"
echo "==> VM stopped"

stop_virtiofsd
echo "==> Cleanup complete"
