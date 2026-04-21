# exasol-local-vm

This repository builds the platform-native launchers for the Exasol container.

## Artifacts

The primary release artifacts are the platform launchers found under
`launcher/`. Each launcher is a self-contained Go binary that embeds all runtime assets needed to start the Exasol VM on a given
platform.

| Launcher | Platform | Hypervisor |
|---|---|---|
| `launcher/mac/` | macOS (ARM64 / x86_64) | Apple Virtualization.framework |

The launchers expose a simple CLI (`init`, `start`, `stop`) for managing the VM
lifecycle.

## Repository Structure

- `container/` — Guest OS definition (Alpine Linux, OpenRC services, Podman).
- `host/build/` — Containerized VM image build pipeline; produces kernel,
  initramfs, and disk images under `output/`.
- `host/run/` — Podman-based QEMU runner used for local Linux development.
- `launcher/` — Platform launchers (the primary release artifacts).
- `launcher/assets/` — Shared init scripts and assets embedded in each launcher.
- `package/` — Artifacts for bundling into the launchers.
