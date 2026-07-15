// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

// Mac-only status tests: TestStatusAfterForcefulKill relies on SIGKILL to
// the launcher-owned daemon (identified by vm.pid) and SSH access to the
// guest OS to wait for the DB's initial config flush. Neither concept
// applies on windows, where podman owns the container's lifecycle. The
// cross-platform TestStatusLifecycle lives in status_test.go.
package integration

import (
	"fmt"
	"strings"
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

	// Wait for the database to finish its first-time initialization before the
	// forceful kill, so this exercises recovery of a fully-created database
	// rather than a create that was interrupted partway through.
	initialDBPort := readDBPortFromVMState(t, f)
	waitForDB(t, initialDBPort, 5*time.Minute).Close()
	waitForInitialDBStateFlushed(t, f, 2*time.Minute)

	f.KillVM()
	waitForVMStopped(t, f, 10*time.Second)

	if f.Status() {
		t.Fatal("expected status running=false after SIGKILL, got true")
	}

	f.StartVM(2, 4096, 10)

	if !f.Status() {
		t.Fatal("expected status running=true after restart following SIGKILL, got false")
	}

	// Verify the database engine itself recovered after the unclean shutdown,
	// not just that the VM and its port are up: connect and run a query.
	dbPort := readDBPortFromVMState(t, f)
	db := waitForDB(t, dbPort, 5*time.Minute)
	defer db.Close()

	var result string
	if err := db.QueryRow("SELECT CURRENT_SESSION").Scan(&result); err != nil {
		t.Fatalf("query after restart following SIGKILL failed: %v", err)
	}
	if strings.TrimSpace(result) == "" {
		t.Fatal("CURRENT_SESSION returned an empty value after restart following SIGKILL")
	}
}

func waitForInitialDBStateFlushed(t *testing.T, f *LauncherFixture, timeout time.Duration) {
	t.Helper()

	command := fmt.Sprintf(`deadline=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -le "$deadline" ]; do
  if [ -f /var/lib/exa/exasol.conf ] && [ ! -e /var/lib/exa/.exanano-initial-create-in-progress ]; then
    sync
    exit 0
  fi
  sleep 1
done
echo "timed out waiting for durable initial DB state" >&2
echo "/var/lib/exa contents:" >&2
ls -la /var/lib/exa >&2 || true
exit 1`, int(timeout.Seconds()))

	runSSHCommand(t, f, command)
}
