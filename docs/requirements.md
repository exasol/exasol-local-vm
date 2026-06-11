# Exasol Local Runtime Requirements

## Purpose

Exasol Local Runtime provides a local Exasol database instance through a
platform-specific runtime artifact.

The runtime artifact is the product interface consumed by users, automation,
test frameworks, developer tools, installers, and other projects. Platform
backends such as virtual machines, WSL environments, native containers, or other
host-specific mechanisms are implementation details.

## Product Shape

### Platform artifacts

The product should provide one primary runtime artifact for each supported
platform:

- macOS
- Windows
- Linux

Each platform artifact should expose the same user-facing capabilities and user
experience as far as reasonably possible.

The product should be distributed as a single runtime binary per platform. An
archive may be used as the publishing format, for example to carry documentation,
license files, checksums, or other distribution metadata.

The product should not require sidecar runtime assets for the normal user flow.

### Distribution trust

The macOS runtime artifact must be signed and notarized for distribution.

The Windows runtime artifact must be signed for distribution.

## Runtime Capabilities

The runtime artifact should provide a common lifecycle interface across
platforms. The interface should allow users and integrations to:

- prepare local runtime state before first use
- start a local Exasol DB instance
- stop the local Exasol DB instance
- inspect whether the local DB is running
- obtain connection information
- configure runtime resources where the platform supports it
- configure or grow persistent storage where the platform supports it
- explicitly reset or delete local runtime state when a fresh DB instance is
  desired

Exact binary names, command names, and command-line syntax are not specified by
these requirements.

### Prepare

The runtime should provide a way to prepare local state before the first start.
Preparation should make the artifact ready to start the local Exasol DB without
requiring users or integrations to assemble backend-specific assets manually.

### Start

The runtime should provide a way to start the local Exasol DB instance.
Starting should allow users or integrations to provide runtime resource settings
where the platform supports them.

Starting should make connection information available once the local DB is ready
or report clearly that startup failed.

### Stop

The runtime should provide a way to stop the local Exasol DB instance.
Stopping should preserve persistent DB data and should avoid leaving the local
runtime in a state that prevents a later start.

### Connection information

The runtime should provide a way to obtain the endpoint needed to connect to the
local Exasol DB from the host.

The connection information should be usable by both people and integrations.

### Resource and storage configuration

The runtime should provide a way to configure CPU and memory resources where the
platform supports it.

Where the runtime manages explicit persistent storage size, it should provide a
way to configure or grow that storage. Shrinking persistent storage is not
required.

### Status

The runtime should provide a way to inspect status. Status should allow users
and integrations to determine whether the local DB is usable, not initialized,
stopped, starting, running, or failed where the platform can determine that
state.

### Reset

The runtime should provide an explicit way to reset or delete local runtime
state when a fresh local DB instance is desired.

Resetting or deleting persistent DB data must be an intentional destructive
operation. It must not happen as part of normal start or stop behavior.

## Output and Integration

The runtime should support both human-friendly output and machine-readable
output.

Human-friendly output should be suitable for interactive use.

Machine-readable output should be suitable for integrations. JSON is the
preferred machine-readable format.

The machine-readable contract should expose at least:

- whether the local DB is running
- the localhost endpoint for connecting to the DB
- effective runtime resource or storage settings that consumers need in order to
  understand the running instance

## Database Payload

The runtime artifact should include the Exasol nano DB payload needed for the
normal runtime flow.

Users and consuming projects should not need to pull, download, or provide the
DB image manually for normal use.

The runtime should avoid unnecessary payload reloads when an existing local
runtime state can be reused.

## Networking

The local Exasol DB must be reachable from the host through localhost.

The preferred endpoint is:

- `127.0.0.1:8563`

If that port is unavailable or a platform requires a different mapping, the
runtime may expose the DB on another localhost port. In that case, the runtime
must report the actual endpoint clearly.

Backend-specific addresses, such as guest VM IPs or container-internal
addresses, are not the user-facing connection contract.

## Persistence and Local State

The runtime should preserve local DB data across normal stop/start cycles.

Normal start, stop, status, or connection-discovery operations should not delete
persistent DB data.

The runtime should create and use local runtime state in a predictable location.

Where the runtime manages explicit persistent storage size, it should support
creating storage on first use, reusing existing storage, and growing storage
where the platform supports it.

Shrinking persistent storage is not required.

## Platform Expectations

The macOS, Windows, and Linux artifacts should provide the same external
interface and behavior as much as reasonably possible.

Platform-specific permissions, prerequisites, and dependencies may differ and
should be documented where they affect users.

The requirements do not specify the backend implementation for any platform.
