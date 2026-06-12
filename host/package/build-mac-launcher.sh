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

VM_ARTIFACTS_TARBALL="${RELEASE_FILE:-$ROOT_DIR/release/$PACKAGE_NAME.tar.xz}"

if [ ! -f "$VM_ARTIFACTS_TARBALL" ]; then
    echo "Error: Release file not found: $VM_ARTIFACTS_TARBALL" >&2
    echo "Run 'task package-mac IMG_ARCH=$IMG_ARCH' first to create the archive." >&2
    exit 1
fi

echo "==> Building macOS launcher for $ARCH..."
echo "    Release archive: $VM_ARTIFACTS_TARBALL"

LAUNCHER_DIR="$ROOT_DIR/launcher/mac"
pushd "$LAUNCHER_DIR" > /dev/null

# Copy the release archive to be embedded
cp "$VM_ARTIFACTS_TARBALL" vm-package.tar.xz

# Compress launcher/assets/init directory to be embedded
echo "==> Creating init assets tarball..."
tar -C "$ROOT_DIR/launcher/assets" -cf - init | xz -9 --extreme > init-assets.tar.xz

# Update Go module dependencies and go.sum
echo "Updating Go dependencies..."
go mod tidy
go mod download

# Build the launcher binary
# Use directory structure: release/launcher/{os}/{arch}/launcher
# Note: CGO is required (vz/v3 binds Apple's Virtualization.framework), so CGO_ENABLED=0 is not an option.
# -trimpath strips local paths; -ldflags="-s -w" drops the symbol table and DWARF debug data.
LAUNCHER_OUTPUT_DIR="$ROOT_DIR/release/launcher/darwin/$ARCH"
mkdir -p "$LAUNCHER_OUTPUT_DIR"
LAUNCHER_OUTPUT="$LAUNCHER_OUTPUT_DIR/launcher"
GOOS=darwin GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w" -o "$LAUNCHER_OUTPUT" .

# Clean up generated files
rm -f vm-package.tar.xz
rm -f init-assets.tar.xz

popd > /dev/null

chmod +x "$LAUNCHER_OUTPUT"

echo "==> Launcher binary: $LAUNCHER_OUTPUT"

# Sign the launcher (required)
if [ -z "${MACOS_SIGN_KEYCHAIN:-}" ] || [ -z "${MACOS_SIGN_IDENTITY:-}" ]; then
  echo "Error: Code signing is required but credentials are not set" >&2
  echo "Please set MACOS_SIGN_KEYCHAIN and MACOS_SIGN_IDENTITY environment variables" >&2
  exit 1
fi

echo "==> Signing macOS launcher with virtualization entitlement..."

codesign \
  --force \
  --timestamp \
  --options runtime \
  --keychain "${MACOS_SIGN_KEYCHAIN}" \
  --entitlements "$ROOT_DIR/launcher/mac/entitlements.plist" \
  --sign "${MACOS_SIGN_IDENTITY}" \
  "${LAUNCHER_OUTPUT}"

echo "==> Verifying virtualization entitlement..."
codesign -d --entitlements :- "${LAUNCHER_OUTPUT}" 2>&1 | tee /tmp/launcher.entitlements
if grep -q '<key>com.apple.security.virtualization</key>' /tmp/launcher.entitlements; then
  echo "✓ Virtualization entitlement verified"
else
  echo "✗ Virtualization entitlement missing!" >&2
  exit 1
fi

echo "==> Signed launcher binary: $LAUNCHER_OUTPUT"
