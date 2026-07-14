// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin || windows

// Cross-platform status-lifecycle tests. The mac-only case
// (TestStatusAfterForcefulKill, which relies on SIGKILL to the vm.pid
// daemon and SSH-based DB-state flushing) lives in status_darwin_test.go.
package integration

import (
	"testing"
	"time"
)

// TestStatusLifecycle verifies the status command reports the correct state
// before the launcher is started, while the container is running, and after
// a graceful stop.
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
