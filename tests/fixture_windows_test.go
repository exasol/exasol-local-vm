// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build windows

package integration

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Platform defaults consumed by the shared code in fixture_common_test.go.
const (
	launcherBinaryName = "launcher.exe"
	launcherZipDefault = "../dist/windows-launcher-x86_64.zip"
)

// SSHCaptureDiagnostics is a no-op on windows: there is no guest VM to SSH
// into. Podman's own logs are captured by CopyLogsToFailuresDir via
// vm-state.json (and podman ps / podman logs left to the launcher's post-job
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

// StartDBInVM is a no-op on windows: the windows launcher's own guest-side
// init-db.sh starts the DB container as part of the launcher's `start`,
// so tests calling this after StartVM don't need to do anything extra.
func (f *LauncherFixture) StartDBInVM() {
	f.t.Helper()
}

// StartVM runs `launcher start <cpu> <ramMB> <dataSizeGB>` on the windows
// launcher (which still uses positional args and the historic --ports
// override syntax).
func (f *LauncherFixture) StartVM(cpu, ramMB, dataSizeGB int) {
	f.t.Helper()
	f.run("start",
		fmt.Sprintf("%d", cpu),
		fmt.Sprintf("%d", ramMB),
		fmt.Sprintf("%d", dataSizeGB),
	)
	f.vmRunning = true
}

// StartVMWithPorts is like StartVM but also passes --ports to override which
// host port is bound for each named service (e.g. "db:9090,ssh:2222"). Only
// available on windows — the mac launcher no longer supports this shape.
func (f *LauncherFixture) StartVMWithPorts(cpu, ramMB, dataSizeGB int, ports string) {
	f.t.Helper()
	f.run("start",
		"--ports", ports,
		fmt.Sprintf("%d", cpu),
		fmt.Sprintf("%d", ramMB),
		fmt.Sprintf("%d", dataSizeGB),
	)
	f.vmRunning = true
}

// StartVMExpectError runs `launcher start` with the given extra flags/args
// and returns any error rather than fataling, so callers can assert on
// failure cases. The launcher is not marked as running regardless of
// outcome. Windows-only for the same reason as StartVMWithPorts.
func (f *LauncherFixture) StartVMExpectError(cpu, ramMB, dataSizeGB int, extraArgs ...string) error {
	f.t.Helper()
	args := append([]string{"start"}, extraArgs...)
	args = append(args, fmt.Sprintf("%d", cpu), fmt.Sprintf("%d", ramMB), fmt.Sprintf("%d", dataSizeGB))
	cmd := exec.Command(f.BinaryPath, args...)
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		f.t.Logf("start output:\n%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
