// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"encoding/json"
)

// TestPortOverrideAssignsRequestedHostPort verifies that --ports binds the
// exact host port the caller requested for a known service.
func TestPortOverrideAssignsRequestedHostPort(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()

	// Find a free port to request, then release it so the launcher can bind it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %v", err)
	}
	requestedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	f.StartVMWithPorts(2, 4096, 10, fmt.Sprintf("db:%d", requestedPort))

	statePath := filepath.Join(f.WorkDir, "vm-state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read vm-state.json: %v", err)
	}
	var state vmState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("failed to parse vm-state.json: %v", err)
	}
	if state.Ports["db"] != requestedPort {
		t.Fatalf("expected db port %d in vm-state.json, got %d", requestedPort, state.Ports["db"])
	}

	db := waitForDB(t, requestedPort, 5*time.Minute)
	defer db.Close()

	var result string
	if err := db.QueryRow("SELECT CURRENT_SESSION").Scan(&result); err != nil {
		t.Fatalf("query on overridden port %d failed: %v", requestedPort, err)
	}
	if strings.TrimSpace(result) == "" {
		t.Fatal("CURRENT_SESSION returned an empty value")
	}
}

// TestPortOverrideFailsIfPortInUse verifies that the VM is shut down and an
// error is returned when --ports requests a host port that is already bound.
func TestPortOverrideFailsIfPortInUse(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()

	// Hold a port open so the launcher cannot bind it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind a port to occupy: %v", err)
	}
	defer ln.Close()
	occupiedPort := ln.Addr().(*net.TCPAddr).Port

	err = f.StartVMExpectError(2, 4096, 10, "--ports", fmt.Sprintf("db:%d", occupiedPort))
	if err == nil {
		t.Fatalf("expected start to fail when port %d is already in use, but it succeeded", occupiedPort)
	}
}

// TestPortOverrideFailsForUnknownService verifies that the VM is shut down and
// an error is returned when --ports names a service not reported by the VM.
func TestPortOverrideFailsForUnknownService(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()

	const unknownService = "nonexistent"
	err := f.StartVMExpectError(2, 4096, 10, "--ports", fmt.Sprintf("%s:9090", unknownService))
	if err == nil {
		t.Fatalf("expected start to fail when service %q does not exist, but it succeeded", unknownService)
	}
	if !strings.Contains(err.Error(), unknownService) {
		t.Fatalf("expected error to mention service name %q, got: %v", unknownService, err)
	}
}
