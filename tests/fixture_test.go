// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

// Package integration contains end-to-end tests for the mac-runner launcher binary.
// Tests require the notarized artifact zip produced by the build-packages CI workflow.
// By default the fixture looks for ../dist/mac-runner-aarch64.zip (the path the
// workflow writes it to); override with the LAUNCHER_ZIP env var.
package integration

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// LauncherFixture manages a temporary working directory with the launcher binary.
// Call NewLauncherFixture to create one; call Cleanup when the test is done.
type LauncherFixture struct {
	// WorkDir is the temporary directory in which the launcher is run.
	WorkDir string
	// BinaryPath is the path to the launcher binary inside WorkDir.
	BinaryPath string

	vmRunning bool
	t         *testing.T
}

// NewLauncherFixture locates the mac-runner-aarch64.zip artifact, unzips it into
// a fresh temporary directory, and returns a ready-to-use fixture.
// If the zip cannot be found the test is skipped.
func NewLauncherFixture(t *testing.T) *LauncherFixture {
	t.Helper()

	if err := os.MkdirAll("failures", 0755); err != nil {
		t.Logf("warning: could not create failures dir: %v", err)
	}

	zipPath := findLauncherZip(t)

	workDir, err := os.MkdirTemp("", "mac-runner-test-*")
	if err != nil {
		t.Fatalf("failed to create temp work dir: %v", err)
	}

	if err := unzipTo(zipPath, workDir); err != nil {
		os.RemoveAll(workDir)
		t.Fatalf("failed to unzip launcher artifact %s: %v", zipPath, err)
	}

	binaryPath := filepath.Join(workDir, "launcher")
	if err := os.Chmod(binaryPath, 0755); err != nil {
		os.RemoveAll(workDir)
		t.Fatalf("failed to chmod launcher binary: %v", err)
	}

	return &LauncherFixture{
		WorkDir:    workDir,
		BinaryPath: binaryPath,
		t:          t,
	}
}

// Init runs `launcher init` in WorkDir, extracting the embedded VM package and
// init assets and generating an SSH key pair.
func (f *LauncherFixture) Init() {
	f.t.Helper()
	f.run("init")
}

// StartVM runs `launcher start <cpu> <ramMB> <dataSizeGB>` and waits for it to
// return. Marks the VM as running so Cleanup will call Stop.
func (f *LauncherFixture) StartVM(cpu, ramMB, dataSizeGB int) {
	f.t.Helper()
	f.run("start",
		fmt.Sprintf("%d", cpu),
		fmt.Sprintf("%d", ramMB),
		fmt.Sprintf("%d", dataSizeGB),
	)
	f.vmRunning = true
}

// StartVMWithPorts is like StartVM but also passes --ports to override which
// host port is bound for each named service (e.g. "db:9090,ssh:2222").
func (f *LauncherFixture) StartVMWithPorts(cpu, ramMB, dataSizeGB int, ports string) {
	f.t.Helper()
	f.run("start",
		"--ports", ports,
		fmt.Sprintf("%d", cpu),
		fmt.Sprintf("%d", ramMB),
		fmt.Sprintf("%d", dataSizeGB),
	)
	f.vmRunning = true
}

// StartVMExpectError runs `launcher start` with the given extra flags/args and
// returns any error rather than fataling, so callers can assert on failure cases.
// The VM is not marked as running regardless of outcome.
func (f *LauncherFixture) StartVMExpectError(cpu, ramMB, dataSizeGB int, extraArgs ...string) error {
	f.t.Helper()
	args := append([]string{"start"}, extraArgs...)
	args = append(args, fmt.Sprintf("%d", cpu), fmt.Sprintf("%d", ramMB), fmt.Sprintf("%d", dataSizeGB))
	cmd := exec.Command(f.BinaryPath, args...)
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		f.t.Logf("start output:\n%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopVM runs `launcher stop`.
func (f *LauncherFixture) StopVM() {
	f.t.Helper()
	f.run("stop")
	f.vmRunning = false
}

// Status runs `launcher status` and returns whether the VM reports itself as running.
func (f *LauncherFixture) Status() bool {
	f.t.Helper()
	cmd := exec.Command(f.BinaryPath, "status")
	cmd.Dir = f.WorkDir
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("launcher status: %v", err)
	}
	var result struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		f.t.Fatalf("failed to parse status output %q: %v", strings.TrimSpace(string(out)), err)
	}
	return result.Running
}

// KillVM sends SIGKILL to the daemon process identified by vm.pid.
// Use this to simulate an unclean shutdown; prefer StopVM for graceful stops.
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

// VMState reads and parses vm-state.json from WorkDir.
func (f *LauncherFixture) VMState() vmState {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.WorkDir, "vm-state.json"))
	if err != nil {
		f.t.Fatalf("failed to read vm-state.json: %v", err)
	}
	var state vmState
	if err := json.Unmarshal(data, &state); err != nil {
		f.t.Fatalf("failed to parse vm-state.json: %v", err)
	}
	return state
}

// SSHCaptureDiagnostics SSHes into the running VM and collects diagnostic
// information (dmesg, Podman state, container logs) into failures/<testName>/diagnostics.txt.
// Best-effort: logs warnings on error rather than fataling.
func (f *LauncherFixture) SSHCaptureDiagnostics(testName string) {
	f.t.Helper()
	state := f.VMState()
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

// CopyLogsToFailuresDir copies VM log files from WorkDir into failures/<testName>/
// so they survive fixture cleanup and can be inspected after a test failure.
func (f *LauncherFixture) CopyLogsToFailuresDir(testName string) {
	f.t.Helper()
	dest := filepath.Join("failures", testName)
	if err := os.MkdirAll(dest, 0755); err != nil {
		f.t.Logf("could not create failures dir %s: %v", dest, err)
		return
	}
	for _, name := range []string{"vm.log", "vm-console.log", "vm-state.json"} {
		data, err := os.ReadFile(filepath.Join(f.WorkDir, name))
		if err != nil {
			if !os.IsNotExist(err) {
				f.t.Logf("could not read %s for failures dir: %v", name, err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dest, name), data, 0644); err != nil {
			f.t.Logf("could not write %s to failures dir: %v", name, err)
		}
	}
}

// ResizeData runs `launcher resize-data <newSizeGB>` and returns any error.
// The launcher's stderr is included in the returned error so callers can
// inspect the human-readable rejection message (e.g. "shrinking is not supported").
func (f *LauncherFixture) ResizeData(newSizeGB int) error {
	f.t.Helper()
	cmd := exec.Command(f.BinaryPath, "resize-data", fmt.Sprintf("%d", newSizeGB))
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		f.t.Logf("resize-data output:\n%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Cleanup stops the VM if it is running and removes WorkDir.
func (f *LauncherFixture) Cleanup() {
	if f.vmRunning {
		_ = exec.Command(f.BinaryPath, "stop").Run()
	}
	os.RemoveAll(f.WorkDir)
}

// SSHKeyPath returns the path to the generated private key after Init.
func (f *LauncherFixture) SSHKeyPath() string {
	return filepath.Join(f.WorkDir, "vm-ssh-key")
}

// DataDiskPath returns the expected path of the VM data disk after StartVM.
func (f *LauncherFixture) DataDiskPath() string {
	return filepath.Join(f.WorkDir, "vm", "data.img")
}

// requireIntegration is kept as a no-op helper for compatibility with existing
// tests; integration tests now always run when this package is executed.
func requireIntegration(t *testing.T) {
	t.Helper()
}

// run executes the launcher binary with the given arguments inside WorkDir and
// fails the test if the command exits non-zero.
func (f *LauncherFixture) run(args ...string) {
	f.t.Helper()
	cmd := exec.Command(f.BinaryPath, args...)
	cmd.Dir = f.WorkDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("launcher %v: %v", args, err)
	}
}

// launcherBinaryPath returns the path to the launcher binary, preferring the
// LAUNCHER_BINARY env var and falling back to the default build output location.
func findLauncherZip(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("LAUNCHER_ZIP"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Skipf("LAUNCHER_ZIP=%q not found; skipping", p)
	}

	// Default: dist/mac-runner-aarch64.zip at the repo root.
	// go test sets the working directory to the package directory (tests/),
	// so ../dist/ resolves to the repo root's dist/ directory.
	defaultPath := filepath.Join("..", "dist", "mac-runner-aarch64.zip")
	if _, err := os.Stat(defaultPath); err != nil {
		t.Skipf(
			"launcher zip not found at %s; download the mac-launcher artifact from CI or set LAUNCHER_ZIP",
			defaultPath,
		)
	}
	return defaultPath
}

// unzipTo extracts all files from zipPath into destDir, preserving permissions.
func unzipTo(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		destPath := filepath.Join(destDir, filepath.Clean(f.Name))

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, f.Mode()); err != nil {
				return fmt.Errorf("mkdir %s: %w", destPath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", destPath, err)
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("create %s: %w", destPath, err)
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}

		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %s: %w", f.Name, copyErr)
		}
	}
	return nil
}
