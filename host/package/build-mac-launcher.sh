#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

ARCH="$IMG_ARCH"
if [ "$ARCH" != "aarch64" ]; then
    echo "Error: macOS provider builds only support aarch64, got: $ARCH" >&2
    exit 1
fi
PACKAGE_NAME="mac-arm64"
GOARCH="arm64"

VM_ARTIFACTS_TARBALL="${RELEASE_FILE:-$ROOT_DIR/release/$PACKAGE_NAME.tar.xz}"

if [ ! -f "$VM_ARTIFACTS_TARBALL" ]; then
    echo "Error: Release file not found: $VM_ARTIFACTS_TARBALL" >&2
    echo "Run 'task package-mac IMG_ARCH=$IMG_ARCH' first to create the archive." >&2
    exit 1
fi

echo "==> Building macOS provider for $ARCH..."
echo "    Release archive: $VM_ARTIFACTS_TARBALL"

PROVIDER_SOURCE_DIR="$ROOT_DIR/launcher/mac"
pushd "$PROVIDER_SOURCE_DIR" > /dev/null

# Copy the release archive to be embedded
cp "$VM_ARTIFACTS_TARBALL" vm-package.tar.xz

# Compress the provider initialization assets to be embedded.
echo "==> Creating init assets tarball..."
tar -C "$ROOT_DIR/launcher/assets" -cf - init | xz -9 --extreme > init-assets.tar.xz

# Update Go module dependencies and go.sum
echo "Updating Go dependencies..."
go mod tidy
go mod download

# Build the provider binary.
# Note: CGO is required (vz/v3 binds Apple's Virtualization.framework), so CGO_ENABLED=0 is not an option.
# -trimpath strips local paths; -ldflags="-s -w" drops the symbol table and DWARF debug data.
PROVIDER_VERSION="${PROVIDER_VERSION:-$(git -C "$ROOT_DIR" describe --tags --always --dirty)}"
PROVIDER_OUTPUT_DIR="$ROOT_DIR/release/local-vm/darwin/$ARCH"
mkdir -p "$PROVIDER_OUTPUT_DIR"
PROVIDER_OUTPUT="$PROVIDER_OUTPUT_DIR/local-vm"
GOOS=darwin GOARCH="$GOARCH" go build -trimpath -ldflags="-s -w -X main.providerVersion=$PROVIDER_VERSION" -o "$PROVIDER_OUTPUT" .

# Clean up generated files
rm -f vm-package.tar.xz
rm -f init-assets.tar.xz

popd > /dev/null

chmod +x "$PROVIDER_OUTPUT"

echo "==> Provider binary: $PROVIDER_OUTPUT"

# Release builds are signed. A manually built artifact used through Personal's
# LOCAL_VM_BINARY development input may explicitly opt into an unsigned build.
if [ -z "${MACOS_SIGN_KEYCHAIN:-}" ] || [ -z "${MACOS_SIGN_IDENTITY:-}" ]; then
  if [ "${ALLOW_UNSIGNED_LOCAL_VM:-}" = "1" ]; then
    echo "==> Leaving development local-vm artifact unsigned"
    echo "==> Provider binary: $PROVIDER_OUTPUT"
    exit 0
  fi
  echo "Error: Code signing is required; set signing credentials or use ALLOW_UNSIGNED_LOCAL_VM=1 for a development-only artifact" >&2
  exit 1
fi

echo "==> Signing macOS provider with virtualization entitlement..."

codesign \
  --force \
  --timestamp \
  --options runtime \
  --keychain "${MACOS_SIGN_KEYCHAIN}" \
  --entitlements "$ROOT_DIR/launcher/mac/entitlements.plist" \
  --sign "${MACOS_SIGN_IDENTITY}" \
  "${PROVIDER_OUTPUT}"

echo "==> Verifying virtualization entitlement..."
codesign -d --entitlements :- "${PROVIDER_OUTPUT}" 2>&1 | tee /tmp/provider.entitlements
if grep -q '<key>com.apple.security.virtualization</key>' /tmp/provider.entitlements; then
  echo "✓ Virtualization entitlement verified"
else
  echo "✗ Virtualization entitlement missing!" >&2
  exit 1
fi

echo "==> Signed provider binary: $PROVIDER_OUTPUT"
