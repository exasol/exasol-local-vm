# TODO

## Current initramfs build

[ ] mount `/mnt/host` from virtiofs on QEMU/vfkit and from a Hyper-V data disk when virtiofs is unavailable

[ ] grow the `/var` partition and resize the filesystem when the disk has extra space

[ ] decide and implement the final SSH/user model; the current image autologins root

[ ] prevent VM startup from wasting time in firmware or boot menus

[ ] configure aggressive log rotation or move runtime logs to host-backed storage

[ ] add smoke tests for boot, networking, `/var`, `/mnt/host`, and service startup

[ ] reconcile package launchers with the final guest behavior, especially shared storage on macOS and Windows

## Preserved generic shared-container runtime

[ ] decide whether to port or retire the old shared-container manifest runtime

[ ] if ported, wire the preserved `container/guest-services/shared-container/` scripts into the new `Containerfile` and OpenRC model

[ ] if ported, update the preserved tests so they target the new image instead of the removed cloud-init flow
