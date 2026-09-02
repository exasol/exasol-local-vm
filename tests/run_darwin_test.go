// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCommandInsideVM(t *testing.T) {
	// Given
	f := NewLauncherFixture(t)
	defer f.Cleanup()
	f.Init()
	f.StartVM(2, 4096, 10)

	t.Run("streams standard IO and preserves the remote exit code", func(t *testing.T) {
		// When
		cmd := exec.Command(
			f.BinaryPath,
			"run", "--", "sh", "-c",
			`read value; printf 'stdout:%s' "$value"; printf 'stderr:%s' "$value" >&2; exit 23`,
		)
		cmd.Dir = f.WorkDir
		cmd.Stdin = strings.NewReader("payload\n")
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()

		// Then
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 23 {
			t.Fatalf("launcher exit error = %v, want remote exit code 23", err)
		}
		if got, want := stdout.String(), "stdout:payload"; got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
		if got, want := stderr.String(), "stderr:payload"; got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
	})

	t.Run("provides Podman in the VM appliance", func(t *testing.T) {
		// When
		output := runVMCommand(t, f, "podman", "version", "--format", "{{.Client.Version}}")

		// Then
		if strings.TrimSpace(output) == "" {
			t.Fatal("podman version returned empty output")
		}
	})

	t.Run("maps files from the reported shared directory to /mnt/host", func(t *testing.T) {
		// Given
		state := f.VMState()
		if state.SharedDir == "" {
			t.Fatal("vm-state.json does not report shared_dir")
		}
		hostSharedDir := filepath.Join(f.WorkDir, filepath.Clean(state.SharedDir))
		const content = "launcher shared-directory boundary\n"
		if err := os.WriteFile(filepath.Join(hostSharedDir, "boundary.txt"), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write host shared file: %v", err)
		}

		// When
		output := runVMCommand(t, f, "cat", "/mnt/host/boundary.txt")

		// Then
		if output != content {
			t.Fatalf("guest shared file content = %q, want %q", output, content)
		}
	})

	t.Run("preserves directory metadata and sparse allocation in the shared directory", func(t *testing.T) {
		// Given
		state := f.VMState()
		hostSharedDir := filepath.Join(f.WorkDir, filepath.Clean(state.SharedDir))
		const (
			guestSource = "/var/copy-contract-source"
			guestTarget = "/mnt/host/copy-contract-target"
			fileSize    = int64(64 * 1024 * 1024)
		)
		runVMCommand(t, f, "sh", "-c", `
set -eu
rm -rf "$1" "$2"
mkdir -p "$1" "$2"
truncate -s "$3" "$1/sparse"
printf x | dd of="$1/sparse" bs=1 seek="$(( $3 - 1 ))" conv=notrunc status=none
chmod 0640 "$1/sparse"
touch -d @1577934245 "$1/sparse"
ln -s sparse "$1/link"
cp -a --sparse=always "$1/." "$2/"
sync
`, "sh", guestSource, guestTarget, fmt.Sprintf("%d", fileSize))

		// When
		targetDir := filepath.Join(hostSharedDir, "copy-contract-target")
		fileInfo, err := os.Stat(filepath.Join(targetDir, "sparse"))

		// Then
		if err != nil {
			t.Fatalf("failed to stat copied sparse file: %v", err)
		}
		if fileInfo.Size() != fileSize {
			t.Fatalf("copied file size = %d, want %d", fileInfo.Size(), fileSize)
		}
		if fileInfo.Mode().Perm() != 0640 {
			t.Fatalf("copied file mode = %o, want 640", fileInfo.Mode().Perm())
		}
		wantModTime := time.Unix(1577934245, 0)
		if !fileInfo.ModTime().Equal(wantModTime) {
			t.Fatalf("copied file modification time = %v, want %v", fileInfo.ModTime(), wantModTime)
		}
		stat, ok := fileInfo.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("unexpected stat type for copied sparse file")
		}
		if allocated := int64(stat.Blocks) * 512; allocated >= fileSize/2 {
			t.Fatalf("copied file allocated %d bytes for logical size %d", allocated, fileSize)
		}
		linkTarget, err := os.Readlink(filepath.Join(targetDir, "link"))
		if err != nil {
			t.Fatalf("failed to read copied symbolic link: %v", err)
		}
		if linkTarget != "sparse" {
			t.Fatalf("copied symbolic link target = %q, want sparse", linkTarget)
		}
	})

	t.Run("opens a VM shell when invoked from a terminal", func(t *testing.T) {
		// When
		cmd := exec.Command("/usr/bin/script", "-q", "/dev/null", f.BinaryPath, "run")
		cmd.Dir = f.WorkDir
		cmd.Stdin = strings.NewReader("printf 'vm-shell-ready\\n'\nexit\n")
		output, err := cmd.CombinedOutput()

		// Then
		if err != nil {
			t.Fatalf("interactive launcher run failed: %v\noutput:\n%s", err, output)
		}
		if !bytes.Contains(output, []byte("vm-shell-ready")) {
			t.Fatalf("interactive launcher run did not open a VM shell: %q", output)
		}
	})

	t.Run("does not expose SSH transport in public state", func(t *testing.T) {
		// When
		state, err := os.ReadFile(filepath.Join(f.WorkDir, "vm-state.json"))
		if err != nil {
			t.Fatalf("failed to read VM state: %v", err)
		}

		// Then
		for _, transportField := range []string{"vm_ip", "ssh_private_key", `"ssh"`} {
			if bytes.Contains(state, []byte(transportField)) {
				t.Fatalf("vm-state.json exposes SSH transport field %q: %s", transportField, state)
			}
		}
	})
}

func TestInitWithSSHKeySupportsGuestCommand(t *testing.T) {
	// Given
	f := NewLauncherFixture(t)
	defer f.Cleanup()
	privateKeyPath := filepath.Join(f.WorkDir, "imported-ed25519-key")
	keygen := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", privateKeyPath)
	if output, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("failed to generate test SSH key: %v\noutput:\n%s", err, output)
	}

	// When
	f.InitWithSSHKey(privateKeyPath)
	f.StartVM(2, 4096, 10)
	output := runVMCommand(t, f, "printf", "imported-key-ready")

	// Then
	if output != "imported-key-ready" {
		t.Fatalf("guest command output = %q, want imported-key-ready", output)
	}
}
