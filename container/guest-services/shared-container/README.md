# Shared Container Runtime Reference

These files are preserved from the previous cloud-init based image build.
They are not wired into the current initramfs image.

The current image starts without an application container. If generic
shared-container loading is needed again, port these scripts into the new
`Containerfile` and OpenRC startup model instead of reintroducing cloud-init.

Useful pieces kept here:

- `load-shared-container.sh` and `load-shared-container.initd`: load and run a
  manifest-defined container from `/mnt/host`.
- `import-shared-keys.sh` and `import-shared-keys.initd`: replace guest SSH keys
  from `/mnt/host/authorized_keys`.
- `setup-system.reference.sh`: old first-boot partition and host-share setup
  logic, kept only as implementation reference.
- `host-tools/`: old host-side helpers for manifest port validation and SSH key
  generation.
- `tests/`: old runtime smoke tests for shared-container and SSH-key behavior.
