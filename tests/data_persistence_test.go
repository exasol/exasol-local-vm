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

const gib = int64(1024 * 1024 * 1024)

type vmState struct {
	Ports map[string]int `json:"ports"`
}

// TestDataDiskCreatedOnFirstStart verifies that `launcher start` creates a sparse
// raw data disk at the expected path with the requested size.
func TestDataDiskCreatedOnFirstStart(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	const dataSizeGB = 10
	f.StartVM(2, 4096, dataSizeGB)

	info, err := os.Stat(f.DataDiskPath())
	if err != nil {
		t.Fatalf("data disk not found at %s: %v", f.DataDiskPath(), err)
	}

	expectedSize := int64(dataSizeGB) * gib
	if info.Size() != expectedSize {
		t.Fatalf("unexpected data disk size: got %d bytes, want %d", info.Size(), expectedSize)
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type for %s", f.DataDiskPath())
	}

	allocatedBytes := int64(st.Blocks) * 512
	if allocatedBytes >= info.Size()/2 {
		t.Fatalf("data disk is not sparse enough: allocated %d bytes for logical size %d bytes", allocatedBytes, info.Size())
	}
}

// TestDataPersistenceAcrossRestart writes a sentinel file to the VM data volume,
// stops the VM, starts it again, and verifies the file is still present.
func TestDataPersistenceAcrossRestart(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)
	waitForDataMount(t, f, 120*time.Second)

	sentinel := fmt.Sprintf("persist-%d", time.Now().UnixNano())
	runSSHCommand(t, f, fmt.Sprintf("printf %s > /var/persist-test.txt && sync", shellQuote(sentinel)))

	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	f.StartVM(2, 4096, 10)
	got := strings.TrimSpace(runSSHCommand(t, f, "cat /var/persist-test.txt"))
	if got != sentinel {
		t.Fatalf("sentinel mismatch after restart: got %q, want %q", got, sentinel)
	}
}

// TestDataDiskGrowth verifies that `launcher resize-data` grows the data disk
// without destroying existing data.
func TestDataDiskGrowth(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)
	waitForDataMount(t, f, 120*time.Second)

	sentinel := fmt.Sprintf("grow-%d", time.Now().UnixNano())
	runSSHCommand(t, f, fmt.Sprintf("printf %s > /var/grow-test.txt && sync", shellQuote(sentinel)))

	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	if err := f.ResizeData(20); err != nil {
		t.Fatalf("resize-data failed: %v", err)
	}

	info, err := os.Stat(f.DataDiskPath())
	if err != nil {
		t.Fatalf("failed to stat resized data disk: %v", err)
	}
	expectedSize := int64(20) * gib
	if info.Size() != expectedSize {
		t.Fatalf("unexpected resized data disk size: got %d bytes, want %d", info.Size(), expectedSize)
	}

	f.StartVM(2, 4096, 20)
	got := strings.TrimSpace(runSSHCommand(t, f, "cat /var/grow-test.txt"))
	if got != sentinel {
		t.Fatalf("sentinel mismatch after resize: got %q, want %q", got, sentinel)
	}
}

// TestDataDiskShrinkRejected verifies that passing a data_size_gb smaller than
// the existing disk size causes `launcher start` to exit with a non-zero status.
func TestDataDiskShrinkRejected(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 20)
	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	err := f.ResizeData(10)
	if err == nil {
		t.Fatal("expected resize-data to a smaller size to fail, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "shrink") {
		t.Fatalf("expected shrink-related error, got: %v", err)
	}
}

// TestDataDiskSizeMatchReusesExisting verifies that starting the VM a second time
// with the same data_size_gb leaves the disk unchanged.
func TestDataDiskSizeMatchReusesExisting(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)
	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	beforeInfo, err := os.Stat(f.DataDiskPath())
	if err != nil {
		t.Fatalf("failed to stat data disk before restart: %v", err)
	}
	beforeStat, ok := beforeInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type before restart for %s", f.DataDiskPath())
	}

	f.StartVM(2, 4096, 10)
	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	afterInfo, err := os.Stat(f.DataDiskPath())
	if err != nil {
		t.Fatalf("failed to stat data disk after restart: %v", err)
	}
	afterStat, ok := afterInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type after restart for %s", f.DataDiskPath())
	}

	expectedSize := int64(10) * gib
	if afterInfo.Size() != expectedSize {
		t.Fatalf("unexpected data disk size after restart: got %d bytes, want %d", afterInfo.Size(), expectedSize)
	}
	if beforeStat.Ino != afterStat.Ino {
		t.Fatalf("data disk was recreated (inode changed from %d to %d)", beforeStat.Ino, afterStat.Ino)
	}
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

func waitForDataMount(t *testing.T, f *LauncherFixture, timeout time.Duration) {
	t.Helper()

	sshPort := readSSHPortFromVMState(t, f)
	checkVarMountCmd := `awk '$2 == "/var" && $3 == "ext4" { found=1 } END { exit(found ? 0 : 1) }' /proc/mounts`
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("ssh",
			"-i", f.SSHKeyPath(),
			"-p", strconv.Itoa(sshPort),
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=5",
			"-o", "BatchMode=yes",
			"root@127.0.0.1",
			fmt.Sprintf("sh -c %s", shellQuote(checkVarMountCmd)),
		)
		cmd.Dir = f.WorkDir
		if err := cmd.Run(); err == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for ext4 data disk to be mounted at /var after %v", timeout)
}

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

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
