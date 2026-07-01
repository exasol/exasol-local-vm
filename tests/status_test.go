// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"testing"
	"time"
)

// TestStatusLifecycle verifies the status command reports the correct state
// before the VM is started, while it is running, and after a graceful stop.
func TestStatusLifecycle(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()

	if f.Status() {
		t.Fatal("expected status running=false before start, got true")
	}

	f.StartVM(2, 4096, 10)

	if !f.Status() {
		t.Fatal("expected status running=true after start, got false")
	}

	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	if f.Status() {
		t.Fatal("expected status running=false after stop, got true")
	}
}

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

	if state := f.VMState(); state.Ports["db"] == 0 {
		f.CopyLogsToFailuresDir(t.Name())
		f.SSHCaptureDiagnostics(t.Name())
		t.Fatal("VM restarted after SIGKILL but db port is absent from vm-state.json; logs saved to failures/" + t.Name())
	}
}
