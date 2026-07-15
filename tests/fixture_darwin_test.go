// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Platform defaults consumed by the shared code in fixture_common_test.go.
const (
	launcherBinaryName = "launcher"
	launcherZipDefault = "../dist/mac-runner-aarch64.zip"
)

// SSHKeyPath returns the path to the ED25519 private key `launcher init`
// generated for talking to the guest OS's sshd. Not applicable on windows.
func (f *LauncherFixture) SSHKeyPath() string {
	return filepath.Join(f.WorkDir, "vm-ssh-key")
}

// DataDiskPath returns the expected path of the raw VM data disk after
// StartVM. On windows this file does not exist (data lives in a podman
// named volume), so the accessor is darwin-only.
func (f *LauncherFixture) DataDiskPath() string {
	return filepath.Join(f.WorkDir, "vm", "data.img")
}

// KillVM sends SIGKILL to the daemon process identified by vm.pid. Use this
// to simulate an unclean shutdown; prefer StopVM for graceful stops. On
// windows there is no launcher-owned daemon (podman keeps the container
// alive), so this helper is darwin-only.
func (f *LauncherFixture) KillVM() {
	f.t.Helper()
	pidPath := filepath.Join(f.WorkDir, "vm.pid")
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		f.t.Fatalf("KillVM: failed to read %s: %v", pidPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		f.t.Fatalf("KillVM: invalid pid in %s: %v", pidPath, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		f.t.Fatalf("KillVM: failed to find process %d: %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		f.t.Fatalf("KillVM: failed to SIGKILL process %d: %v", pid, err)
	}
	f.vmRunning = false
}

// SSHCaptureDiagnostics SSHes into the running VM and collects diagnostic
// information (dmesg, Podman state, container logs) into
// failures/<testName>/diagnostics.txt. Best-effort: logs warnings on error
// rather than fataling. Windows has a no-op stub in fixture_windows_test.go.
func (f *LauncherFixture) SSHCaptureDiagnostics(testName string) {
	f.t.Helper()
	stateData, err := os.ReadFile(filepath.Join(f.WorkDir, "vm-state.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			f.t.Logf("SSHCaptureDiagnostics: could not read vm-state.json: %v", err)
		}
		return
	}
	var state vmState
	if err := json.Unmarshal(stateData, &state); err != nil {
		f.t.Logf("SSHCaptureDiagnostics: could not parse vm-state.json: %v", err)
		return
	}
	sshPort := state.Ports["ssh"]
	if sshPort == 0 {
		f.t.Logf("SSHCaptureDiagnostics: no ssh port in vm-state.json, skipping")
		return
	}
	dest := filepath.Join("failures", testName)
	if err := os.MkdirAll(dest, 0755); err != nil {
		f.t.Logf("SSHCaptureDiagnostics: could not create dir %s: %v", dest, err)
		return
	}
	const script = `set +e
echo '=== dmesg ==='
dmesg
echo '=== /proc/mounts ==='
cat /proc/mounts
echo '=== df -h ==='
df -h
echo '=== podman info ==='
podman info
echo '=== podman ps -a ==='
podman ps -a
echo '=== podman inspect exasol-local-db ==='
podman inspect exasol-local-db 2>&1
echo '=== podman logs exasol-local-db ==='
podman logs exasol-local-db 2>&1`
	cmd := exec.Command("ssh",
		"-i", f.SSHKeyPath(),
		"-p", strconv.Itoa(sshPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"root@127.0.0.1",
		fmt.Sprintf("sh -c %s", shellQuote(script)),
	)
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Logf("SSHCaptureDiagnostics: ssh error: %v", err)
	}
	diagPath := filepath.Join(dest, "diagnostics.txt")
	if writeErr := os.WriteFile(diagPath, out, 0644); writeErr != nil {
		f.t.Logf("SSHCaptureDiagnostics: could not write %s: %v", diagPath, writeErr)
		return
	}
	f.t.Logf("SSHCaptureDiagnostics: saved %s (%d bytes)", diagPath, len(out))
}

// waitForVMStopped polls until the launcher-owned daemon process (identified
// by vm.pid) has exited, or until timeout. Windows has a no-op stub in
// fixture_windows_test.go because there is no launcher-owned process — the
// container's lifecycle is entirely inside the podman service, and stop is
// synchronous.
func waitForVMStopped(t *testing.T, f *LauncherFixture, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	pidPath := filepath.Join(f.WorkDir, "vm.pid")

	for time.Now().Before(deadline) {
		pidData, err := os.ReadFile(pidPath)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			t.Fatalf("failed to read vm.pid: %v", err)
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil {
			t.Fatalf("invalid pid in %s: %v", pidPath, err)
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("VM did not stop within %v", timeout)
}

// runSSHCommand SSHes into the running guest and runs the given shell
// snippet, returning its combined stdout. Retries for up to 30s while the
// guest sshd is still starting up. Darwin-only.
func runSSHCommand(t *testing.T, f *LauncherFixture, command string) string {
	t.Helper()

	sshPort := readSSHPortFromVMState(t, f)
	args := []string{
		"-i", f.SSHKeyPath(),
		"-p", strconv.Itoa(sshPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "BatchMode=yes",
		"root@127.0.0.1",
		fmt.Sprintf("sh -c %s", shellQuote(command)),
	}

	var lastErr error
	var lastStderr string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("ssh", args...)
		cmd.Dir = f.WorkDir
		stdout, err := cmd.Output()
		if err == nil {
			return string(stdout)
		}

		lastErr = err
		if exitErr, ok := err.(*exec.ExitError); ok {
			lastStderr = string(exitErr.Stderr)
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatalf("ssh command failed after retries: %v\ncommand: %s\nstderr:\n%s", lastErr, command, lastStderr)
	return ""
}

// readSSHPortFromVMState reads the forwarded SSH port from vm-state.json.
// Darwin-only.
func readSSHPortFromVMState(t *testing.T, f *LauncherFixture) int {
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

	sshPort := state.Ports["ssh"]
	if sshPort <= 0 {
		t.Fatalf("vm state does not contain a valid ssh port: %s", statePath)
	}

	return sshPort
}

// shellQuote produces a single-quoted, shell-safe version of s.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
