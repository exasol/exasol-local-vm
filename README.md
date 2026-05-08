# exasol-nano-vm

This repository builds a minimal Linux VM image to run an Exasol container.

The VM image is buiult using `task build` in two distinct containerized steps:

1. Build the guest filesystem image from `container/Containerfile`.
2. Build and run a separate VM image builder container from
   `host/build/Containerfile` that repackages the guest image into exported VM
   artifacts.

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

## Runtime Shape

The VM boots a unified kernel image from the EFI System Partition. The root
filesystem comes from the initramfs and switches to a tmpfs copy early in boot.
The `/var` tree is stored on the `exasol-data` ext4 partition.

Current runtime behavior is intentionally minimal:

- Podman is installed in the guest.
- OpenRC starts base services, networking and initial setup.
- The guest autologins as `exasol` on configured consoles.

On Linux, the default launcher is a Podman-based QEMU runner. Host-side QEMU,
UEFI firmware, and virtiofsd dependencies are intentionally isolated inside
`host/run/Containerfile`.

The following behavior is implemented in the current image:

- Optional `/mnt/host` mounting through virtiofs on the Linux QEMU runner and
  on vfkit.
- Boot-time growth of the `/var` partition and filesystem via `growpart` and
  `resize2fs`.
- SSH key import for the `exasol` user from `/mnt/host/authorized_keys`.
