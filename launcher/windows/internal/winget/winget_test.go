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
		"--accept-source-agreements",
		"--accept-package-agreements",
	}
	if !slicesEqual(calls[0], want) {
		t.Errorf("winget argv:\n  want: %v\n  got:  %v", want, calls[0])
	}
}

func TestPodmanInstallCommand(t *testing.T) {
	// The exact command string shown to the user before the launcher's
	// Y/n prompt. Kept in the same package as InstallPodman so any drift
	// between what we advertise and what we actually exec is caught here.
	got := PodmanInstallCommand()
	want := "winget install --exact --id RedHat.Podman --source winget " +
		"--accept-source-agreements --accept-package-agreements"
	if got != want {
		t.Errorf("PodmanInstallCommand() =\n  %q\nwant\n  %q", got, want)
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
	// Simulate the post-install state: the registry-refresh returns a
	// PATH-additions string containing a directory that already holds
	// the fake podman shim.
	installDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(installDir, "podman.exe"), []byte(""), 0o644); err != nil {
		t.Fatalf("seed fake install: %v", err)
	}
	t.Setenv(podmanPathAdditionsOverrideEnv, installDir)
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
}

func TestEnsurePodmanOnPath_MultipleAdditions(t *testing.T) {
	// Registry PATH may contain many entries (machine + user scopes
	// concatenated). All new-to-us entries land on PATH; duplicates
	// are dropped.
	sep := string(os.PathListSeparator)
	installDir := t.TempDir()
	otherDir := t.TempDir()
	t.Setenv("PATH", "/pre-existing"+sep+otherDir)
	t.Setenv(podmanPathAdditionsOverrideEnv,
		installDir+sep+otherDir+sep+"/system32")

	if err := EnsurePodmanOnPath(); err != nil {
		t.Fatalf("EnsurePodmanOnPath() unexpected error: %v", err)
	}
	got := os.Getenv("PATH")
	// installDir and /system32 are new; otherDir is a duplicate and
	// must NOT appear a second time.
	if !strings.Contains(got, installDir) {
		t.Errorf("expected new install dir in PATH: %q", got)
	}
	if !strings.Contains(got, "/system32") {
		t.Errorf("expected new /system32 in PATH: %q", got)
	}
	if strings.Count(got, otherDir) != 1 {
		t.Errorf("otherDir %q should appear exactly once, got PATH=%q", otherDir, got)
	}
	if !strings.Contains(got, "/pre-existing") {
		t.Errorf("PATH should preserve pre-existing entries: %q", got)
	}
}

func TestEnsurePodmanOnPath_EmptyAdditionsIsNoOp(t *testing.T) {
	before := "/before"
	t.Setenv("PATH", before)
	t.Setenv(podmanPathAdditionsOverrideEnv, "")
	if err := EnsurePodmanOnPath(); err != nil {
		t.Fatalf("EnsurePodmanOnPath() unexpected error: %v", err)
	}
	if got := os.Getenv("PATH"); got != before {
		t.Errorf("empty additions should leave PATH unchanged: got %q, want %q", got, before)
	}
}

func TestEnsurePodmanOnPath_NonWindowsRequiresOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test asserts the non-windows fallback error; not applicable on windows")
	}
	// Unset any override so we exercise the fallback branch. Using
	// Unsetenv (not Setenv with "") because LookupEnv distinguishes
	// unset from empty, and the "" case is now a valid no-op input.
	os.Unsetenv(podmanPathAdditionsOverrideEnv)
	t.Cleanup(func() { os.Unsetenv(podmanPathAdditionsOverrideEnv) })
	err := EnsurePodmanOnPath()
	if err == nil {
		t.Fatal("expected error on non-windows without override")
	}
	if !strings.Contains(err.Error(), "non-windows platform requires") {
		t.Errorf("error should call out the test-only override: %v", err)
	}
}

func TestMergePATHPrependsNewEntries(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := mergePATH("/a"+sep+"/b", "/new")
	want := "/new" + sep + "/a" + sep + "/b"
	if got != want {
		t.Errorf("mergePATH(existing, /new) = %q, want %q", got, want)
	}
}

func TestMergePATHDropsDuplicatesCaseInsensitive(t *testing.T) {
	sep := string(os.PathListSeparator)
	// /a already exists (in a different case on Windows-style
	// comparison); /new is genuinely new.
	got := mergePATH("/A"+sep+"/b", "/a"+sep+"/new")
	want := "/new" + sep + "/A" + sep + "/b"
	if got != want {
		t.Errorf("mergePATH dedup: got %q, want %q", got, want)
	}
}

func TestMergePATHEmptyCurrent(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := mergePATH("", "/a"+sep+"/b")
	want := "/a" + sep + "/b"
	if got != want {
		t.Errorf("mergePATH empty current: got %q, want %q", got, want)
	}
}

func TestMergePATHEmptyAdditional(t *testing.T) {
	got := mergePATH("/a", "")
	if got != "/a" {
		t.Errorf("mergePATH empty additional: got %q, want %q", got, "/a")
	}
}
