# exasol-nano-vm

This repository builds a minimal Linux VM image to run an Exasol container.
`task build` first builds the guest filesystem image from
`container/Containerfile`. It then builds and runs a separate VM image builder
container from `host/build/Containerfile` that repackages that guest image into
VM artifacts.

## Build

Build the VM image for the target architecture:

```bash
IMG_ARCH=x86_64 task build
```

Supported `IMG_ARCH` values are:

- `x86_64`
- `aarch64`

The build exports artifacts under `output/`:

- `arch.txt`
- `vmlinuz-virt`
- `initramfs.img.zst`
- `disk_thin.img`
- `disk.img`
- `disk.vhdx`
- `kernel-cmdline.txt`

## Local Linux Run

Start the image with the Podman-based QEMU runner:

```bash
task start-vm
```

If the VM artifacts or runner image are missing, the task builds them first.
The host only needs Podman and jq for the Linux run path; QEMU, firmware, and
virtiofsd are installed inside the runner image.

Keep the console attached:

```bash
task start-vm-attached
```

## Packaging

Create distributable bundles for each runtime:

```bash
IMG_ARCH=x86_64 task package-linux
IMG_ARCH=aarch64 task package-linux
IMG_ARCH=aarch64 task package-mac
IMG_ARCH=x86_64 task package-windows
```

The Linux package uses the Podman-based QEMU runner. The macOS package uses the
raw UEFI disk image with vfkit. The Windows package uses the VHDX artifact for
Hyper-V Generation 2 VMs.
