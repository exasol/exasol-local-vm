#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

# Build the windows-launcher for windows/amd64 (podman-for-windows
# is x86_64-only in practice, so we only cross-compile that target).
#
# Unlike host/package/build-mac-launcher.sh this script does NOT embed a
# VM disk image: the windows launcher delegates virtualization to the
# user's natively installed podman-for-windows. The only embedded blob is
# launcher/assets/windows/init/ (packaged as init-assets.tar.xz), which
# contains the DB container tarball and its config.json.

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (currently only x86_64 is supported)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

case "$IMG_ARCH" in
    x86_64) GOARCH=amd64 ;;
    *) echo "Error: unsupported architecture '$IMG_ARCH' for windows launcher (only x86_64 is currently supported)" >&2; exit 1 ;;
esac

WINDOWS_ASSETS_DIR="$ROOT_DIR/launcher/assets/windows/init"
if [ ! -d "$WINDOWS_ASSETS_DIR" ]; then
    echo "Error: windows init assets directory not found: $WINDOWS_ASSETS_DIR" >&2
    echo "Run 'task stage-windows-init-assets IMG_ARCH=$IMG_ARCH' first." >&2
    exit 1
fi
if [ ! -f "$WINDOWS_ASSETS_DIR/exasol-nano-db.tar.gz" ]; then
    echo "Error: staged container tarball missing: $WINDOWS_ASSETS_DIR/exasol-nano-db.tar.gz" >&2
    echo "Run 'task stage-windows-init-assets IMG_ARCH=$IMG_ARCH' first." >&2
    exit 1
fi

echo "==> Building windows launcher for $IMG_ARCH (GOARCH=$GOARCH)..."

LAUNCHER_DIR="$ROOT_DIR/launcher/windows"
pushd "$LAUNCHER_DIR" > /dev/null

# Compress launcher/assets/windows/init/ into launcher/windows/init-assets.tar.xz
# so a future //go:embed block in main.go can embed it. Phase 3 produces the
# tarball unconditionally so that Phase 4 only needs to add the embed line.
echo "==> Creating init assets tarball..."
tar -C "$ROOT_DIR/launcher/assets/windows" -cf - init | xz -9 --extreme > init-assets.tar.xz
INIT_ASSETS_SIZE=$(du -h init-assets.tar.xz | cut -f1)
echo "    init-assets.tar.xz: $INIT_ASSETS_SIZE"

# Ensure the temporary tarball is removed even if the build fails, so
# a failed local build does not leave a stale artifact in the tree.
cleanup() {
    rm -f "$LAUNCHER_DIR/init-assets.tar.xz"
}
trap cleanup EXIT

# Update Go module dependencies and go.sum. On windows this is a no-op
# today (Phase 2 skeleton has no deps) but keeps parity with the mac
# script for when Phase 4 introduces github.com/ulikunitz/xz.
echo "==> Updating Go dependencies..."
go mod tidy
go mod download

# Build the launcher binary. CGO is disabled because — unlike mac's
# vz/v3 binding — nothing on windows needs to link against a native
# framework. -trimpath strips local paths; -ldflags="-s -w" drops the
# symbol table and DWARF debug data.
LAUNCHER_OUTPUT_DIR="$ROOT_DIR/release/launcher/windows/$IMG_ARCH"
mkdir -p "$LAUNCHER_OUTPUT_DIR"
LAUNCHER_OUTPUT="$LAUNCHER_OUTPUT_DIR/launcher.exe"
GOOS=windows GOARCH="$GOARCH" CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" -o "$LAUNCHER_OUTPUT" .

popd > /dev/null

echo "==> Launcher binary: $LAUNCHER_OUTPUT ($(du -h "$LAUNCHER_OUTPUT" | cut -f1))"

# Code signing is intentionally out of scope for this script: the
# windows launcher is signed in CI via the SSLcom/esigner-codesign
# GitHub Action, which authenticates against SSL.com's eSigner cloud
# HSM using the ESIGN_* repository secrets. Local builds produce an
# unsigned binary; see windows-launcher-plan.md § "Windows code signing"
# for the design rationale.
