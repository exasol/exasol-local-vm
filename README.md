# exasol-nano-vm

This repository builds a minimal Linux VM image to run an Exasol container.
The image is built from `container/Containerfile` and then converted to vm
images using another container buiult from `host/build/Containerfile`.

## Build

Build the VM image for the target architecture:

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
IMG_ARCH=x86_64 task package-mac
IMG_ARCH=x86_64 task package-windows
```

The Linux package uses the Podman-based QEMU runner. The macOS package uses the
raw UEFI disk image with vfkit. The Windows package uses the VHDX artifact for
Hyper-V Generation 2 VMs.

## Reference Runtime Code

The previous branch used cloud-init to seed a booted VM image. That build path
has been removed. Reusable pieces from the old generic shared-container runtime
are kept under `container/guest-services/shared-container/` as reference only;
they are not wired into the current image.
