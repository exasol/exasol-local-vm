// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
