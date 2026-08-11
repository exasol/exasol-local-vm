// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

// Mac-only status tests: TestStatusAfterForcefulKill relies on SIGKILL to
// the launcher-owned daemon identified by vm.pid. That concept does not
// apply on Windows, where Podman owns the container's lifecycle. The
// cross-platform TestStatusLifecycle lives in status_test.go.
package integration

import (
	"testing"
	"time"
)

// TestStatusAfterForcefulKill verifies that a SIGKILL leaves the status
// reporting running=false and that the VM can be restarted cleanly afterward.
func TestStatusAfterForcefulKill(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)

	if !f.Status() {
		t.Fatal("expected status running=true after start, got false")
	}

	f.KillVM()
	waitForVMStopped(t, f, 10*time.Second)

	if f.Status() {
		t.Fatal("expected status running=false after SIGKILL, got true")
	}

	f.StartVM(2, 4096, 10)

	if !f.Status() {
		t.Fatal("expected status running=true after restart following SIGKILL, got false")
	}

	if output := runVMCommand(t, f, "true"); output != "" {
		t.Fatalf("unexpected output from guest command after restart: %q", output)
	}
}
