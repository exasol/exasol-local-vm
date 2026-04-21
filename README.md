# exasol-nano-vm

Build tool for producing a lightweight Linux VM image with Exasol Nano
pre-installed, for distribution alongside the exasol-personal launcher
on macOS.

## What it produces

A pre-built Alpine Linux VM image containing Exasol Nano, ready to boot via
[vfkit](https://github.com/crc-org/vfkit) on Apple Silicon macOS.

The image is used by `exasol personal` to run the Exasol Database
locally with no external dependencies.

## Architecture
