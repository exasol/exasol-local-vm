// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

// Data-persistence tests that depend on mac-only implementation details:
// the raw disk image at vm/data.img, filesystem inode identity, and SSH
// access to the guest OS. The one platform-agnostic case in this suite
// (TestDataDiskShrinkRejected) lives in data_persistence_test.go.
package integration

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const gib = int64(1024 * 1024 * 1024)

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

	const sentinel = "exasol-persistence-sentinel"
	runSSHCommand(t, f, fmt.Sprintf("printf %s > /var/persist-test.txt && sync", shellQuote(sentinel)))

	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)
	f.StartVM(2, 4096, 10)

	got := strings.TrimSpace(runSSHCommand(t, f, "cat /var/persist-test.txt"))
	if got != sentinel {
		t.Fatalf("persistence sentinel mismatch: got %q, want %q", got, sentinel)
	}
}

// TestDataDiskGrowth verifies that `launcher resize-data` grows the data disk
// to the requested size while leaving the sentinel file on the ext4 volume
// intact after the VM is restarted.
func TestDataDiskGrowth(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 10)
	waitForDataMount(t, f, 120*time.Second)

	const sentinel = "exasol-growth-sentinel"
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

// waitForDataMount SSHes into the guest and polls /proc/mounts for the ext4
// data-disk mount at /var, retrying until timeout. Darwin-only because the
// windows launcher does not maintain a raw ext4 filesystem inside the WSL2
// backing VM (data lives on the volume-backed /exa mount).
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
