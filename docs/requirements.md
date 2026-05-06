# VM Image Requirements

## Current Build Path

The repository MUST build the VM image through Podman builder stages. It MUST
NOT use cloud-init, a NoCloud ISO, or a temporary initialized VM as part of the
active build.

The supported user-facing build command is:

```bash
IMG_ARCH=x86_64 task build
```

`IMG_ARCH` MUST be either `x86_64` or `aarch64`. Internal tooling may map those
values to Podman architecture names.

## Image Artifacts

The build MUST produce these files in `output/`:

- `disk.img`
- `disk.qcow2`
- `disk.vhdx`
- `initramfs.img.zst`
- `vmlinuz-virt`
- `arch.txt`
- `kernel-cmdline.txt`

The raw disk MUST be a GPT UEFI disk with:

- an EFI System Partition containing the unified kernel image
- an ext4 partition labeled `exasol-data` mounted as `/var`

## Guest Runtime

The guest MUST include Alpine, OpenRC, Podman, the Linux virt kernel, and SSH.
No application container is included by default.

The guest SHOULD support host sharing at `/mnt/host`:

- virtiofs with mount tag `hostshare` for QEMU and vfkit
- a secondary Hyper-V data disk fallback where virtiofs is unavailable

The guest SHOULD grow and resize the `/var` data partition when the packaged
disk is expanded.

The final SSH/user model is undecided. The current image autologins root on the
console, while the previous cloud-init flow used an `exasol` user and imported
keys from a shared folder.

## Packaging And Launchers

Packages MUST include the relevant disk image, `arch.txt`, `vm-config.json`,
and the platform launcher. Linux packages MUST also include `vmlinuz-virt`,
`initramfs.img.zst`, and `kernel-cmdline.txt` for the Podman/QEMU runner.

The Linux package uses the Podman-based QEMU runner with the raw disk, kernel,
initrd, and kernel command line artifacts. The macOS package uses vfkit with
the raw disk image. The Windows package uses Hyper-V with the VHDX image.

Linux hosts MUST NOT need QEMU, UEFI firmware, or virtiofsd installed directly
for the default launch path; those dependencies belong inside
`host/run/Containerfile`.

Runtime port forwarding is controlled by `host/run/vm-config.json` and the
platform launcher.

## Retired Behavior

The old build downloaded Alpine NoCloud images, generated a cloud-init ISO,
started QEMU to seed the VM image, waited for cloud-init completion, and shrank
the disk afterward. That path is retired and MUST NOT be reintroduced as the
default build.

Reusable generic shared-container behavior from the old implementation is kept
only as reference under `container/guest-services/shared-container/`.
