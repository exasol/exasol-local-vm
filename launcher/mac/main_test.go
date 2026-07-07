// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestVersionCommandOutput(t *testing.T) {
	previousVersion := runnerVersion
	runnerVersion = "v1.2.3"
	t.Cleanup(func() { runnerVersion = previousVersion })

	var output bytes.Buffer
	versionCmd(&output)
	if got, want := output.String(), "v1.2.3\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestAuthorizedKeyFromPrivateKeyMatchesGeneratedPublicKey(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	privateKeyPath := filepath.Join(tempDir, "id_ed25519")
	generatedAuthorizedKeysPath := filepath.Join(tempDir, "generated_authorized_keys")
	importedAuthorizedKeysPath := filepath.Join(tempDir, "imported_authorized_keys")

	if err := generateSSHKeyPair(privateKeyPath, generatedAuthorizedKeysPath); err != nil {
		t.Fatalf("generateSSHKeyPair() error = %v", err)
	}

	authorizedKey, err := authorizedKeyFromPrivateKey(privateKeyPath)
	if err != nil {
		t.Fatalf("authorizedKeyFromPrivateKey() error = %v", err)
	}
	if err := os.WriteFile(importedAuthorizedKeysPath, authorizedKey, 0644); err != nil {
		t.Fatalf("failed to write imported authorized key: %v", err)
	}

	generatedAuthorizedKey, err := os.ReadFile(generatedAuthorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to read generated authorized key: %v", err)
	}
	importedAuthorizedKey, err := os.ReadFile(importedAuthorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to read imported authorized key: %v", err)
	}
	if string(importedAuthorizedKey) != string(generatedAuthorizedKey) {
		t.Fatalf("imported authorized key does not match generated public key")
	}
}

func TestVersionCheckRuntimeConfigFromOptionsUsesRunnerContract(t *testing.T) {
	config := versionCheckRuntimeConfigFromOptions(VersionCheckOptions{
		Enabled:         false,
		IntervalSeconds: 42,
		Identity:        "exasol-personal;deployment;small;default",
		URL:             "https://metrics.example.test/v1/version-check",
	})

	if config.Enabled {
		t.Fatal("expected version checks to be disabled")
	}
	if config.IntervalSeconds != 42 {
		t.Fatalf("expected interval 42, got %d", config.IntervalSeconds)
	}
	if config.Identity != "exasol-personal;deployment;small;default" {
		t.Fatalf("unexpected identity: %q", config.Identity)
	}
	if config.URL != "https://metrics.example.test/v1/version-check" {
		t.Fatalf("unexpected URL: %q", config.URL)
	}
	if config.OperatingSystem != versionCheckOperatingSystem(runtime.GOOS) {
		t.Fatalf("unexpected operating system: %q", config.OperatingSystem)
	}
}

func TestVersionCheckRuntimeConfigFromOptionsDefaults(t *testing.T) {
	config := versionCheckRuntimeConfigFromOptions(VersionCheckOptions{
		Enabled: true,
	})

	if !config.Enabled {
		t.Fatal("expected version checks to be enabled")
	}
	if config.IntervalSeconds != defaultVersionCheckIntervalSeconds {
		t.Fatalf("expected default interval %d, got %d", defaultVersionCheckIntervalSeconds, config.IntervalSeconds)
	}
	if config.Identity != defaultVersionCheckIdentity {
		t.Fatalf("expected default identity %q, got %q", defaultVersionCheckIdentity, config.Identity)
	}
	if config.URL != defaultVersionCheckURL {
		t.Fatalf("expected default URL %q, got %q", defaultVersionCheckURL, config.URL)
	}
	if config.OperatingSystem != versionCheckOperatingSystem(runtime.GOOS) {
		t.Fatalf("unexpected operating system: %q", config.OperatingSystem)
	}
}

func TestVersionCheckOperatingSystem(t *testing.T) {
	tests := map[string]string{
		"darwin":  "MacOS",
		"linux":   "Linux",
		"windows": "Windows",
		"":        "unknown",
		"freebsd": "freebsd",
	}

	for goos, want := range tests {
		if got := versionCheckOperatingSystem(goos); got != want {
			t.Fatalf("versionCheckOperatingSystem(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestWriteVersionCheckRuntimeConfig(t *testing.T) {
	tempDir := t.TempDir()
	config := VersionCheckRuntimeConfig{
		Enabled:         true,
		IntervalSeconds: 7,
		Identity:        "exasol-personal;deployment;small;default",
		URL:             "https://metrics.example.test/v1/version-check",
		OperatingSystem: "MacOS",
	}

	if err := writeVersionCheckRuntimeConfig(tempDir, config); err != nil {
		t.Fatalf("writeVersionCheckRuntimeConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tempDir, versionCheckRuntimeConfigName))
	if err != nil {
		t.Fatalf("failed to read version-check runtime config: %v", err)
	}

	var decoded VersionCheckRuntimeConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to parse version-check runtime config: %v", err)
	}
	if decoded != config {
		t.Fatalf("decoded config mismatch: got %+v, want %+v", decoded, config)
	}
}

func TestRefreshInitDBScriptUpdatesOnlyDatabaseInitializer(t *testing.T) {
	sharedDir := t.TempDir()
	initDir := filepath.Join(sharedDir, "init")
	if err := os.MkdirAll(initDir, 0755); err != nil {
		t.Fatalf("failed to create init directory: %v", err)
	}

	initDBPath := filepath.Join(initDir, "init-db.sh")
	if err := os.WriteFile(initDBPath, []byte("old initializer"), 0644); err != nil {
		t.Fatalf("failed to seed old init-db.sh: %v", err)
	}
	sshKeyPath := filepath.Join(sharedDir, "authorized_keys")
	const sshKey = "preserve this SSH key\n"
	if err := os.WriteFile(sshKeyPath, []byte(sshKey), 0600); err != nil {
		t.Fatalf("failed to seed authorized_keys: %v", err)
	}

	if err := refreshInitDBScript(sharedDir); err != nil {
		t.Fatalf("refreshInitDBScript() error = %v", err)
	}

	updatedScript, err := os.ReadFile(initDBPath)
	if err != nil {
		t.Fatalf("failed to read refreshed init-db.sh: %v", err)
	}
	if string(updatedScript) == "old initializer" || len(updatedScript) == 0 {
		t.Fatalf("init-db.sh was not refreshed")
	}
	preservedKey, err := os.ReadFile(sshKeyPath)
	if err != nil {
		t.Fatalf("failed to read authorized_keys: %v", err)
	}
	if string(preservedKey) != sshKey {
		t.Fatalf("authorized_keys changed during init-db.sh refresh: got %q", preservedKey)
	}
}

func TestWaitForSSHServiceAcceptsSSHBanner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("SSH-2.0-test\r\n"))
	}()

	if err := waitForSSHService(ln.Addr().String(), time.Second); err != nil {
		t.Fatalf("waitForSSHService() error = %v", err)
	}
	<-done
}

func TestWaitForSSHServiceRejectsNonSSHBanner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n"))
			_ = conn.Close()
		}
	}()

	if err := waitForSSHService(ln.Addr().String(), 50*time.Millisecond); err == nil {
		t.Fatal("expected waitForSSHService() to reject a non-SSH banner")
	}
}

func TestClassifyDialErr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil error is reachable", err: nil, want: "reachable"},
		{name: "context deadline exceeded is timeout", err: context.DeadlineExceeded, want: "timeout"},
		{
			name: "wrapped context deadline exceeded is timeout",
			err:  fmt.Errorf("dial tcp: %w", context.DeadlineExceeded),
			want: "timeout",
		},
		{
			name: "net.Error reporting Timeout() is timeout",
			err:  &net.DNSError{IsTimeout: true},
			want: "timeout",
		},
		{name: "connection refused is refused", err: syscall.ECONNREFUSED, want: "refused"},
		{
			name: "wrapped connection refused is refused",
			err:  &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			want: "refused",
		},
		{name: "permission denied is blocked", err: syscall.EPERM, want: "blocked"},
		{name: "access denied is blocked", err: syscall.EACCES, want: "blocked"},
		{name: "host unreachable is blocked", err: syscall.EHOSTUNREACH, want: "blocked"},
		{name: "network unreachable is blocked", err: syscall.ENETUNREACH, want: "blocked"},
		{name: "unrecognized error defaults to blocked", err: errors.New("something else"), want: "blocked"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyDialErr(tc.err); got != tc.want {
				t.Fatalf("classifyDialErr(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// newReachableTCPAddr starts a listener that accepts and immediately closes
// every connection, and returns its address. The caller must close it (e.g.
// via defer) once done.
func newReachableTCPAddr(t *testing.T) (*net.TCPAddr, io.Closer) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	return ln.Addr().(*net.TCPAddr), ln
}

// newRefusedTCPAddr returns an address nothing is listening on, by binding
// an ephemeral port and releasing it immediately, to reliably get a
// connection-refused outcome.
func newRefusedTCPAddr(t *testing.T) *net.TCPAddr {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	if err := ln.Close(); err != nil {
		t.Fatalf("failed to close listener: %v", err)
	}

	return addr
}

func TestLoopbackForwarderProbeReachable(t *testing.T) {
	t.Parallel()

	addr, closer := newReachableTCPAddr(t)
	defer closer.Close()

	forwarder := &LoopbackForwarder{name: "test", guestHost: addr.IP.String(), guestPort: addr.Port}

	state := forwarder.Probe(context.Background(), time.Second)
	if state != "reachable" {
		t.Fatalf("Probe() state = %q, want %q", state, "reachable")
	}
}

func TestLoopbackForwarderProbeRefused(t *testing.T) {
	t.Parallel()

	addr := newRefusedTCPAddr(t)
	forwarder := &LoopbackForwarder{name: "test", guestHost: addr.IP.String(), guestPort: addr.Port}

	state := forwarder.Probe(context.Background(), time.Second)
	if state != "refused" {
		t.Fatalf("Probe() state = %q, want %q", state, "refused")
	}
}

func TestProbeForwardersReportsEveryRegisteredPort(t *testing.T) {
	// Not run in parallel: exercises the package-level forwarder registry,
	// which must not race with other tests touching it.
	previous := forwarderRegistry
	forwarderRegistry = map[string]*LoopbackForwarder{}
	t.Cleanup(func() { forwarderRegistry = previous })

	reachableAddr, closer := newReachableTCPAddr(t)
	defer closer.Close()
	refusedAddr := newRefusedTCPAddr(t)

	registerForwarder("ssh", &LoopbackForwarder{name: "ssh", guestHost: reachableAddr.IP.String(), guestPort: reachableAddr.Port})
	registerForwarder("db", &LoopbackForwarder{name: "db", guestHost: refusedAddr.IP.String(), guestPort: refusedAddr.Port})

	got := probeForwarders(context.Background())

	if len(got) != 2 {
		t.Fatalf("probeForwarders() returned %d entries, want 2: %#v", len(got), got)
	}
	if got["ssh"].State != "reachable" {
		t.Fatalf("ssh state = %q, want %q", got["ssh"].State, "reachable")
	}
	if got["db"].State != "refused" {
		t.Fatalf("db state = %q, want %q", got["db"].State, "refused")
	}
}

func TestQueryHealthCheckParsesPortStates(t *testing.T) {
	// Not run in parallel: changes the process working directory, since
	// vmSocketPath is a relative path.
	tempDir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to change to temp directory: %v", err)
	}
	t.Cleanup(func() { os.Chdir(originalDir) })

	ln, err := net.Listen("unix", vmSocketPath)
	if err != nil {
		t.Fatalf("failed to listen on fake socket: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		var req struct {
			Request string `json:"request"`
		}
		if err := json.NewDecoder(conn).Decode(&req); err != nil || req.Request != "health-check" {
			return
		}
		json.NewEncoder(conn).Encode(map[string]any{ //nolint:errcheck
			"ports": map[string]portHealthResponse{
				"ssh": {State: "reachable"},
				"db":  {State: "blocked"},
			},
		})
	}()

	ports, err := queryHealthCheck()
	if err != nil {
		t.Fatalf("queryHealthCheck() error = %v", err)
	}
	if ports["ssh"].State != "reachable" {
		t.Fatalf("ssh state = %q, want %q", ports["ssh"].State, "reachable")
	}
	if ports["db"].State != "blocked" {
		t.Fatalf("db state = %q, want %q", ports["db"].State, "blocked")
	}
}
