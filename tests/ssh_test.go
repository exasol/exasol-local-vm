// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSSHKeyGeneration verifies that `launcher init` produces a valid ED25519 key pair:
//   - vm-ssh-key (private key) with mode 0600
//   - vm-shared/authorized_keys (public key) with mode 0644
//   - the public key entry in authorized_keys corresponds to the generated private key
func TestSSHKeyGeneration(t *testing.T) {
	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()

	privateInfo, err := os.Stat(f.SSHKeyPath())
	if err != nil {
		t.Fatalf("failed to stat private key %s: %v", f.SSHKeyPath(), err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected private key mode: got %o, want 600", privateInfo.Mode().Perm())
	}

	authorizedKeysPath := filepath.Join(f.WorkDir, "vm-shared", "authorized_keys")
	publicInfo, err := os.Stat(authorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to stat authorized_keys %s: %v", authorizedKeysPath, err)
	}
	if publicInfo.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected authorized_keys mode: got %o, want 644", publicInfo.Mode().Perm())
	}

	derivedPub, err := exec.Command("ssh-keygen", "-y", "-f", f.SSHKeyPath()).CombinedOutput()
	if err != nil {
		t.Fatalf("failed to derive public key from private key: %v\noutput:\n%s", err, string(derivedPub))
	}

	authorizedBytes, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to read authorized_keys %s: %v", authorizedKeysPath, err)
	}

	derivedLine := strings.TrimSpace(string(derivedPub))
	authorizedLine := strings.TrimSpace(string(authorizedBytes))
	if derivedLine != authorizedLine {
		t.Fatalf("authorized key mismatch\nexpected: %q\nactual:   %q", derivedLine, authorizedLine)
	}
}

// TestSSHPortAvailableAfterStart verifies that the VM exposes an SSH listener
// on the loopback forwarder port reported in the init output after `launcher start`.
func TestSSHPortAvailableAfterStart(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)

	sshPort := readSSHPortFromVMState(t, f)
	addr := fmt.Sprintf("127.0.0.1:%d", sshPort)

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("failed to dial forwarded SSH port %s: %v", addr, err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}

	banner := make([]byte, 256)
	n, err := conn.Read(banner)
	if err != nil {
		t.Fatalf("failed to read SSH banner from %s: %v", addr, err)
	}
	if !strings.HasPrefix(string(banner[:n]), "SSH-") {
		t.Fatalf("unexpected SSH banner from %s: %q", addr, string(banner[:n]))
	}
}

// TestSSHLoginWithGeneratedKey verifies that a shell command can be executed
// inside the VM over SSH using the key pair produced by `launcher init`.
func TestSSHLoginWithGeneratedKey(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)

	output := strings.TrimSpace(runSSHCommand(t, f, "echo hello"))
	if output != "hello" {
		t.Fatalf("unexpected SSH command output: got %q, want %q", output, "hello")
	}
}

// TestSSHPortForwarding verifies that the loopback TCP forwarder correctly
// proxies a connection from the host into the guest network.
func TestSSHPortForwarding(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)

	forwardedPort := readSSHPortFromVMState(t, f)
	addr := fmt.Sprintf("127.0.0.1:%d", forwardedPort)

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.Fatalf("failed to dial forwarded port %s: %v", addr, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}

	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("failed to read server greeting from %s: %v", addr, err)
	}
	if !strings.HasPrefix(string(buf[:n]), "SSH-") {
		t.Fatalf("unexpected server greeting on forwarded port %s: %q", addr, string(buf[:n]))
	}

	if _, err := io.WriteString(conn, "SSH-2.0-integration-test\r\n"); err != nil {
		t.Fatalf("failed to write client banner to %s: %v", addr, err)
	}

	if _, err := runSSHCommandCapture(t, f, "true"); err != nil {
		t.Fatalf("forwarded SSH command failed: %v", err)
	}
}

func runSSHCommandCapture(t *testing.T, f *LauncherFixture, command string) (string, error) {
	t.Helper()

	sshPort := readSSHPortFromVMState(t, f)
	target := "root@127.0.0.1"
	args := []string{
		"-i", f.SSHKeyPath(),
		"-p", fmt.Sprintf("%d", sshPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "BatchMode=yes",
		target,
		fmt.Sprintf("sh -c %s", shellQuote(command)),
	}

	cmd := exec.Command("ssh", args...)
	cmd.Dir = f.WorkDir
	out, err := cmd.Output()
	return string(out), err
}
