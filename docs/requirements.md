# Generic macOS VM provider requirements

## Configuration

The provider must accept schema-versioned JSON defining positive CPU and memory
resources, named shares, named TCP forwards, an optional versioned boot hook,
and an optional caller-owned runtime disk.

Host paths must be absolute and canonical. Symlink and traversal escapes must be
rejected. Names, guest paths, and explicit host endpoints must be unique.
Configuration schema and hook API versions must be validated independently.

Only TCP forwarding is supported initially. Host port zero requests a dynamic
port; an unavailable explicit port is an error. Forward creation must depend
only on configuration.

## Lifecycle

Initialization must be idempotent for a compatible configuration. Changes to
shares or the caller runtime disk of an initialized VM must fail clearly rather
than attach an unintended path.

The provider must wait for SSH, mount shares, and establish forwarders before
running the root boot hook. Hook output must be streamed and logged. Failure or
cancellation must preserve the running VM and all caller-owned data.

Stop must be idempotent and allow the guest to flush writable storage. Destroy
must remove only provider-owned state below the exact `state-dir`.

## Observation

Status and health output must be JSON and independently schema-versioned. It must
report VM phase, PID, guest IP, SSH endpoint, requested and actual forwards,
share mount state, and hook phase, attempt, exit code, timestamps, and log path.
Health checking must freshly probe each configured guest TCP endpoint.

## Packaging

The VM image must contain Podman and SSH but no application image, application
initialization, readiness policy, or platform runtime other than the macOS
provider. A non-application hook and TCP echo service must be usable for
end-to-end validation.
