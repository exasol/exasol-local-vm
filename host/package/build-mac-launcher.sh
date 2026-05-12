#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

ARCH="$IMG_ARCH"
case "$ARCH" in
    x86_64) PACKAGE_NAME="mac-x86_64"; GOARCH=amd64 ;;
    aarch64) PACKAGE_NAME="mac-arm64"; GOARCH=arm64 ;;
    *) echo "Error: unknown architecture: $ARCH" >&2; exit 1 ;;
esac

RELEASE_FILE="${RELEASE_FILE:-$ROOT_DIR/release/$PACKAGE_NAME.tar.xz}"

if [ ! -f "$RELEASE_FILE" ]; then
    echo "Error: Release file not found: $RELEASE_FILE" >&2
    echo "Run 'task package-mac IMG_ARCH=$IMG_ARCH' first to create the archive." >&2
    exit 1
fi

echo "==> Building macOS launcher for $ARCH..."
echo "    Release archive: $RELEASE_FILE"

LAUNCHER_DIR="$ROOT_DIR/launcher/mac"
pushd "$LAUNCHER_DIR" > /dev/null

# Copy the release archive to be embedded
cp "$RELEASE_FILE" vm-package.tar.xz

# Update Go module dependencies and go.sum
echo "Updating Go dependencies..."
go mod tidy
go mod download

# Build the launcher binary
LAUNCHER_OUTPUT="$ROOT_DIR/release/mac-runner-$ARCH"
GOOS=darwin GOARCH="$GOARCH" go build -o "$LAUNCHER_OUTPUT" .

# Clean up generated file
rm -f vm-package.tar.xz

popd > /dev/null

chmod +x "$LAUNCHER_OUTPUT"

echo "==> Launcher binary: $LAUNCHER_OUTPUT"
