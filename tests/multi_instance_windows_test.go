// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build windows

package integration

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTwoInstancesRunSimultaneously exercises the plan.md § "Allow
// multiple instances to be launched simultaneously" acceptance case:
// two launcher installs in two independent working directories, each
// with its own container ID suffix, running side-by-side against the
// same podman machine.
//
// Per-instance host ports are supplied explicitly via --ports rather
// than relying on the launcher's random-fallback path — this keeps the
// test deterministic and independent of TOCTOU races on port 8563.
//
// After both instances are up we run `SELECT 1` against each on its
// respective port, then stop instance A first and confirm instance B
// stays reachable. The test's t.Cleanup handlers explicitly `podman
// volume rm` each instance's data volume so the run doesn't leave
// orphan volumes behind (the launcher's own stop preserves data across
// stop/start, so it leaves the volume in place).
func TestTwoInstancesRunSimultaneously(t *testing.T) {
	requireIntegration(t)

	// Reserve two free host ports up-front so the launcher's
	// selectAllHostPorts probe finds them free at start time.
	portA := freeHostPort(t)
	portB := freeHostPort(t)
	if portA == portB {
		t.Fatalf("freeHostPort returned duplicate port %d", portA)
	}

	fA := NewLauncherFixture(t)
	defer fA.Cleanup()
	fB := NewLauncherFixture(t)
	defer fB.Cleanup()

	fA.Init()
	fB.Init()

	idA := readFixtureContainerID(t, fA)
	idB := readFixtureContainerID(t, fB)
	if idA == idB {
		t.Fatalf("two independent init calls produced the same container id %q", idA)
	}
	// Register per-instance volume cleanup up-front so a mid-test
	// panic still leaves the podman machine clean.
	t.Cleanup(func() { podmanVolumeRm(t, "exasol-nano-data-"+idA) })
	t.Cleanup(func() { podmanVolumeRm(t, "exasol-nano-data-"+idB) })

	fA.StartVMWithPorts(2, 4096, 10, fmt.Sprintf("db:%d", portA))
	fB.StartVMWithPorts(2, 4096, 10, fmt.Sprintf("db:%d", portB))

	// Both vm-state.json files should report the requested ports.
	if got := fA.VMState().Ports["db"]; got != portA {
		t.Fatalf("instance A: expected db port %d in vm-state.json, got %d", portA, got)
	}
	if got := fB.VMState().Ports["db"]; got != portB {
		t.Fatalf("instance B: expected db port %d in vm-state.json, got %d", portB, got)
	}

	// Both containers must exist under distinct per-instance names.
	assertContainerExists(t, "exasol-local-db-"+idA)
	assertContainerExists(t, "exasol-local-db-"+idB)

	dbA := waitForDB(t, portA, 5*time.Minute)
	defer dbA.Close()
	dbB := waitForDB(t, portB, 5*time.Minute)
	defer dbB.Close()

	assertSelectOne(t, dbA, "instance A")
	assertSelectOne(t, dbB, "instance B")

	// Stopping instance A must not affect instance B.
	fA.StopVM()
	assertSelectOne(t, dbB, "instance B after A stopped")
}

// freeHostPort returns a currently-unbound TCP port on 127.0.0.1 by
// asking the kernel and immediately releasing it. Two calls in
// sequence produce distinct ports because the kernel's ephemeral
// range only reuses a port after TIME_WAIT.
func freeHostPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("failed to release probe port: %v", err)
	}
	return port
}

// readFixtureContainerID reads the .containerId file that the
// launcher's init writes into WorkDir. Fatals the test if absent —
// init having run successfully is a precondition for calling this.
func readFixtureContainerID(t *testing.T, f *LauncherFixture) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.WorkDir, ".containerId"))
	if err != nil {
		t.Fatalf("read .containerId in %s: %v", f.WorkDir, err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		t.Fatalf(".containerId in %s is empty", f.WorkDir)
	}
	return id
}

// assertContainerExists fatals if the named podman container is not
// present. Uses `podman container exists` (exit 0 present, non-zero
// absent) rather than parsing `podman ps` for robustness.
func assertContainerExists(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command("podman", "container", "exists", name)
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected podman container %q to exist: %v", name, err)
	}
}

// assertSelectOne runs `SELECT 1` and fatals on any error. `who` is a
// short label so failures name which instance broke.
func assertSelectOne(t *testing.T, db *sql.DB, who string) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT 1").Scan(&got); err != nil {
		t.Fatalf("%s: SELECT 1 failed: %v", who, err)
	}
	if got != 1 {
		t.Fatalf("%s: SELECT 1 returned %d, expected 1", who, got)
	}
}

// podmanVolumeRm best-effort removes a podman volume. Never fails the
// test — the volume may already be gone if the launcher's stop
// happened to remove it (currently it does not, but stay
// forward-compatible), or podman may not be available at cleanup
// time. Errors are logged so leaks are visible.
func podmanVolumeRm(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command("podman", "volume", "rm", "--force", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("podman volume rm %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
}
