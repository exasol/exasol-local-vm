# Release workflow

Build the workload-neutral VM image on Linux, then build, test, sign, and
notarize the Virtualization.framework provider on macOS. The resulting archive
contains only the provider binary and its embedded generic VM image.

The v2 schema must remain unreleased while its first consumer validates a manual
artifact. After that integration passes, freeze the config, hook, state, and
packaging contracts and publish v2.0.0. Retain every v1 artifact unchanged for
older consumers.
