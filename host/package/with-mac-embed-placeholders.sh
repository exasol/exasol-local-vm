#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LAUNCHER_DIR="$ROOT_DIR/launcher/mac"
CREATED_PLACEHOLDERS=()

cleanup() {
    local placeholder
    for placeholder in "${CREATED_PLACEHOLDERS[@]}"; do
        rm -f "$placeholder"
    done
}
trap cleanup EXIT

for placeholder in vm-package.tar.xz init-assets.tar.xz; do
    placeholder="$LAUNCHER_DIR/$placeholder"
    if [[ ! -e "$placeholder" ]]; then
        : > "$placeholder"
        CREATED_PLACEHOLDERS+=("$placeholder")
    fi
done

cd "$LAUNCHER_DIR"
"$@"
