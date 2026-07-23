# Exasol Local Runtime

Exasol Local Runtime provides a local Exasol database instance through a
platform-specific runtime artifact.

The artifact is the product interface. It prepares local state, starts the local
database, reports connection information, and stops the database again. Platform
backends such as VMs, WSL, native containers, or platform virtualization APIs are
implementation details hidden behind that interface.

## Current status

- The primary release artifact today is the macOS runtime binary published in a
  zip archive.
- The macOS artifact embeds the Exasol nano DB payload and the runtime assets it
  needs.
- Windows and Linux artifacts are target platforms and should expose the same
  user-facing behavior as far as reasonably possible.

## Using the runtime

After unpacking a runtime archive, use the runtime binary to:

1. initialize local state
2. start the local Exasol DB with CPU, memory, and storage settings
3. read the reported localhost connection information
4. stop the local DB when done

For the current macOS artifact, the flow is:

```bash
./launcher init
./launcher start 2 2048 10
./launcher stop
```

By default, initialization generates an SSH key pair for VM administration. To
use an existing private key instead, pass it during initialization:

```bash
./launcher init --ssh-key ~/.ssh/id_ed25519
```

The preferred DB endpoint is `127.0.0.1:8563`. If that port is unavailable, the
runtime reports the actual localhost endpoint to use.

## Developing

Common entry points:

- `launcher/` — platform runtime binaries, with `launcher/mac/` as the current
  primary release path
- `launcher/assets/` — initialization assets embedded into runtime artifacts
- `host/build/` — build pipeline for runtime backend assets
- `host/run/` — Linux/QEMU development launcher
- `container/` — Linux guest environment used by the current macOS backend
- `docs/requirements.md` — product requirements
- `docs/architecture.md` — current architecture overview

Useful tasks:

```bash
task install-deps
task build IMG_ARCH=aarch64
task start-vm IMG_ARCH=aarch64
task stop-vm
```

The macOS release binary must be built, signed, and notarized on macOS:

```bash
task build-mac-launcher IMG_ARCH=aarch64
```

See `docs/release-workflow.md` for release workflow details.
