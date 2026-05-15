# Exasol Nano DB container

This directory builds a container image that wraps the upstream Exasol Nano
`.run` payload. The image is consumed by `exasol-personal`'s `install local`
flow: the launcher stages this tarball into the VM and the guest's
`load-shared-container` service runs it on boot.

## Files

| File | Purpose |
|---|---|
| `Containerfile` | Ubuntu 24.04 base, extracts the `.run` at build time, sets the production entrypoint. |
| `entrypoint.sh` | Keeps stdin open via FIFO (the DB treats Ctrl-D as a shutdown trigger) and forwards SIGTERM/SIGINT to a clean shutdown. |
| `nano-version.env` | Pinned release tag + per-arch URL + SHA256. The SHAs are captured automatically on the first build with empty values. |
| `build.sh` | Downloads + verifies the `.run`, builds the container, saves a zstd-compressed tarball (named `.tar.gz` for launcher-metadata compatibility) under `<repo>/output/nano-container/`. |
| `.gitignore` | Keeps the downloaded `.run` out of the tree. |

## Build

From the repo root:

```bash
task build-nano-container IMG_ARCH=aarch64
# or
task build-nano-container IMG_ARCH=x86_64
```

Or directly:

```bash
./nano-container/build.sh aarch64
```

Outputs:

```
output/nano-container/
  exasol-nano-<tag>-<arch>.tar.gz
  exasol-nano-<tag>-<arch>.tar.gz.sha256
```

The tarball and sidecar SHA file are what the launcher's `metadata.json`
should reference as `container.image`.

## Bumping the upstream `.run` version

1. Edit `nano-version.env`:
   - Update `NANO_RELEASE_TAG`.
   - Update both `*_URL` lines.
   - Clear both `*_SHA` values (leave them empty after the `=`).
2. Run `./build.sh aarch64` (and `./build.sh x86_64` if needed). The script
   downloads each `.run`, captures its SHA256 back into `nano-version.env`,
   and builds the image.
3. Review the diff to `nano-version.env` and commit it.

On any subsequent rebuild the SHA is verified strictly. A mismatch fails the
build with a clear message; the only way to "update" a pinned SHA is to
explicitly clear it and re-run.

## Why the entrypoint wrapper

The upstream `.run` runs an interactive controller that treats `Ctrl-D`
(EOF on stdin) as a clean shutdown signal. `podman run -d` wires the
container's stdin to `/dev/null`, which produces EOF immediately, so a naive
`ENTRYPOINT ["/opt/exasol-nano/run.sh"]` shuts the database down a second
after it starts. `entrypoint.sh` keeps stdin open via a FIFO fed by
`tail -f /dev/null`, and maps `SIGTERM`/`SIGINT` back to "close the FIFO" so
that `podman stop` still triggers the documented clean-shutdown path.

## Architecture

The `.run` is architecture-specific (verifiable with `file` against any of
the bundled ELF binaries). Containers don't translate CPU instructions, so
the guest VM's CPU has to match the `.run`'s architecture. The launcher runs
on Apple Silicon Macs which only support `arm64` guests, so `aarch64` is the
production build target. The `x86_64` target exists for parity and for any
future non-Apple-Silicon host.
