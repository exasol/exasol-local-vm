#!/usr/bin/env bash
set -euo pipefail

PACKAGE="mac-arm64.tar.xz"
PACKAGE_DIR="mac-arm64"
SHARED_DIR="$HOME/shared"

echo "==> Checking for Homebrew..."
if ! command -v brew &>/dev/null; then
    echo "Error: Homebrew is not installed. Install it from https://brew.sh"
    exit 1
fi

echo "==> Installing vfkit..."
brew install vfkit

echo "==> Extracting $PACKAGE..."
if [ ! -f "$HOME/$PACKAGE" ]; then
    echo "Error: $HOME/$PACKAGE not found. Upload it first."
    exit 1
fi
tar -xf "$HOME/$PACKAGE" -C "$HOME"

echo "==> Creating shared folder..."
mkdir -p "$SHARED_DIR"

echo "==> Starting VM..."
cd "$HOME/$PACKAGE_DIR"
./start.sh 2 2048 "$SHARED_DIR"
