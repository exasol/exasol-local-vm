// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build windows

package integration

import (
	"testing"
	"time"
)

// Platform defaults consumed by the shared code in fixture_common_test.go.
const (
	launcherBinaryName = "launcher.exe"
	launcherZipDefault = "../dist/windows-runner-x86_64.zip"
)

// SSHCaptureDiagnostics is a no-op on windows: there is no guest VM to SSH
// into. Podman's own logs are captured by CopyLogsToFailuresDir via
// vm-state.json (and podman ps / podman logs left to the runner's post-job
// steps if needed).
func (f *LauncherFixture) SSHCaptureDiagnostics(testName string) {
	f.t.Helper()
	_ = testName
}

// waitForVMStopped is a no-op on windows: the launcher's stopCmd is
// synchronous (`podman stop --time 30 <name>` blocks until the container is
// stopped or SIGKILLed), so by the time StopVM returns the container is
// already gone. This matches the shape callers expect from the mac helper.
func waitForVMStopped(t *testing.T, f *LauncherFixture, timeout time.Duration) {
	t.Helper()
	_ = f
	_ = timeout
}
