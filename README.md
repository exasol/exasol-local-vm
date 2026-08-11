# Exasol Local VM

Exasol Local VM builds a small Linux VM appliance and platform launchers for
software that needs a local Linux execution environment. The macOS launcher
owns the VM, not the containers running inside it.

The appliance includes Podman. A consuming runtime such as exasol-personal is
responsible for loading images, creating containers, and managing their
lifecycle by executing Podman commands through the launcher.

## macOS launcher

Initialize and start a VM:

```bash
./launcher init
./launcher start --forward database:8563:0 2 4096 20
```

Each `--forward` value is `<name>:<guest-port>:<host-port>`. Host port `0`
requests an available loopback port. `vm-state.json` reports the effective
named mappings without exposing the guest transport.

Run commands inside the VM:

```bash
./launcher run podman info
./launcher run podman load -i /mnt/host/runtime-artifacts/image.tar
```

Calling `launcher run` without a command from a terminal opens a VM shell. A
consumer that owns a container can use the same interface for a container shell:

```bash
./launcher run --tty podman exec -it <container> sh
```

The launcher streams stdin, stdout, and stderr and returns the guest command's
exit code. SSH is an internal transport and is not part of the consumer
contract.

The host directory reported as `shared_dir` in `vm-state.json` is mounted at
`/mnt/host` in the VM. Files passed to guest commands must be staged below that
directory and translated to the corresponding `/mnt/host/...` path.

Inspect and stop the VM with:

```bash
./launcher status
./launcher health-check
./launcher stop
```

## Building

Build the complete ARM64 appliance from source before packaging the launcher:

```bash
task build IMG_ARCH=aarch64
task package-mac IMG_ARCH=aarch64
task build-mac-launcher IMG_ARCH=aarch64
```

The macOS release binary must be signed and notarized. See
[the release workflow](docs/release-workflow.md).

## Design

See [the architecture](docs/architecture.md) and
[the requirements](docs/requirements.md).
