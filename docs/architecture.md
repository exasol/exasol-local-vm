# Architecture

## Boundary

The provider owns the macOS Virtualization.framework VM, guest operating system,
Podman installation, SSH, VirtioFS devices, TCP forwarders, and provider
observation state. A caller supplies all workload behavior through shares and an
optional boot hook.

The guest image is workload-neutral. Its persistent provider disk stores the
guest's writable `/var`, including Podman runtime state. A caller may supply an
opaque runtime disk when it needs to retain guest-backed application storage;
otherwise the provider creates a replaceable disk below `state-dir`.

## Startup

`init` validates the versioned configuration, extracts the VM image, generates
provider SSH credentials, and records the share/runtime-disk identity that must
remain compatible with that VM.

`start` boots the guest with configured CPU and memory, attaches caller shares,
waits for SSH, mounts shares at their configured guest paths, creates exactly
the configured TCP forwarders, writes state, then invokes the configured hook as
root over SSH. The command streams and records hook output and waits for its
exit. Hook failure or cancellation is recorded but deliberately leaves the VM
running for diagnostics.

## State

Provider JSON describes live VM phase, process identity, guest IP, SSH endpoint,
requested and actual forwards, fresh TCP health, mounted shares, and hook
attempts. It is a replaceable runtime observation, not caller product
configuration.

## Destruction

Provider-owned disks, credentials, logs, sockets, and metadata live below
`state-dir`. `destroy` stops the VM and removes that directory. It never follows
configuration paths to remove caller shares or runtime disks.
