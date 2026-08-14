# Exasol Local VM Requirements

## Purpose

Exasol Local VM provides a local Linux VM on macOS for a consuming runtime. It
must not own or embed the consuming runtime's application containers.

## Appliance

The VM appliance must include the operating-system services and Podman
installation required to run containers. It must not include the Exasol
container payload.

The appliance must provide writable guest storage that survives normal stop and
start cycles. Host-shared files must use a separate exchange directory.

## Launcher

The macOS launcher must allow a consumer to initialize, start, inspect, and stop
the VM. It must support CPU, memory, and grow-only data-disk configuration.

The launcher must execute arbitrary guest commands without exposing the
underlying transport. It must stream standard input, output, and error and
preserve the guest command's exit code. Interactive use must support a VM shell.

## Networking

The launcher must accept labeled guest-to-host TCP forwards when the VM starts.
A nonzero host port must bind exactly or fail. Host port zero must request an
available loopback port.

The effective mapping must be machine-readable and keyed by the supplied label.
Transport-only ports and guest addresses must not be part of the consumer
contract.

## Host Sharing

The launcher must report the host exchange directory and mount it at a stable
guest location. This must allow a consumer to translate staged host paths into
paths accepted by guest commands such as `podman load -i`.

## Packaging

The macOS launcher artifact must embed a VM package built from the current
source. Distribution artifacts must be signed and notarized.
