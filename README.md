# Exasol Local VM

Exasol Local VM is a generic macOS Apple-Silicon VM provider. It boots a small
Linux guest with Podman and SSH, mounts caller-owned directories, creates
configured TCP forwarders, and runs an optional caller hook.

It does not select, package, start, inspect, or delete application workloads.
Callers own workload images, manifests, readiness, persistent data, and lifecycle
policy.

## Provider contract

```text
local-vm init         --state-dir <path> --config <path>
local-vm start        --state-dir <path> --config <path>
local-vm stop         --state-dir <path>
local-vm status       --state-dir <path> --json
local-vm health-check --state-dir <path> --json
local-vm destroy      --state-dir <path>
local-vm version      --json
```

Configuration, hook, and state schemas are independently versioned. See
[requirements](docs/requirements.md) for the contract and
[architecture](docs/architecture.md) for ownership boundaries.

`destroy` removes provider-owned files below `state-dir`, including the fixed
sparse disk used for the guest's writable `/var`. Configured shares remain
caller-owned and survive provider destruction.

## Development

```bash
task install-deps
task build IMG_ARCH=aarch64
task package-mac IMG_ARCH=aarch64
```

The distributable provider must be built, signed, and notarized on macOS:

```bash
task build-mac-provider IMG_ARCH=aarch64
```
