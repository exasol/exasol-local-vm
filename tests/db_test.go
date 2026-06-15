// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
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

// waitForDB polls until the Exasol DB at 127.0.0.1:port accepts connections,
// then returns an open *sql.DB handle.
func waitForDB(t *testing.T, port int, timeout time.Duration) *sql.DB {
	t.Helper()

	connStr := exasol.NewConfig("SYS", "exasol").
		Host("127.0.0.1").
		Port(port).
		ValidateServerCertificate(false).
		String()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		db, err := sql.Open("exasol", connStr)
		if err == nil {
			if pingErr := db.Ping(); pingErr == nil {
				return db
			} else {
				lastErr = pingErr
				_ = db.Close()
			}
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}
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