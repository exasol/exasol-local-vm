# Architecture

## Boundary

The provider owns the macOS Virtualization.framework VM, guest operating system,
Podman installation, SSH, VirtioFS devices, TCP forwarders, and provider
observation state. A caller supplies all workload behavior through shares and an
optional boot hook.

The guest image is workload-neutral. Its replaceable provider disk stores the
guest's writable `/var`, including Podman runtime state. The provider always
stores this disk below `state-dir`; callers persist workload data through
configured shares.

## Startup

`init` validates the versioned configuration, extracts the VM image, generates
provider SSH credentials, and records the shares that must remain compatible
with that VM. Pre-contract initialization preserves the existing provider disk
while refreshing replaceable VM assets.

`start` boots the guest with configured CPU and memory, attaches caller shares,
waits for SSH, mounts shares at their configured guest paths, creates exactly
the configured TCP forwarders, writes state, then invokes the configured hook as
root over SSH. The command streams and records hook output and waits for its
exit. Hook failure is recorded but deliberately leaves the VM running for
diagnostics.

## State

Provider JSON describes live VM phase, process identity, guest IP, SSH endpoint,
requested and actual forwards, fresh TCP health, mounted shares, and hook
attempts. It is a replaceable runtime observation, not caller product
configuration.

## Destruction

Provider-owned disks, credentials, logs, sockets, and metadata live below
`state-dir`. `destroy` stops the VM and removes that directory. It never follows
configuration paths to remove caller shares.
