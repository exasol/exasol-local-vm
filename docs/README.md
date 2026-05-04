# Build Notes

The VM image is built from container stages instead of a temporary booted VM.
The active build path is:

1. `Taskfile.yml` builds a minimal Exasol Nano container image from `nano/`.
2. The image is saved as `container/exasol_nano_${IMG_ARCH}.tar`.
3. The root `Containerfile` installs Alpine, Podman, OpenRC services, and the
   matching saved Nano image.
4. The initramfs stage packages the guest root filesystem, excluding `/boot`
   and `/var`.
5. The disk-image stage creates a unified kernel image and a GPT disk with an
   EFI System Partition plus an ext4 `exasol-data` partition for `/var`.

No cloud-init ISO is generated, and the build does not run QEMU to initialize
the image.

## Build Inputs

`IMG_ARCH` is the architecture selector used by the public build command:

```bash
IMG_ARCH=x86_64 task build
```

Accepted values:

- `x86_64`
- `aarch64`

The matching Nano installer must already exist in `nano/`.

## Runtime Shape

The VM boots a unified kernel image from the EFI System Partition. The root
filesystem comes from the initramfs and switches to a tmpfs copy early in boot.
The `/var` tree is stored on the `exasol-data` ext4 partition.

Current runtime behavior is intentionally minimal:

- Podman and the Exasol Nano image are present in the guest.
- OpenRC starts base services, networking, Podman, `acpid`, and `sshd`.
- The guest currently autologins as root on configured consoles.

The following behavior still needs implementation:

- Mount `/mnt/host` through virtiofs on QEMU/vfkit and a data disk on Hyper-V.
- Grow the `/var` partition and resize its filesystem when needed.
- Decide the final SSH/user model.

## Preserved Reference Code

The old cloud-init based build included a generic shared-container runtime with
manifest parsing, SSH-key import, and smoke tests. Those files are preserved as
reference under:

```text
container/guest-services/shared-container/
```

They are not part of the current image until they are ported into the new
`Containerfile` and OpenRC boot model.
