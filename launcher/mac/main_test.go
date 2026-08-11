// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

func TestVersionCommandOutput(t *testing.T) {
	previousVersion := launcherVersion
	launcherVersion = "v1.2.3"
	t.Cleanup(func() { launcherVersion = previousVersion })

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

func TestParseForwardSpecGivenValidSpecification(t *testing.T) {
	// Given
	const value = "database:8563:0"

	// When
	got, err := parseForwardSpec(value)

	// Then
	if err != nil {
		t.Fatalf("parseForwardSpec() error = %v", err)
	}
	want := ForwardSpec{Name: "database", GuestPort: 8563, HostPort: 0}
	if got != want {
		t.Fatalf("parseForwardSpec() = %+v, want %+v", got, want)
	}
}

func TestParseForwardSpecGivenInvalidSpecification(t *testing.T) {
	tests := []string{
		"database:8563",
		":8563:0",
		"data base:8563:0",
		"database:0:0",
		"database:65536:0",
		"database:8563:-1",
		"database:8563:65536",
	}

	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			// Given an invalid specification, when it is parsed, then it is rejected.
			if _, err := parseForwardSpec(value); err == nil {
				t.Fatalf("parseForwardSpec(%q) did not return an error", value)
			}
		})
	}
}

func TestForwardSpecListGivenDuplicateName(t *testing.T) {
	// Given
	var specs forwardSpecList
	if err := specs.Set("database:8563:0"); err != nil {
		t.Fatalf("first Set() error = %v", err)
	}

	// When
	err := specs.Set("database:2580:0")

	// Then
	if err == nil {
		t.Fatal("duplicate forward name was accepted")
	}
}

func TestRefreshInitAssetsGivenLegacyDatabaseAssets(t *testing.T) {
	// Given
	sharedDir := t.TempDir()
	initDir := filepath.Join(sharedDir, "init")
	if err := os.MkdirAll(initDir, 0755); err != nil {
		t.Fatalf("failed to create init directory: %v", err)
	}

	initDBPath := filepath.Join(initDir, "init-db.sh")
	if err := os.WriteFile(initDBPath, []byte("legacy initializer"), 0644); err != nil {
		t.Fatalf("failed to seed old init-db.sh: %v", err)
	}
	for _, name := range []string{"config.json", "exasol-nano-db.tar.gz", "exasol-nano-db.tar.gz.metadata"} {
		if err := os.WriteFile(filepath.Join(initDir, name), []byte("legacy"), 0644); err != nil {
			t.Fatalf("failed to seed %s: %v", name, err)
		}
	}
	for _, name := range []string{"version-check.json", "slc.json"} {
		if err := os.WriteFile(filepath.Join(sharedDir, name), []byte("legacy"), 0644); err != nil {
			t.Fatalf("failed to seed %s: %v", name, err)
		}
	}
	sshKeyPath := filepath.Join(sharedDir, "authorized_keys")
	const sshKey = "preserve this SSH key\n"
	if err := os.WriteFile(sshKeyPath, []byte(sshKey), 0600); err != nil {
		t.Fatalf("failed to seed authorized_keys: %v", err)
	}
	previousInitAssets := initAssets
	initAssets = createTestTarXZ(t, map[string]string{
		"init/init.sh": "new VM initializer\n",
	})
	t.Cleanup(func() { initAssets = previousInitAssets })

	// When
	if err := refreshInitAssets(sharedDir); err != nil {
		t.Fatalf("refreshInitAssets() error = %v", err)
	}

	// Then
	updatedScript, err := os.ReadFile(filepath.Join(initDir, "init.sh"))
	if err != nil {
		t.Fatalf("failed to read refreshed init.sh: %v", err)
	}
	if string(updatedScript) != "new VM initializer\n" {
		t.Fatalf("init.sh was not refreshed: got %q", updatedScript)
	}
	for _, path := range []string{
		initDBPath,
		filepath.Join(initDir, "config.json"),
		filepath.Join(initDir, "exasol-nano-db.tar.gz"),
		filepath.Join(initDir, "exasol-nano-db.tar.gz.metadata"),
		filepath.Join(sharedDir, "version-check.json"),
		filepath.Join(sharedDir, "slc.json"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy asset %s still exists", path)
		}
	}
	preservedKey, err := os.ReadFile(sshKeyPath)
	if err != nil {
		t.Fatalf("failed to read authorized_keys: %v", err)
	}
	if string(preservedKey) != sshKey {
		t.Fatalf("authorized_keys changed during init-db.sh refresh: got %q", preservedKey)
	}
}

func createTestTarXZ(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var archive bytes.Buffer
	xzWriter, err := xz.NewWriter(&archive)
	if err != nil {
		t.Fatalf("failed to create xz writer: %v", err)
	}
	tarWriter := tar.NewWriter(xzWriter)
	for name, content := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(content)),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write tar content: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := xzWriter.Close(); err != nil {
		t.Fatalf("failed to close xz writer: %v", err)
	}

	return archive.Bytes()
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
