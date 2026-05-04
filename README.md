# exasol-nano-vm

This repository builds a minimal Linux VM image with Exasol Nano preloaded as a
Podman image. The image is assembled by Podman builder stages: Alpine provides
the guest root filesystem and kernel, Fedora builds the unified kernel image and
disk images with `ukify` and `systemd-repart`.

## Build

Place the matching Exasol Nano `.run` file in `nano/`, then build the VM image:

```bash
IMG_ARCH=x86_64 task build
```

Supported `IMG_ARCH` values are:

- `x86_64`
- `aarch64`

The build exports artifacts under `output/`:

- `vmlinuz-virt`
- `initramfs.img.zst`
- `disk.img`
- `disk.qcow2`
- `disk.vhdx`
- `kernel-cmdline.txt`

## Local Linux Run

Start the built image with QEMU:

```bash
task start-vm
```

Keep the console attached:

```bash
task start-vm-attached
```

## Packaging

Create distributable bundles for each runtime:

```bash
IMG_ARCH=x86_64 task package-linux
IMG_ARCH=x86_64 task package-mac
IMG_ARCH=x86_64 task package-windows
```

The Linux and macOS packages use the raw UEFI disk image. The Windows package
uses the VHDX artifact for Hyper-V Generation 2 VMs.

## Reference Runtime Code

The previous branch used cloud-init to seed a booted VM image. That build path
has been removed. Reusable pieces from the old generic shared-container runtime
are kept under `container/guest-services/shared-container/` as reference only;
they are not wired into the current image.
