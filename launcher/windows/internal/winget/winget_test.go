// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package winget

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeWinget drops a POSIX shell script named "winget" into a
// fresh temp directory, prepends that directory to PATH for the duration
// of the test, and returns the path of an argv-log file the shim writes
// to. Mirrors installFakePodman in the sibling podman package.
func installFakeWinget(t *testing.T, body string) (argvLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake winget shim uses a POSIX shell script; skipping on windows")
	}

	dir := t.TempDir()
	argvLogPath = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + argvLogPath + "\"; done\n" +
		"printf -- '---\\n' >> \"" + argvLogPath + "\"\n" +
		body + "\n"
	binPath := filepath.Join(dir, "winget")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake winget shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLogPath
}

func readArgvCalls(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read argv log: %v", err)
	}
	var calls [][]string
	var current []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "---" {
			calls = append(calls, current)
			current = nil
			continue
		}
		if line == "" {
			continue
		}
		current = append(current, line)
	}
	return calls
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestInstallPodman_Success(t *testing.T) {
	logPath := installFakeWinget(t, `printf 'Downloading...\nInstalled.\n'; exit 0`)
	var buf bytes.Buffer
	if err := InstallPodman(&buf); err != nil {
		t.Fatalf("InstallPodman() unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Downloading") {
		t.Errorf("expected winget stdout in output writer, got %q", buf.String())
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 winget call, got %d: %v", len(calls), calls)
	}
	want := []string{
		"install",
		"--exact", "--id", "RedHat.Podman",
		"--source", "winget",
		"--scope", "user",
		"--accept-source-agreements",
		"--accept-package-agreements",
	}
	if !slicesEqual(calls[0], want) {
		t.Errorf("winget argv:\n  want: %v\n  got:  %v", want, calls[0])
	}
}

func TestInstallPodman_WingetMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty PATH → winget not found
	var buf bytes.Buffer
	err := InstallPodman(&buf)
	if err == nil {
		t.Fatal("expected error when winget is not on PATH")
	}
	if !strings.Contains(err.Error(), "winget install RedHat.Podman failed") {
		t.Errorf("error should mention the failed command: %v", err)
	}
}

func TestInstallPodman_WingetExitsNonZero(t *testing.T) {
	installFakeWinget(t, `echo "package not found" >&2; exit 1`)
	var buf bytes.Buffer
	err := InstallPodman(&buf)
	if err == nil {
		t.Fatal("expected error when winget exits non-zero")
	}
	if !strings.Contains(buf.String(), "package not found") {
		t.Errorf("expected winget stderr streamed to output writer, got %q", buf.String())
	}
}

func TestEnsurePodmanOnPath_Success(t *testing.T) {
	installDir := t.TempDir()
	// Simulate the installed layout: podman.exe (empty content is fine —
	// we only Stat the directory, not the binary).
	if err := os.WriteFile(filepath.Join(installDir, "podman.exe"), []byte(""), 0o644); err != nil {
		t.Fatalf("seed fake install: %v", err)
	}
	t.Setenv(podmanInstallDirOverrideEnv, installDir)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "/some/existing/path")

	if err := EnsurePodmanOnPath(); err != nil {
		t.Fatalf("EnsurePodmanOnPath() unexpected error: %v", err)
	}

	got := os.Getenv("PATH")
	wantPrefix := installDir + string(os.PathListSeparator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("PATH prefix: want %q, got %q", wantPrefix, got)
	}
	if !strings.Contains(got, "/some/existing/path") {
		t.Errorf("PATH did not preserve existing entries: %q", got)
	}
	_ = origPath // t.Setenv restores automatically on cleanup.
}

func TestEnsurePodmanOnPath_InstallDirMissing(t *testing.T) {
	// Point override at a directory that doesn't exist.
	t.Setenv(podmanInstallDirOverrideEnv, filepath.Join(t.TempDir(), "missing"))
	err := EnsurePodmanOnPath()
	if err == nil {
		t.Fatal("expected error when install dir does not exist")
	}
	if !strings.Contains(err.Error(), "podman install directory not found") {
		t.Errorf("error should describe missing install dir: %v", err)
	}
}

func TestEnsurePodmanOnPath_NonWindowsRequiresOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test asserts the non-windows fallback error; not applicable on windows")
	}
	// Unset any override so we exercise the fallback branch.
	t.Setenv(podmanInstallDirOverrideEnv, "")
	err := EnsurePodmanOnPath()
	if err == nil {
		t.Fatal("expected error on non-windows without override")
	}
	if !strings.Contains(err.Error(), "non-windows platform requires") {
		t.Errorf("error should call out the test-only override: %v", err)
	}
}
