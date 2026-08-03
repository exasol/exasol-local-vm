#!/bin/sh
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

# Enable rootful podman so callers can talk to it over the SSH port forwarded
# by the mac-vm launcher. See ../../mac/main.go for the launcher-side
# contract and the consumer preset at
# https://github.com/exasol/exasol-personal/blob/main/assets/infrastructure/mac/preset.py.

set -eu

log_msg() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [PODMAN] $1"
  logger -t init-podman "$1" 2>/dev/null || true
}

if ! command -v systemctl >/dev/null 2>&1; then
  log_msg "systemctl not available; cannot enable podman.socket automatically"
  exit 0
fi

# Rootful socket at /run/podman/podman.sock matches the consumer preset's
# default PODMAN_SOCKET_PATH_DEFAULT. Rootless is intentionally not used —
# the guest is a single-tenant appliance and rootful avoids linger / user
# session complications.
if systemctl enable --now podman.socket >/dev/null 2>&1; then
  log_msg "podman.socket enabled and started"
else
  log_msg "systemctl enable --now podman.socket failed"
  exit 1
fi
