// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	launcherBinaryName = "launcher"
	launcherZipDefault = "../dist/mac-launcher-aarch64.zip"
)

func (f *LauncherFixture) DataDiskPath() string {
	return filepath.Join(f.WorkDir, "vm", "data.img")
}

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

func (f *LauncherFixture) SSHCaptureDiagnostics(testName string) {
	f.t.Helper()
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
podman ps -a`

	cmd := exec.Command(f.BinaryPath, "run", "sh", "-c", script)
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Logf("launcher run diagnostics error: %v", err)
	}
	dest := filepath.Join("failures", testName)
	if err := os.MkdirAll(dest, 0755); err != nil {
		f.t.Logf("could not create dir %s: %v", dest, err)
		return
	}
	diagPath := filepath.Join(dest, "diagnostics.txt")
	if err := os.WriteFile(diagPath, out, 0644); err != nil {
		f.t.Logf("could not write %s: %v", diagPath, err)
		return
	}
	f.t.Logf("saved %s (%d bytes)", diagPath, len(out))
}

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
		if err != nil || proc.Signal(syscall.Signal(0)) != nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("VM did not stop within %v", timeout)
}

func runVMCommand(t *testing.T, f *LauncherFixture, command ...string) string {
	t.Helper()

	out, err := runVMCommandCapture(f, command...)
	if err != nil {
		t.Fatalf("launcher run %q failed: %v\noutput:\n%s", command, err, out)
	}

	return out
}

func runVMCommandCapture(f *LauncherFixture, command ...string) (string, error) {
	cmd := exec.Command(f.BinaryPath, append([]string{"run", "--"}, command...)...)
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
