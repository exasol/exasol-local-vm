// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGenerateSSHKeyPairWritesMatchingKeys(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	privateKeyPath := filepath.Join(tempDir, "id_ed25519")
	authorizedKeysPath := filepath.Join(tempDir, "authorized_keys")

	if err := generateSSHKeyPair(privateKeyPath, authorizedKeysPath); err != nil {
		t.Fatalf("generateSSHKeyPair() error = %v", err)
	}

	privateKeyInfo, err := os.Stat(privateKeyPath)
	if err != nil {
		t.Fatalf("failed to stat private key: %v", err)
	}
	if got := privateKeyInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("private key mode = %o, want 600", got)
	}

	privateKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		t.Fatalf("failed to read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		t.Fatalf("failed to parse generated private key: %v", err)
	}

	authorizedKey, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to read authorized key: %v", err)
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(authorizedKey)
	if err != nil {
		t.Fatalf("failed to parse authorized key: %v", err)
	}
	if got, want := ssh.FingerprintSHA256(publicKey), ssh.FingerprintSHA256(signer.PublicKey()); got != want {
		t.Fatalf("public key fingerprint = %q, want %q", got, want)
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

func TestTCPForwarderProbeReachable(t *testing.T) {
	t.Parallel()

	addr, closer := newReachableTCPAddr(t)
	defer closer.Close()

	forwarder := &tcpForwarder{name: "test", guestHost: addr.IP.String(), guestPort: addr.Port}

	state := forwarder.probe(context.Background(), time.Second)
	if state != "reachable" {
		t.Fatalf("probe() state = %q, want %q", state, "reachable")
	}
}

func TestTCPForwarderProbeRefused(t *testing.T) {
	t.Parallel()

	addr := newRefusedTCPAddr(t)
	forwarder := &tcpForwarder{name: "test", guestHost: addr.IP.String(), guestPort: addr.Port}

	state := forwarder.probe(context.Background(), time.Second)
	if state != "refused" {
		t.Fatalf("probe() state = %q, want %q", state, "refused")
	}
}

func TestProbeForwardersReportsEveryRegisteredPort(t *testing.T) {
	// Not run in parallel: exercises the package-level forwarder registry,
	// which must not race with other tests touching it.
	previous := forwarderRegistry
	forwarderRegistry = map[string]*tcpForwarder{}
	t.Cleanup(func() { forwarderRegistry = previous })

	reachableAddr, closer := newReachableTCPAddr(t)
	defer closer.Close()
	refusedAddr := newRefusedTCPAddr(t)

	registerForwarder("ssh", &tcpForwarder{name: "ssh", guestHost: reachableAddr.IP.String(), guestPort: reachableAddr.Port})
	registerForwarder("echo", &tcpForwarder{name: "echo", guestHost: refusedAddr.IP.String(), guestPort: refusedAddr.Port})

	got := probeForwarders(context.Background())

	if len(got) != 2 {
		t.Fatalf("probeForwarders() returned %d entries, want 2: %#v", len(got), got)
	}
	if got["ssh"].State != "reachable" {
		t.Fatalf("ssh state = %q, want %q", got["ssh"].State, "reachable")
	}
	if got["echo"].State != "refused" {
		t.Fatalf("echo state = %q, want %q", got["echo"].State, "refused")
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
				"ssh":  {State: "reachable"},
				"echo": {State: "blocked"},
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
	if ports["echo"].State != "blocked" {
		t.Fatalf("echo state = %q, want %q", ports["echo"].State, "blocked")
	}
}
