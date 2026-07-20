// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin || windows

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exasol/exasol-driver-go"
)

// TestDBConnection starts the VM, waits for the Exasol database inside it to
// accept connections, then verifies that a simple SQL query works.
func TestDBConnection(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)

	dbPort := readDBPortFromVMState(t, f)
	db := waitForDB(t, dbPort, 5*time.Minute)
	defer db.Close()

	var result string
	if err := db.QueryRow("SELECT CURRENT_SESSION").Scan(&result); err != nil {
		t.Fatalf("simple query failed: %v", err)
	}
	if strings.TrimSpace(result) == "" {
		t.Fatal("CURRENT_SESSION returned an empty value")
	}
}

// waitForDB polls until the Exasol DB at localhost:port can actually serve a
// query, then returns an open *sql.DB handle.
//
// It runs a real "SELECT 1" rather than db.Ping(). The forwarded port can
// accept TCP/websocket connections (and Ping can succeed) while the SQL engine
// is still initializing or crash-looping - e.g. pddserver briefly binds 8563
// before the InitProcess fails on a bad restart. Requiring a query round-trip
// to succeed proves the engine has finished starting and is genuinely serving,
// which is the readiness signal the DB reconnection tests depend on.
//
// Host is "localhost" (not "127.0.0.1") because podman-for-windows's gvproxy
// binds forwarded ports on the IPv6 loopback (::1) only; 127.0.0.1 refuses
// TCP even when the port is being served. Go's dialer resolves "localhost"
// to both ::1 and 127.0.0.1 and tries each, so this address works on
// windows-latest (::1 succeeds), macOS (both succeed), and any future
// Linux host (both succeed).
func waitForDB(t *testing.T, port int, timeout time.Duration) *sql.DB {
	t.Helper()

	connStr := exasol.NewConfig("SYS", "exasol").
		Host("localhost").
		Port(port).
		ValidateServerCertificate(false).
		String()

	db, err := sql.Open("exasol", connStr)
	if err != nil {
		t.Fatalf("failed to open exasol connection on port %d: %v", port, err)
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		// Per-attempt timeout so a single hung connection doesn't swallow the
		// whole budget; database/sql discards the bad conn and we retry.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var one int
		queryErr := db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		cancel()
		if queryErr == nil && one == 1 {
			return db
		}
		lastErr = queryErr
		time.Sleep(3 * time.Second)
	}
	_ = db.Close()
	t.Fatalf("Exasol DB on port %d did not become ready within %v: %v", port, timeout, lastErr)
	return nil
}

// readDBPortFromVMState reads the forwarded DB port from vm-state.json.
func readDBPortFromVMState(t *testing.T, f *LauncherFixture) int {
	t.Helper()

	statePath := filepath.Join(f.WorkDir, "vm-state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read vm state %s: %v", statePath, err)
	}

	var state vmState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("failed to parse vm state %s: %v", statePath, err)
	}

	dbPort := state.Ports["db"]
	if dbPort <= 0 {
		t.Fatalf("vm state does not contain a valid db port: %s", statePath)
	}
	return dbPort
}

// Ensure fmt is used (port formatting in error message).
var _ = fmt.Sprintf