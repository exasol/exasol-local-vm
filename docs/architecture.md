# Architecture

## Boundary

Exasol Local VM provides a Linux VM execution environment on macOS. The macOS
launcher owns VM initialization, boot, shutdown, persistent guest storage, host
sharing, loopback forwarding, and guest command execution.

The consuming runtime owns application images and containers. In particular,
the launcher does not load the Exasol image, create an Exasol container, choose
its ports, or report database readiness.

This boundary lets Linux-host and macOS runtimes use the same Podman install
logic. On macOS the commands execute inside the VM through `launcher run`; the
consumer does not know that SSH implements that command.

## Guest

The appliance is an Alpine Linux system containing Podman and SSH. SSH is a
launcher-private control channel. Application payloads are not embedded in the
VM package.

The writable data disk is mounted at `/var`, so Podman state and application
data persist across normal VM restarts. The host exchange directory is mounted
separately at `/mnt/host` through VirtioFS and is not persistent guest storage.

## Runtime Flow

The consumer first determines the application services it will publish. It then
starts the VM with a labeled forward for each guest port, runs its normal Podman
installation inside the VM, and reads the effective host ports from
`vm-state.json`.

Forward labels are opaque consumer identifiers. A requested nonzero host port
must bind exactly or startup fails. Host port zero delegates selection to the
operating system. This keeps forwarding fixed for the lifetime of a VM and
avoids a mutable networking API.

## Command Execution

`launcher run <command ...>` loads launcher-private runtime state, executes the
command in the guest, connects all standard streams, and preserves the remote
exit code. With terminal input and no command it opens a VM shell. Container
shells remain the consumer's responsibility and can be implemented by running
`podman exec` through this interface.

## Path Mapping

Host files required by guest commands are staged below the launcher shared
directory. A consumer maps a host path below that directory to the same relative
path below `/mnt/host` before passing it to a command.

The command being executed remains unaware of the transport. For example, image
installation always uses `podman load -i <runtime-path>`; a Linux-host runtime
uses the host path directly, while the macOS runtime supplies the mapped guest
path.

## State

`vm-state.json` is the consumer-facing result of a successful start. It contains
resource settings, the shared directory, the daemon PID, and effective labeled
forwards. It contains no SSH credentials, SSH forward, or guest IP.

Guest addressing and credentials are launcher-private state used by command
execution and shutdown. Consumers must use `launcher run`, `status`,
`health-check`, and `stop` rather than reading transport state.
